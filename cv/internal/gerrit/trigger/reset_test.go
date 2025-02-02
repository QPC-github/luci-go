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

package trigger

import (
	"fmt"
	"strconv"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"go.chromium.org/luci/common/clock"
	gerritpb "go.chromium.org/luci/common/proto/gerrit"
	"go.chromium.org/luci/common/retry/transient"
	"go.chromium.org/luci/gae/service/datastore"

	"github.com/smartystreets/goconvey/convey"
	cfgpb "go.chromium.org/luci/cv/api/config/v2"
	"go.chromium.org/luci/cv/internal/changelist"
	"go.chromium.org/luci/cv/internal/configs/prjcfg"
	"go.chromium.org/luci/cv/internal/cvtesting"
	"go.chromium.org/luci/cv/internal/gerrit"
	gf "go.chromium.org/luci/cv/internal/gerrit/gerritfake"
	"go.chromium.org/luci/cv/internal/run"
	"go.chromium.org/luci/cv/internal/usertext"

	. "github.com/smartystreets/assertions"
	. "go.chromium.org/luci/common/testing/assertions"
)

// Convey package also exports `Reset` method that conflicts with the function
// in this package. This is a workaround since we can not dot import the convey
// package.
var Convey = convey.Convey

func TestReset(t *testing.T) {
	t.Parallel()

	Convey("Reset", t, func() {
		ct := cvtesting.Test{}
		ctx, cancel := ct.SetUp()
		defer cancel()

		const ownerID int64 = 5
		const reviewerID int64 = 50
		const triggererID int64 = 100
		triggerer := gf.U(fmt.Sprintf("user-%d", triggererID))
		const gHost = "x-review.example.com"
		const lProject = "lProject"
		const changeNum = 10001
		triggerTime := ct.Clock.Now().Add(-2 * time.Minute)
		ci := gf.CI(
			10001, gf.PS(2),
			gf.Owner(fmt.Sprintf("user-%d", ownerID)),
			gf.CQ(2, triggerTime, triggerer),
			gf.Updated(clock.Now(ctx).Add(-1*time.Minute)),
			gf.Reviewer(gf.U(fmt.Sprintf("user-%d", reviewerID))),
		)
		triggers := Find(&FindInput{ChangeInfo: ci, ConfigGroup: &cfgpb.ConfigGroup{}})
		So(triggers.GetCqVoteTrigger(), ShouldResembleProto, &run.Trigger{
			Time:            timestamppb.New(triggerTime),
			Mode:            string(run.FullRun),
			Email:           fmt.Sprintf("user-%d@example.com", triggererID),
			GerritAccountId: triggererID,
		})
		So(triggers.GetCqVoteTrigger().GerritAccountId, ShouldEqual, 100)
		cl := &changelist.CL{
			ID:         99999,
			ExternalID: changelist.MustGobID(gHost, int64(changeNum)),
			EVersion:   2,
			Snapshot: &changelist.Snapshot{
				ExternalUpdateTime:    timestamppb.New(clock.Now(ctx).Add(-3 * time.Minute)),
				LuciProject:           lProject,
				Patchset:              2,
				MinEquivalentPatchset: 1,
				Kind: &changelist.Snapshot_Gerrit{
					Gerrit: &changelist.Gerrit{
						Host: gHost,
						Info: proto.Clone(ci).(*gerritpb.ChangeInfo),
					},
				},
			},
			TriggerNewPatchsetRunAfterPS: 1,
		}
		So(datastore.Put(ctx, cl), ShouldBeNil)
		ct.GFake.CreateChange(&gf.Change{
			Host: gHost,
			Info: proto.Clone(ci).(*gerritpb.ChangeInfo),
			ACLs: gf.ACLGrant(gf.OpRead, codes.PermissionDenied, lProject).Or(
				gf.ACLGrant(gf.OpReview, codes.PermissionDenied, lProject),
				gf.ACLGrant(gf.OpAlterVotesOfOthers, codes.PermissionDenied, lProject),
			),
		})

		input := ResetInput{
			CL: cl,
			ConfigGroups: []*prjcfg.ConfigGroup{{
				Content: &cfgpb.ConfigGroup{
					Verifiers: &cfgpb.Verifiers{Tryjob: &cfgpb.Verifiers_Tryjob{
						Builders: []*cfgpb.Verifiers_Tryjob_Builder{{
							Name:          "new patchset upload builder",
							ModeAllowlist: []string{string(run.NewPatchsetRun)},
						}},
					}},
				},
			}},
			LUCIProject:       lProject,
			Message:           "Full Run has passed",
			Requester:         "test",
			Notify:            gerrit.Whoms{gerrit.Owner, gerrit.CQVoters},
			AddToAttentionSet: gerrit.Whoms{gerrit.Reviewers},
			AttentionReason:   usertext.StoppedRun,
			LeaseDuration:     30 * time.Second,
			CLMutator:         changelist.NewMutator(ct.TQDispatcher, nil, nil, nil),
			GFactory:          ct.GFactory(),
		}
		findTriggers := func(resultCI *gerritpb.ChangeInfo) *run.Triggers {
			for _, cg := range input.ConfigGroups {
				if ts := Find(&FindInput{ChangeInfo: resultCI, ConfigGroup: cg.Content}); ts != nil {
					return ts
				}
			}
			return nil
		}
		ts := findTriggers(ci)
		cqTrigger := ts.GetCqVoteTrigger()
		nprTrigger := ts.GetNewPatchsetRunTrigger()
		input.Triggers = &run.Triggers{}

		Convey("Fails PreCondition if CL is AccessDenied from code review site", func() {
			Convey("For CQ-Label trigger", func() {
				input.Triggers.CqVoteTrigger = cqTrigger
			})
			Convey("For NewPatchset trigger", func() {
				input.Triggers.NewPatchsetRunTrigger = nprTrigger
			})
			noAccessTime := ct.Clock.Now().UTC().Add(1 * time.Minute)
			cl.Access = &changelist.Access{
				ByProject: map[string]*changelist.Access_Project{
					lProject: {
						UpdateTime:   timestamppb.New(noAccessTime),
						NoAccessTime: timestamppb.New(noAccessTime),
					},
				},
			}
			err := Reset(ctx, input)
			So(err, ShouldErrLike, "failed to reset trigger because CV lost access to this CL")
			So(ErrResetPreconditionFailedTag.In(err), ShouldBeTrue)
		})

		Convey("Fails PreCondition if CL has newer PS in datastore", func() {
			input.Triggers.CqVoteTrigger = cqTrigger
			newCI := proto.Clone(ci).(*gerritpb.ChangeInfo)
			gf.PS(3)(newCI)
			newCL := &changelist.CL{
				ID:         99999,
				ExternalID: changelist.MustGobID(gHost, int64(changeNum)),
				EVersion:   3,
				Snapshot: &changelist.Snapshot{
					ExternalUpdateTime:    timestamppb.New(clock.Now(ctx).Add(-1 * time.Minute)),
					LuciProject:           lProject,
					Patchset:              3,
					MinEquivalentPatchset: 3,
					Kind: &changelist.Snapshot_Gerrit{
						Gerrit: &changelist.Gerrit{
							Host: gHost,
							Info: newCI,
						},
					},
				},
			}
			So(datastore.Put(ctx, newCL), ShouldBeNil)
			err := Reset(ctx, input)
			So(err, ShouldErrLike, "failed to cancel because ps 2 is not current for cl(99999)")
			So(ErrResetPreconditionFailedTag.In(err), ShouldBeTrue)
		})

		Convey("Fails PreCondition if CL has newer PS in Gerrit", func() {
			input.Triggers.CqVoteTrigger = cqTrigger
			ct.GFake.MutateChange(gHost, int(ci.GetNumber()), func(c *gf.Change) {
				gf.PS(3)(c.Info)
			})
			err := Reset(ctx, input)
			So(err, ShouldErrLike, "failed to cancel because ps 2 is not current for x-review.example.com/10001")
			So(ErrResetPreconditionFailedTag.In(err), ShouldBeTrue)
		})

		Convey("Cancelling CQ Vote fails if receive stale data from gerrit", func() {
			input.Triggers.CqVoteTrigger = cqTrigger
			ct.GFake.MutateChange(gHost, int(ci.GetNumber()), func(c *gf.Change) {
				gf.Updated(clock.Now(ctx).Add(-3 * time.Minute))(c.Info)
			})
			err := Reset(ctx, input)
			So(err, ShouldErrLike, gerrit.ErrStaleData)
			So(transient.Tag.In(err), ShouldBeTrue)
		})

		Convey("Cancelling NewPatchsetRun", func() {
			input.Triggers.NewPatchsetRunTrigger = nprTrigger
			input.Message = "reset new patchset run trigger"

			cl := &changelist.CL{ID: input.CL.ID}
			So(datastore.Get(ctx, cl), ShouldBeNil)
			originalValue := cl.TriggerNewPatchsetRunAfterPS

			So(Reset(ctx, input), ShouldBeNil)

			cl = &changelist.CL{ID: input.CL.ID}
			So(datastore.Get(ctx, cl), ShouldBeNil)
			So(cl.TriggerNewPatchsetRunAfterPS, ShouldNotEqual, originalValue)
			So(cl.TriggerNewPatchsetRunAfterPS, ShouldEqual, input.CL.Snapshot.Patchset)
			change := ct.GFake.GetChange(input.CL.Snapshot.GetGerrit().GetHost(), int(input.CL.Snapshot.GetGerrit().GetInfo().GetNumber()))
			So(change.Info.GetMessages()[len(change.Info.GetMessages())-1].Message, ShouldEqual, input.Message)
		})

		splitSetReviewRequests := func() (onBehalf, asSelf []*gerritpb.SetReviewRequest) {
			for _, req := range ct.GFake.Requests() {
				switch r, ok := req.(*gerritpb.SetReviewRequest); {
				case !ok:
				case r.GetOnBehalfOf() != 0:
					// OnBehalfOf removes votes and must happen before any asSelf.
					So(asSelf, ShouldBeEmpty)
					onBehalf = append(onBehalf, r)
				default:
					asSelf = append(asSelf, r)
				}
			}
			return onBehalf, asSelf
		}
		Convey("cancel new patchset run and cq vote run at the same time", func() {
			input.Triggers.CqVoteTrigger = cqTrigger
			input.Triggers.NewPatchsetRunTrigger = nprTrigger
			cl := &changelist.CL{ID: input.CL.ID}
			So(datastore.Get(ctx, cl), ShouldBeNil)
			originalValue := cl.TriggerNewPatchsetRunAfterPS

			err := Reset(ctx, input)
			So(err, ShouldBeNil)
			resultCI := ct.GFake.GetChange(gHost, int(ci.GetNumber()))
			So(resultCI.Info.GetMessages(), ShouldHaveLength, 1)
			So(resultCI.Info.GetMessages()[0].GetMessage(), ShouldEqual, input.Message)
			So(gf.NonZeroVotes(resultCI.Info, CQLabelName), ShouldBeEmpty)

			onBehalfs, asSelf := splitSetReviewRequests()
			So(onBehalfs, ShouldHaveLength, 1)
			So(onBehalfs[0].GetOnBehalfOf(), ShouldEqual, triggererID)
			So(onBehalfs[0].GetNotifyDetails(), ShouldBeNil)
			So(asSelf, ShouldHaveLength, 1)
			So(asSelf[0].GetNotify(), ShouldEqual, gerritpb.Notify_NOTIFY_NONE)
			So(asSelf[0].GetNotifyDetails(), ShouldResembleProto,
				&gerritpb.NotifyDetails{
					Recipients: []*gerritpb.NotifyDetails_Recipient{
						{
							RecipientType: gerritpb.NotifyDetails_RECIPIENT_TYPE_TO,
							Info: &gerritpb.NotifyDetails_Info{
								Accounts: []int64{ownerID, triggererID},
							},
						},
					},
				})
			So(asSelf[0].GetAddToAttentionSet(), ShouldResembleProto, []*gerritpb.AttentionSetInput{
				{User: strconv.FormatInt(reviewerID, 10), Reason: "ps#2: " + usertext.StoppedRun},
			})
			cl = &changelist.CL{ID: input.CL.ID}
			So(datastore.Get(ctx, cl), ShouldBeNil)
			So(cl.TriggerNewPatchsetRunAfterPS, ShouldNotEqual, originalValue)
			So(cl.TriggerNewPatchsetRunAfterPS, ShouldEqual, input.CL.Snapshot.Patchset)
		})
		Convey("Remove single vote", func() {
			input.Triggers.CqVoteTrigger = cqTrigger
			err := Reset(ctx, input)
			So(err, ShouldBeNil)
			resultCI := ct.GFake.GetChange(gHost, int(ci.GetNumber()))
			So(resultCI.Info.GetMessages(), ShouldHaveLength, 1)
			So(resultCI.Info.GetMessages()[0].GetMessage(), ShouldEqual, input.Message)
			So(gf.NonZeroVotes(resultCI.Info, CQLabelName), ShouldBeEmpty)

			onBehalfs, asSelf := splitSetReviewRequests()
			So(onBehalfs, ShouldHaveLength, 1)
			So(onBehalfs[0].GetOnBehalfOf(), ShouldEqual, triggererID)
			So(onBehalfs[0].GetNotifyDetails(), ShouldBeNil)
			So(asSelf, ShouldHaveLength, 1)
			So(asSelf[0].GetNotify(), ShouldEqual, gerritpb.Notify_NOTIFY_NONE)
			So(asSelf[0].GetNotifyDetails(), ShouldResembleProto,
				&gerritpb.NotifyDetails{
					Recipients: []*gerritpb.NotifyDetails_Recipient{
						{
							RecipientType: gerritpb.NotifyDetails_RECIPIENT_TYPE_TO,
							Info: &gerritpb.NotifyDetails_Info{
								Accounts: []int64{ownerID, triggererID},
							},
						},
					},
				})
			So(asSelf[0].GetAddToAttentionSet(), ShouldResembleProto, []*gerritpb.AttentionSetInput{
				{User: strconv.FormatInt(reviewerID, 10), Reason: "ps#2: " + usertext.StoppedRun},
			})
			So(asSelf[0].GetTag(), ShouldResemble, fmt.Sprintf("autogenerated:cq:full-run:%d", triggerTime.Unix()))
		})

		Convey("Remove multiple votes", func() {
			input.Triggers.CqVoteTrigger = cqTrigger
			ct.GFake.MutateChange(gHost, int(ci.GetNumber()), func(c *gf.Change) {
				gf.CQ(1, clock.Now(ctx).Add(-130*time.Second), gf.U("user-1"))(c.Info)
				gf.CQ(2, clock.Now(ctx).Add(-110*time.Second), gf.U("user-70"))(c.Info)
				gf.CQ(1, clock.Now(ctx).Add(-100*time.Second), gf.U("user-1000"))(c.Info)
			})

			Convey("Success", func() {
				err := Reset(ctx, input)
				So(err, ShouldBeNil)
				resultCI := ct.GFake.GetChange(gHost, int(ci.GetNumber()))
				So(resultCI.Info.GetMessages(), ShouldHaveLength, 1)
				So(resultCI.Info.GetMessages()[0].GetMessage(), ShouldEqual, input.Message)
				So(gf.NonZeroVotes(resultCI.Info, CQLabelName), ShouldBeEmpty)

				onBehalfs, asSelf := splitSetReviewRequests()
				for _, r := range onBehalfs {
					So(r.GetNotify(), ShouldEqual, gerritpb.Notify_NOTIFY_NONE)
					So(r.GetNotifyDetails(), ShouldBeNil)
				}
				// The triggering vote(s) must have been removed last, the order of
				// removals for the rest doesn't matter so long as it does the job.
				So(onBehalfs[len(onBehalfs)-1].GetOnBehalfOf(), ShouldEqual, 100)
				So(asSelf, ShouldHaveLength, 1)
				So(asSelf[0].GetNotify(), ShouldEqual, gerritpb.Notify_NOTIFY_NONE)
				So(asSelf[0].GetNotifyDetails(), ShouldResembleProto,
					&gerritpb.NotifyDetails{
						Recipients: []*gerritpb.NotifyDetails_Recipient{
							{
								RecipientType: gerritpb.NotifyDetails_RECIPIENT_TYPE_TO,
								Info: &gerritpb.NotifyDetails_Info{
									Accounts: []int64{1, ownerID, 70, triggererID, 1000},
								},
							},
						},
					})
				So(asSelf[0].GetAddToAttentionSet(), ShouldResembleProto, []*gerritpb.AttentionSetInput{
					{User: strconv.FormatInt(reviewerID, 10), Reason: "ps#2: " + usertext.StoppedRun},
				})
			})

			Convey("Removing non-triggering votes fails", func() {
				ct.GFake.MutateChange(gHost, int(ci.GetNumber()), func(c *gf.Change) {
					c.ACLs = gf.ACLGrant(gf.OpRead, codes.PermissionDenied, lProject).Or(
						gf.ACLGrant(gf.OpReview, codes.PermissionDenied, lProject),
					) // no permission to vote on behalf of others
				})
				err := Reset(ctx, input)
				So(err, ShouldBeNil)
				onBehalfs, _ := splitSetReviewRequests()
				So(onBehalfs, ShouldHaveLength, 3) // all non-triggering votes
				for _, r := range onBehalfs {
					switch r.GetOnBehalfOf() {
					case triggererID:
						// CV shouldn't remove triggering votes if removal of non-triggering
						// votes fails.
						So(r.GetOnBehalfOf(), ShouldNotEqual, triggererID)
					case 1, 70, 1000:
					default:
						panic(fmt.Errorf("unknown on_behalf_of %d", r.GetOnBehalfOf()))
					}
				}
			})
		})

		Convey("Removing votes from non-CQ labels used in additional modes", func() {
			const uLabel = "Ultra-Quick-Label"
			const qLabel = "Quick-Label"
			input.Triggers.CqVoteTrigger = cqTrigger
			input.ConfigGroups = []*prjcfg.ConfigGroup{
				{
					Content: &cfgpb.ConfigGroup{
						AdditionalModes: []*cfgpb.Mode{
							{
								Name:            "ULTRA_QUICK_RUN",
								CqLabelValue:    1,
								TriggeringLabel: uLabel,
								TriggeringValue: 1,
							},
							{
								Name:            "QUICK_RUN",
								CqLabelValue:    1,
								TriggeringLabel: qLabel,
								TriggeringValue: 1,
							},
						},
					},
				},
			}

			ultraQuick := func(value int, timeAndUser ...any) gf.CIModifier {
				return gf.Vote(uLabel, value, timeAndUser...)
			}
			quick := func(value int, timeAndUser ...any) gf.CIModifier {
				return gf.Vote(qLabel, value, timeAndUser...)
			}
			// Exact timestamps don't matter in this test, but in practice they affect
			// computation of the triggering vote.
			ct.GFake.MutateChange(gHost, int(ci.GetNumber()), func(c *gf.Change) {
				// user-99 forgot to vote CQ+1.
				quick(1, clock.Now(ctx).Add(-300*time.Second), gf.U("user-99"))(c.Info)
				ultraQuick(1, clock.Now(ctx).Add(-200*time.Second), gf.U("user-99"))(c.Info)

				// user-100 actually triggered an ULTRA_QUICK_RUN.
				gf.CQ(1, clock.Now(ctx).Add(-150*time.Second), gf.U("user-100"))(c.Info)
				ultraQuick(1, clock.Now(ctx).Add(-150*time.Second), gf.U("user-100"))(c.Info)
				quick(1, clock.Now(ctx).Add(-150*time.Second), gf.U("user-100"))(c.Info)

				// user-101 CQ+1 was a noop.
				gf.CQ(1, clock.Now(ctx).Add(-120*time.Second), gf.U("user-101"))(c.Info)

				// user-102 votes for a QUICK_RUN is a noop, but should be removed as
				// as well.
				gf.CQ(1, clock.Now(ctx).Add(-110*time.Second), gf.U("user-101"))(c.Info)
				quick(1, clock.Now(ctx).Add(-110*time.Second), gf.U("user-102"))(c.Info)

				// user-103 votes is a noop, though weird, yet still must be removed.
				ultraQuick(3, clock.Now(ctx).Add(-100*time.Second), gf.U("user-104"))(c.Info)

				// user-104 votes is 0, and doesn't need a reset.
				ultraQuick(0, clock.Now(ctx).Add(-90*time.Second), gf.U("user-104"))(c.Info)
			})
			err := Reset(ctx, input)
			So(err, ShouldBeNil)

			resultCI := ct.GFake.GetChange(gHost, int(ci.GetNumber()))
			So(gf.NonZeroVotes(resultCI.Info, CQLabelName), ShouldBeEmpty)
			So(gf.NonZeroVotes(resultCI.Info, qLabel), ShouldBeEmpty)
			So(gf.NonZeroVotes(resultCI.Info, uLabel), ShouldBeEmpty)

			onBehalfs, _ := splitSetReviewRequests()
			// The last request must be for account 100.
			So(onBehalfs[len(onBehalfs)-1].GetOnBehalfOf(), ShouldEqual, 100)
			So(onBehalfs[len(onBehalfs)-1].GetLabels(), ShouldResemble, map[string]int32{
				CQLabelName: 0,
				qLabel:      0,
				uLabel:      0,
			})
		})

		Convey("Skips zero votes", func() {
			input.Triggers.CqVoteTrigger = cqTrigger
			ct.GFake.MutateChange(gHost, int(ci.GetNumber()), func(c *gf.Change) {
				gf.CQ(0, clock.Now(ctx).Add(-90*time.Second), gf.U("user-101"))(c.Info)
				gf.CQ(0, clock.Now(ctx).Add(-100*time.Second), gf.U("user-102"))(c.Info)
				gf.CQ(0, clock.Now(ctx).Add(-110*time.Second), gf.U("user-103"))(c.Info)
			})

			err := Reset(ctx, input)
			So(err, ShouldBeNil)
			resultCI := ct.GFake.GetChange(gHost, int(ci.GetNumber()))
			So(resultCI.Info.GetMessages(), ShouldHaveLength, 1)
			So(resultCI.Info.GetMessages()[0].GetMessage(), ShouldEqual, input.Message)
			So(gf.NonZeroVotes(resultCI.Info, CQLabelName), ShouldBeEmpty)
			onBehalfs, _ := splitSetReviewRequests()
			So(onBehalfs, ShouldHaveLength, 1)
			So(onBehalfs[0].GetOnBehalfOf(), ShouldEqual, triggererID)
		})

		Convey("Post Message even if triggering votes has been removed already", func() {
			input.Triggers.CqVoteTrigger = cqTrigger
			ct.GFake.MutateChange(gHost, int(ci.GetNumber()), func(c *gf.Change) {
				gf.CQ(0, clock.Now(ctx), triggerer)(c.Info)
			})
			err := Reset(ctx, input)
			So(err, ShouldBeNil)
			resultCI := ct.GFake.GetChange(gHost, int(ci.GetNumber()))
			So(resultCI.Info.GetMessages(), ShouldHaveLength, 1)
			So(resultCI.Info.GetMessages()[0].GetMessage(), ShouldEqual, input.Message)
		})

		Convey("Post Message if CV has no permission to vote", func() {
			input.Triggers.CqVoteTrigger = cqTrigger
			ct.GFake.MutateChange(gHost, int(ci.GetNumber()), func(c *gf.Change) {
				c.ACLs = gf.ACLGrant(gf.OpRead, codes.PermissionDenied, lProject).Or(
					// Needed to post comments
					gf.ACLGrant(gf.OpReview, codes.PermissionDenied, lProject),
				)
			})
			So(Reset(ctx, input), ShouldBeNil)
			resultCI := ct.GFake.GetChange(gHost, int(ci.GetNumber())).Info
			// CQ+2 vote remains.
			So(gf.NonZeroVotes(resultCI, CQLabelName), ShouldResembleProto, []*gerritpb.ApprovalInfo{
				{
					User:  triggerer,
					Value: 2,
					Date:  timestamppb.New(triggerTime),
				},
			})
			// But CL is no longer triggered.
			So(findTriggers(resultCI).GetCqVoteTrigger(), ShouldBeNil)
			// Still, user should know what happened.
			expectedMsg := input.Message + `

CV failed to unset the Commit-Queue label on your behalf. Please unvote and revote on the Commit-Queue label to retry.

Bot data: {"action":"cancel","triggered_at":"2020-02-02T10:28:00Z","revision":"rev-010001-002"}`
			So(resultCI.GetMessages()[0].GetMessage(), ShouldEqual, expectedMsg)
		})

		Convey("Post Message if change is in bad state", func() {
			input.Triggers.CqVoteTrigger = cqTrigger
			ct.GFake.MutateChange(gHost, int(ci.GetNumber()), func(c *gf.Change) {
				gf.Status(gerritpb.ChangeStatus_ABANDONED)(c.Info)
				c.ACLs = func(op gf.Operation, _ string) *status.Status {
					if op == gf.OpAlterVotesOfOthers {
						return status.New(codes.FailedPrecondition, "change abandoned, no vote removals allowed")
					}
					return status.New(codes.OK, "")
				}
			})
			err := Reset(ctx, input)
			So(err, ShouldBeNil)
			resultCI := ct.GFake.GetChange(gHost, int(ci.GetNumber())).Info
			// CQ+2 vote remains.
			So(gf.NonZeroVotes(resultCI, CQLabelName), ShouldResembleProto, []*gerritpb.ApprovalInfo{
				{
					User:  triggerer,
					Value: 2,
					Date:  timestamppb.New(triggerTime),
				},
			})
			// But CL is no longer triggered.
			So(findTriggers(resultCI).GetCqVoteTrigger(), ShouldBeNil)
			// Still, user should know what happened.
			So(resultCI.GetMessages(), ShouldHaveLength, 1)
			So(resultCI.GetMessages()[0].GetMessage(), ShouldContainSubstring, "CV failed to unset the Commit-Queue label on your behalf")
		})

		Convey("Post Message also fails", func() {
			input.Triggers.CqVoteTrigger = cqTrigger
			ct.GFake.MutateChange(gHost, int(ci.GetNumber()), func(c *gf.Change) {
				c.ACLs = gf.ACLGrant(gf.OpRead, codes.PermissionDenied, lProject)
			})
			err := Reset(ctx, input)
			So(err, ShouldErrLike, "no permission to remove vote x-review.example.com/10001")
			So(ErrResetPermanentTag.In(err), ShouldBeTrue)
			resultCI := ct.GFake.GetChange(gHost, int(ci.GetNumber())).Info
			So(gf.NonZeroVotes(resultCI, CQLabelName), ShouldResembleProto, []*gerritpb.ApprovalInfo{
				{
					User:  triggerer,
					Value: 2,
					Date:  timestamppb.New(triggerTime),
				},
			})
			So(resultCI.GetMessages(), ShouldBeEmpty)
		})
	})
}
