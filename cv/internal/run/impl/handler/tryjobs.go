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

package handler

import (
	"context"
	"fmt"
	"sort"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"go.chromium.org/luci/common/clock"
	"go.chromium.org/luci/common/errors"
	"go.chromium.org/luci/common/logging"

	"go.chromium.org/luci/cv/internal/common"
	"go.chromium.org/luci/cv/internal/run"
	"go.chromium.org/luci/cv/internal/run/eventpb"
	"go.chromium.org/luci/cv/internal/run/impl/state"
	"go.chromium.org/luci/cv/internal/tryjob"
)

const (
	// maxTryjobExecutorDuration is the max time that the tryjob executor
	// can process.
	maxTryjobExecutorDuration = 8 * time.Minute
)

// OnTryjobsUpdated implements Handler interface.
func (impl *Impl) OnTryjobsUpdated(ctx context.Context, rs *state.RunState, tryjobs common.TryjobIDs) (*Result, error) {
	switch status := rs.Status; {
	case run.IsEnded(status):
		fallthrough
	case status == run.Status_WAITING_FOR_SUBMISSION || status == run.Status_SUBMITTING:
		logging.Debugf(ctx, "Ignoring Tryjobs event because Run is in status %s", status)
		return &Result{State: rs}, nil
	case status != run.Status_RUNNING:
		return nil, errors.Reason("expected RUNNING status, got %s", status).Err()
	case hasExecuteTryjobLongOp(rs):
		// Process this event after the current tryjob executor finishes running.
		return &Result{State: rs, PreserveEvents: true}, nil
	default:
		tryjobs.Dedupe()
		sort.Sort(tryjobs)
		rs = rs.ShallowCopy()
		enqueueTryjobsUpdatedTask(ctx, rs, tryjobs)
		return &Result{State: rs}, nil
	}
}

func (impl *Impl) onCompletedExecuteTryjobs(ctx context.Context, rs *state.RunState, op *run.OngoingLongOps_Op, opCompleted *eventpb.LongOpCompleted) (*Result, error) {
	opID := opCompleted.GetOperationId()
	rs = rs.ShallowCopy()
	rs.RemoveCompletedLongOp(opID)
	if rs.Status != run.Status_RUNNING {
		logging.Warningf(ctx, "long operation to execute Tryjobs has completed but Run is %s.", rs.Status)
		return &Result{State: rs}, nil
	}
	var (
		runStatus       run.Status
		msg             string
		attentionReason string
	)
	switch opCompleted.GetStatus() {
	case eventpb.LongOpCompleted_EXPIRED:
		// Tryjob executor timeout.
		fallthrough
	case eventpb.LongOpCompleted_FAILED:
		// normally indicates tryjob executor itself encounters error (e.g. failed
		// to read from datastore).
		runStatus = run.Status_FAILED
		msg = "Unexpected error when processing Tryjobs. Please retry. If retry continues to fail, please contact LUCI team.\n\n" + cvBugLink
		attentionReason = "Run failed"
	case eventpb.LongOpCompleted_SUCCEEDED:
		switch es, _, err := tryjob.LoadExecutionState(ctx, rs.ID); {
		case err != nil:
			return nil, err
		case es == nil:
			panic(fmt.Errorf("impossible; Execute Tryjobs task succeeded but ExecutionState was missing"))
		default:
			if rs.Tryjobs == nil {
				rs.Tryjobs = &run.Tryjobs{}
			} else {
				rs.Tryjobs = proto.Clone(rs.Tryjobs).(*run.Tryjobs)
			}
			rs.Tryjobs.State = es // Copy the execution state to Run entity
			switch executionStatus := es.GetStatus(); {
			case executionStatus == tryjob.ExecutionState_SUCCEEDED && rs.Mode == run.FullRun:
				rs.Status = run.Status_WAITING_FOR_SUBMISSION
				return impl.OnReadyForSubmission(ctx, rs)
			case executionStatus == tryjob.ExecutionState_SUCCEEDED:
				runStatus = run.Status_SUCCEEDED
				switch rs.Mode {
				case run.DryRun, run.FullRun, run.QuickDryRun:
					msg = "This CL has passed the run"
					attentionReason = "Run succeeded"
				case run.NewPatchsetRun:
				default:
					panic(fmt.Errorf("unsupported mode %s", rs.Mode))
				}
			case executionStatus == tryjob.ExecutionState_FAILED:
				runStatus = run.Status_FAILED
				msg = "This CL has failed the run. Reason:\n\n" + es.FailureReason
				attentionReason = "Tryjobs failed"
			case executionStatus == tryjob.ExecutionState_RUNNING:
				// Tryjobs are still running. No change to run status.
			case executionStatus == tryjob.ExecutionState_STATUS_UNSPECIFIED:
				panic(fmt.Errorf("execution status is not specified"))
			default:
				panic(fmt.Errorf("unknown tryjob execution status %s", executionStatus))
			}
		}
	default:
		panic(fmt.Errorf("unknown LongOpCompleted status: %s", opCompleted.GetStatus()))
	}

	if run.IsEnded(runStatus) {
		var meta reviewInputMeta
		if msg != "" && attentionReason != "" {
			whoms := rs.Mode.GerritNotifyTargets()
			meta = reviewInputMeta{
				message:        msg,
				notify:         whoms,
				addToAttention: whoms,
				reason:         attentionReason,
			}
		}
		metas := make(map[common.CLID]reviewInputMeta, len(rs.CLs))
		for _, cl := range rs.CLs {
			metas[cl] = meta
		}
		scheduleTriggersReset(ctx, rs, metas, runStatus)
	}

	return &Result{
		State: rs,
	}, nil
}

func hasExecuteTryjobLongOp(rs *state.RunState) bool {
	for _, op := range rs.OngoingLongOps.GetOps() {
		if op.GetExecuteTryjobs() != nil {
			return true
		}
	}
	return false
}

func enqueueTryjobsUpdatedTask(ctx context.Context, rs *state.RunState, tryjobs common.TryjobIDs) {
	rs.EnqueueLongOp(&run.OngoingLongOps_Op{
		Deadline: timestamppb.New(clock.Now(ctx).UTC().Add(maxTryjobExecutorDuration)),
		Work: &run.OngoingLongOps_Op_ExecuteTryjobs{
			ExecuteTryjobs: &tryjob.ExecuteTryjobsPayload{
				TryjobsUpdated: tryjobs.ToInt64(),
			},
		},
	})
}

func enqueueRequirementChangedTask(ctx context.Context, rs *state.RunState) {
	rs.EnqueueLongOp(&run.OngoingLongOps_Op{
		Deadline: timestamppb.New(clock.Now(ctx).UTC().Add(maxTryjobExecutorDuration)),
		Work: &run.OngoingLongOps_Op_ExecuteTryjobs{
			ExecuteTryjobs: &tryjob.ExecuteTryjobsPayload{
				RequirementChanged: true,
			},
		},
	})
}
