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

package remote

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"go.chromium.org/luci/config"

	. "github.com/smartystreets/goconvey/convey"
)

func encodeToB(s string, compress bool) string {
	var b []byte
	if compress {
		buf := &bytes.Buffer{}
		w := zlib.NewWriter(buf)
		if _, err := io.WriteString(w, s); err != nil {
			panic(err)
		}
		if err := w.Close(); err != nil {
			panic(err)
		}
		b = buf.Bytes()
	} else {
		b = []byte(s)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func testTools(code int, resp any) (*httptest.Server, config.Interface) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(code)
		w.Header().Set("Content-Type", "application/json")
		marsh, _ := json.Marshal(resp)
		fmt.Fprintln(w, string(marsh))
	}))

	u, err := url.Parse(server.URL)
	if err != nil {
		panic(err)
	}
	return server, New(u.Host, true, nil)
}

func TestRemoteCalls(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	Convey("Should pass through calls to the generated API", t, func() {
		Convey("GetConfig", func() {
			server, remoteImpl := testTools(200, map[string]string{
				"content":      encodeToB("hi", false),
				"content_hash": "bar",
				"revision":     "3",
				"url":          "config_url",
			})
			defer server.Close()

			res, err := remoteImpl.GetConfig(ctx, "a", "b", false)

			So(err, ShouldBeNil)
			So(*res, ShouldResemble, config.Config{
				Meta: config.Meta{
					ConfigSet:   "a",
					Path:        "b",
					ContentHash: "bar",
					Revision:    "3",
					ViewURL:     "config_url",
				},
				Content: "hi",
			})
		})
		Convey("GetConfig (zlib)", func() {
			server, remoteImpl := testTools(200, map[string]any{
				"content":            encodeToB("hi", true),
				"content_hash":       "bar",
				"is_zlib_compressed": true,
				"revision":           "3",
				"url":                "config_url",
			})
			defer server.Close()

			res, err := remoteImpl.GetConfig(ctx, "a", "b", false)

			So(err, ShouldBeNil)
			So(*res, ShouldResemble, config.Config{
				Meta: config.Meta{
					ConfigSet:   "a",
					Path:        "b",
					ContentHash: "bar",
					Revision:    "3",
					ViewURL:     "config_url",
				},
				Content: "hi",
			})
		})
		Convey("ListFiles", func() {
			server, remoteImpl := testTools(200,
				map[string]any{
					"config_sets": []any{
						map[string]any{
							"files": []any{
								map[string]any{
									"path": "first.template",
								},
								map[string]any{
									"path": "second.template",
								},
							},
							"config_set": "a",
						},
					},
				},
			)
			defer server.Close()

			res, err := remoteImpl.ListFiles(ctx, "a")

			So(err, ShouldBeNil)
			So(res, ShouldResemble, []string{"first.template", "second.template"})
		})
		Convey("GetProjectConfigs", func() {
			server, remoteImpl := testTools(200, map[string]any{
				"configs": [...]any{map[string]string{
					"config_set":   "a",
					"content":      encodeToB("hi", false),
					"content_hash": "bar",
					"revision":     "3",
				}},
			})
			defer server.Close()

			res, err := remoteImpl.GetProjectConfigs(ctx, "b", false)

			So(err, ShouldBeNil)
			So(res, ShouldNotBeEmpty)
			So(len(res), ShouldEqual, 1)
			So(res[0], ShouldResemble, config.Config{
				Meta: config.Meta{
					ConfigSet:   "a",
					Path:        "b",
					ContentHash: "bar",
					Revision:    "3",
				},
				Content: "hi",
			})
		})
		Convey("GetProjectConfigs metaOnly", func() {
			server, remoteImpl := testTools(200, map[string]any{
				"configs": [...]any{map[string]string{
					"config_set":   "a",
					"content_hash": "bar",
					"revision":     "3",
				}},
			})
			defer server.Close()

			res, err := remoteImpl.GetProjectConfigs(ctx, "b", true)

			So(err, ShouldBeNil)
			So(res, ShouldNotBeEmpty)
			So(len(res), ShouldEqual, 1)
			So(res[0], ShouldResemble, config.Config{
				Meta: config.Meta{
					ConfigSet:   "a",
					Path:        "b",
					ContentHash: "bar",
					Revision:    "3",
				},
			})
		})
		Convey("GetProjects", func() {
			id := "blink"
			name := "Blink"
			URL, err := url.Parse("http://example.com")
			if err != nil {
				panic(err)
			}

			server, remoteImpl := testTools(200, map[string]any{
				"projects": [...]any{map[string]string{
					"id":        id,
					"name":      name,
					"repo_type": "GITILES",
					"repo_url":  URL.String(),
				}},
			})
			defer server.Close()

			res, err := remoteImpl.GetProjects(ctx)

			So(err, ShouldBeNil)
			So(res, ShouldNotBeEmpty)
			So(len(res), ShouldEqual, 1)
			So(res[0], ShouldResemble, config.Project{
				ID:       id,
				Name:     name,
				RepoType: config.GitilesRepo,
				RepoURL:  URL,
			})
		})
	})

	Convey("Should handle errors well", t, func() {
		Convey("Should pass through HTTP errors", func() {
			remoteImpl := New("example.com", true, func(context.Context) (*http.Client, error) {
				return &http.Client{
					Transport: failingRoundTripper{},
				}, nil
			})

			_, err := remoteImpl.GetConfig(ctx, "a", "b", false)
			So(err, ShouldNotBeNil)
			_, err = remoteImpl.GetProjectConfigs(ctx, "a", false)
			So(err, ShouldNotBeNil)
			_, err = remoteImpl.GetProjects(ctx)
			So(err, ShouldNotBeNil)
		})
	})
}

type failingRoundTripper struct{}

func (t failingRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("IM AM ERRAR")
}
