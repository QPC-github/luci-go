// Copyright 2022 The LUCI Authors.
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

package pubsub

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"go.chromium.org/luci/bisection/compilefailuredetection"
	taskpb "go.chromium.org/luci/bisection/task/proto"
	buildbucketpb "go.chromium.org/luci/buildbucket/proto"
	. "go.chromium.org/luci/common/testing/assertions"
	"go.chromium.org/luci/server/tq"
)

func TestBuildBucketPubsub(t *testing.T) {
	t.Parallel()

	Convey("Buildbucket Pubsub Handler", t, func() {
		c, scheduler := tq.TestingContext(context.Background(), nil)
		compilefailuredetection.RegisterTaskClass()

		buildPubsub := &buildbucketpb.BuildsV2PubSub{
			Build: &buildbucketpb.Build{
				Id: 8000,
				Builder: &buildbucketpb.BuilderID{
					Project: "chromium",
					Bucket:  "ci",
				},
				Status: buildbucketpb.Status_FAILURE,
			},
		}
		r := &http.Request{Body: makeBBReq(buildPubsub)}
		err := buildbucketPubSubHandlerImpl(c, r)
		So(err, ShouldBeNil)
		// Check that a test was created
		task := &taskpb.FailedBuildIngestionTask{
			Bbid: 8000,
		}
		expected := proto.Clone(task).(*taskpb.FailedBuildIngestionTask)
		So(scheduler.Tasks().Payloads()[0], ShouldResembleProto, expected)
	})
}

func makeBBReq(message *buildbucketpb.BuildsV2PubSub) io.ReadCloser {
	bm, err := protojson.Marshal(message)
	if err != nil {
		panic(err)
	}

	attributes := map[string]any{
		"version": "v2",
	}

	msg := struct {
		Message struct {
			Data       []byte
			Attributes map[string]any
		}
	}{struct {
		Data       []byte
		Attributes map[string]any
	}{Data: bm, Attributes: attributes}}
	jmsg, _ := json.Marshal(msg)
	return io.NopCloser(bytes.NewReader(jmsg))
}
