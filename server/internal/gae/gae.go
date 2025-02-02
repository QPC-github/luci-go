// Copyright 2021 The LUCI Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package gae implements minimal support for using some bundled GAE APIs.
//
// It is essentially a reimplementation of a small subset of Golang GAE SDK v2,
// since the SDK is not very interoperable with non-GAE code,
// see e.g. https://github.com/golang/appengine/issues/265.
//
// Following sources were used as a base:
//   - Repo: https://github.com/golang/appengine
//   - Revision: 6d50fa847719498e759db6d80533dde0284307b3
//
// Some proto files were copied, and their proto package and `go_package`
// updated to reflect their new location to avoid clashing with real GAE SDK
// protos in the registry and to conform to modern protoc-gen-go requirement
// of using full Go package paths:
//   - v2/internal/base/api_base.proto => base/
//   - v2/internal/mail/mail_service.proto => mail/
//   - v2/internal/remote_api/remote_api.proto => remote_api/
//
// The rest is written from scratch based on what the SDK is doing.
package gae

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"go.chromium.org/luci/common/errors"
	"go.chromium.org/luci/common/trace"

	remotepb "go.chromium.org/luci/server/internal/gae/remote_api"
)

//go:generate cproto base
//go:generate cproto mail
//go:generate cproto remote_api

var ticketsContextKey = "go.chromium.org/luci/server/internal/gae.Tickets"

// Note: Go GAE SDK attempts to limit the number of concurrent connections using
// a hand-rolled semaphore-based dialer. It is not clear why it can't just use
// MaxConnsPerHost. We use MaxConnsPerHost below for simplicity. We also don't
// anticipate this client to be used with a ton of concurrent requests yet.
var apiHTTPClient = &http.Client{
	Transport: &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		MaxConnsPerHost:     200,
		MaxIdleConnsPerHost: 100,
		IdleConnTimeout:     90 * time.Second,
	},
}

// Tickets lives in context.Context and carries per-request information.
type Tickets struct {
	api         string   // API ticket identifying the incoming HTTP request
	dapperTrace string   // Dapper Trace ticket
	cloudTrace  string   // Cloud Trace ticket
	apiURL      *url.URL // URL of the service bridge (overridden in tests)
}

// Headers knows how to return request headers.
type Headers interface {
	Header(string) string
}

// DefaultTickets generates default background tickets.
//
// They are used for calls outside of request handlers. Uses GAE environment
// variables.
func DefaultTickets() *Tickets {
	return &Tickets{
		api: fmt.Sprintf("%s/%s.%s.%s",
			strings.Replace(strings.Replace(os.Getenv("GOOGLE_CLOUD_PROJECT"), ":", "_", -1), ".", "_", -1),
			os.Getenv("GAE_SERVICE"),
			os.Getenv("GAE_VERSION"),
			os.Getenv("GAE_INSTANCE"),
		),
	}
}

// RequestTickets extracts tickets from incoming request headers.
func RequestTickets(headers Headers) *Tickets {
	return &Tickets{
		api:         headers.Header("X-Appengine-Api-Ticket"),
		dapperTrace: headers.Header("X-Google-Dappertraceinfo"),
		cloudTrace:  headers.Header("X-Cloud-Trace-Context"),
	}
}

// WithTickets puts the tickets into the context.Context.
func WithTickets(ctx context.Context, tickets *Tickets) context.Context {
	return context.WithValue(ctx, &ticketsContextKey, tickets)
}

// Call makes an RPC to the GAE service bridge.
//
// Uses tickets in the context (see WithTickets). Returns an error if they are
// not there.
//
// Note: currently returns opaque stringy errors. Refactor if you need to
// distinguish API errors from transport errors or need error codes, etc.
func Call(ctx context.Context, service, method string, in, out proto.Message) (err error) {
	ctx, span := trace.StartSpan(ctx, fmt.Sprintf("luci/gae.Call/%s.%s", service, method))
	defer func() { span.End(err) }()

	tickets, _ := ctx.Value(&ticketsContextKey).(*Tickets)
	if tickets == nil {
		return errors.Reason("no GAE API ticket in the context when calling %s.%s", service, method).Err()
	}

	data, err := proto.Marshal(in)
	if err != nil {
		return errors.Annotate(err, "failed to marshal RPC request to %s.%s", service, method).Err()
	}

	postBody, err := proto.Marshal(&remotepb.Request{
		ServiceName: &service,
		Method:      &method,
		Request:     data,
		RequestId:   &tickets.api,
	})
	if err != nil {
		return errors.Annotate(err, "failed to marshal RPC request to %s.%s", service, method).Err()
	}

	respBody, err := postToServiceBridge(ctx, tickets, postBody)
	if err != nil {
		return errors.Annotate(err, "failed to call GAE service bridge for %s.%s", service, method).Err()
	}

	res := &remotepb.Response{}
	if err := proto.Unmarshal(respBody, res); err != nil {
		return errors.Annotate(err, "unexpected response from GAE service bridge for %s.%s", service, method).Err()
	}

	if res.RpcError != nil {
		return errors.Reason(
			"RPC error %s calling %s.%s: %s",
			remotepb.RpcError_ErrorCode(res.RpcError.GetCode()),
			service, method, res.RpcError.GetDetail(),
		).Err()
	}

	if res.ApplicationError != nil {
		return errors.Reason(
			"API error %d calling %s.%s: %s",
			res.ApplicationError.GetCode(),
			service, method, res.ApplicationError.GetDetail(),
		).Err()
	}

	// This should not be happening.
	if res.Exception != nil || res.JavaException != nil {
		return errors.Reason(
			"service bridge returned unexpected exception from %s.%s",
			service, method,
		).Err()
	}

	if err := proto.Unmarshal(res.Response, out); err != nil {
		return errors.Annotate(err, "failed to unmarshal response of %s.%s", service, method).Err()
	}
	return nil
}

// apiURL is the URL of the local GAE service bridge.
func apiURL() *url.URL {
	host, port := "appengine.googleapis.internal", "10001"
	if h := os.Getenv("API_HOST"); h != "" {
		host = h
	}
	if p := os.Getenv("API_PORT"); p != "" {
		port = p
	}
	return &url.URL{
		Scheme: "http",
		Host:   host + ":" + port,
		Path:   "/rpc_http",
	}
}

// postToServiceBridge makes an HTTP POST request to the GAE service bridge.
func postToServiceBridge(ctx context.Context, tickets *Tickets, body []byte) ([]byte, error) {
	// Either get the existing context timeout or create the default 60 sec one.
	timeout := time.Minute
	if deadline, ok := ctx.Deadline(); ok {
		timeout = time.Until(deadline)
	} else {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	url := tickets.apiURL
	if url == nil {
		url = apiURL()
	}

	req := &http.Request{
		Method: "POST",
		URL:    url,
		Header: http.Header{
			"X-Google-Rpc-Service-Endpoint": []string{"app-engine-apis"},
			"X-Google-Rpc-Service-Method":   []string{"/VMRemoteAPI.CallRemoteAPI"},
			"X-Google-Rpc-Service-Deadline": []string{strconv.FormatFloat(timeout.Seconds(), 'f', -1, 64)},
			"Content-Type":                  []string{"application/octet-stream"},
		},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Host:          url.Host,
	}
	if tickets.dapperTrace != "" {
		req.Header.Set("X-Google-Dappertraceinfo", tickets.dapperTrace)
	}
	if tickets.cloudTrace != "" {
		req.Header.Set("X-Cloud-Trace-Context", tickets.cloudTrace)
	}

	res, err := apiHTTPClient.Do(req.WithContext(ctx))
	if err != nil {
		return nil, errors.Annotate(err, "failed to make HTTP call").Err()
	}
	defer res.Body.Close()

	switch body, err := io.ReadAll(res.Body); {
	case err != nil:
		return nil, errors.Annotate(err, "failed to read HTTP %d response", res.StatusCode).Err()
	case res.StatusCode != 200:
		return nil, errors.Reason("unexpected HTTP %d: %q", res.StatusCode, body).Err()
	default:
		return body, nil
	}
}
