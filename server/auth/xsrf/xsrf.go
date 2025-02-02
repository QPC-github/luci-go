// Copyright 2015 The LUCI Authors.
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

// Package xsrf provides Cross Site Request Forgery prevention middleware.
//
// Usage:
//  1. When serving GET request put hidden "xsrf_token" input field with
//     the token value into the form. Use TokenField(...) to generate it.
//  2. Wrap POST-handling route with WithTokenCheck(...) middleware.
package xsrf

import (
	"context"
	"fmt"
	"html"
	"html/template"
	"net/http"
	"time"

	"go.chromium.org/luci/common/logging"
	"go.chromium.org/luci/common/retry/transient"

	"go.chromium.org/luci/server/auth"
	"go.chromium.org/luci/server/router"
	"go.chromium.org/luci/server/tokens"
)

// xsrfToken described how to generate tokens.
var xsrfToken = tokens.TokenKind{
	Algo:       tokens.TokenAlgoHmacSHA256,
	Expiration: 4 * time.Hour,
	SecretKey:  "xsrf_token",
	Version:    1,
}

// Token generates new XSRF token bound to the current caller.
//
// The token is URL safe base64 encoded string. It lives for 4 hours and may
// potentially be used multiple times (i.e. the token is stateless).
//
// Put it in hidden form field under the name of "xsrf_token", e.g.
// <input type="hidden" name="xsrf_token" value="{{.XsrfToken}}">.
//
// Later WithTokenCheck will grab it from there and verify its validity.
func Token(ctx context.Context) (string, error) {
	return xsrfToken.Generate(ctx, state(ctx), nil, 0)
}

// Check returns nil if XSRF token is valid.
func Check(ctx context.Context, tok string) error {
	_, err := xsrfToken.Validate(ctx, tok, state(ctx))
	return err
}

// TokenField generates "<input type="hidden" ...>" field with the token.
//
// It can be put into HTML forms directly. Panics on errors.
func TokenField(ctx context.Context) template.HTML {
	tok, err := Token(ctx)
	if err != nil {
		panic(err)
	}
	return template.HTML(fmt.Sprintf(`<input type="hidden" name="xsrf_token" value="%s">`, html.EscapeString(tok)))
}

// WithTokenCheck is middleware that checks validity of XSRF tokens.
//
// If searches for the token in "xsrf_token" POST form field (as generated by
// TokenField). Aborts the request with HTTP 403 if XSRF token is missing or
// invalid.
func WithTokenCheck(c *router.Context, next router.Handler) {
	tok := c.Request.PostFormValue("xsrf_token")
	if tok == "" {
		replyError(c.Context, c.Writer, http.StatusForbidden, "XSRF token is missing")
		return
	}
	switch err := Check(c.Context, tok); {
	case transient.Tag.In(err):
		replyError(c.Context, c.Writer, http.StatusInternalServerError, "Transient error when checking XSRF token - %s", err)
	case err != nil:
		replyError(c.Context, c.Writer, http.StatusForbidden, "Bad XSRF token - %s", err)
	default:
		next(c)
	}
}

///

// state must return exact same value when generating and verifying token for
// the verification to succeed.
func state(ctx context.Context) []byte {
	return []byte(auth.CurrentUser(ctx).Identity)
}

// replyError sends error response and logs it.
func replyError(ctx context.Context, rw http.ResponseWriter, code int, msg string, args ...any) {
	text := fmt.Sprintf(msg, args...)
	logging.Errorf(ctx, "xsrf: %s", text)
	http.Error(rw, text, code)
}
