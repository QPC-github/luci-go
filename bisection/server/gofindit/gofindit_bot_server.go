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

package gofindit

import (
	"context"
	"fmt"

	"go.chromium.org/luci/bisection/compilefailureanalysis/nthsection"
	"go.chromium.org/luci/bisection/compilefailureanalysis/statusupdater"
	"go.chromium.org/luci/bisection/culpritverification"
	"go.chromium.org/luci/bisection/model"
	gfipb "go.chromium.org/luci/bisection/proto"
	pb "go.chromium.org/luci/bisection/proto/v1"
	taskpb "go.chromium.org/luci/bisection/task/proto"
	"go.chromium.org/luci/bisection/util/datastoreutil"
	"go.chromium.org/luci/bisection/util/loggingutil"

	bbpb "go.chromium.org/luci/buildbucket/proto"
	"go.chromium.org/luci/common/clock"
	"go.chromium.org/luci/common/errors"
	"go.chromium.org/luci/common/logging"
	"go.chromium.org/luci/gae/service/datastore"
	"go.chromium.org/luci/server/tq"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GoFinditBotServer implements the proto service GoFinditBotService.
type GoFinditBotServer struct{}

// UpdateAnalysisProgress is an RPC endpoints used by the recipes to update
// analysis progress.
func (server *GoFinditBotServer) UpdateAnalysisProgress(c context.Context, req *gfipb.UpdateAnalysisProgressRequest) (*gfipb.UpdateAnalysisProgressResponse, error) {
	err := verifyUpdateAnalysisProgressRequest(c, req)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "Invalid request: %s", err)
	}
	c = loggingutil.SetAnalysisID(c, req.AnalysisId)
	c = loggingutil.SetRerunBBID(c, req.Bbid)

	logging.Infof(c, "Update analysis with rerun_build_id = %d analysis_id = %d gitiles_commit=%v ", req.Bbid, req.AnalysisId, req.GitilesCommit)

	cfa, err := datastoreutil.GetCompileFailureAnalysis(c, req.AnalysisId)
	if err != nil {
		err = errors.Annotate(err, "failed GetCompileFailureAnalysis ID: %d", req.AnalysisId).Err()
		errors.Log(c, err)
		return nil, status.Errorf(codes.Internal, "error GetCompileFailureAnalysis")
	}
	if cfa.CompileFailure != nil && cfa.CompileFailure.Parent() != nil {
		c = loggingutil.SetAnalyzedBBID(c, cfa.CompileFailure.Parent().IntID())
	}

	// Get rerun model
	rerunModel := &model.CompileRerunBuild{
		Id: req.Bbid,
	}
	switch err := datastore.Get(c, rerunModel); {
	case err == datastore.ErrNoSuchEntity:
		return nil, status.Errorf(codes.NotFound, "could not find rerun build with id %d", req.Bbid)
	case err != nil:
		return nil, status.Errorf(codes.Internal, "error finding rerun build")
	default:
		//continue
	}

	lastRerun, err := datastoreutil.GetLastRerunForRerunBuild(c, rerunModel)
	if err != nil {
		err = errors.Annotate(err, "failed getting last rerun for build %d. Analysis ID: %d", rerunModel.Id, req.AnalysisId).Err()
		errors.Log(c, err)
		return nil, status.Errorf(codes.Internal, "error getting last rerun build")
	}

	// Update rerun model
	err = updateRerun(c, req, lastRerun)
	if err != nil {
		err = errors.Annotate(err, "failed updating rerun for build %d. Analysis ID: %d", rerunModel.Id, req.AnalysisId).Err()
		errors.Log(c, err)
		return nil, status.Errorf(codes.Internal, "error updating rerun build")
	}

	// Safeguard, we really don't expect any other type
	if lastRerun.Type != model.RerunBuildType_CulpritVerification && lastRerun.Type != model.RerunBuildType_NthSection {
		logging.Errorf(c, "Invalid type %v for analysis %d", lastRerun.Type, req.AnalysisId)
		return nil, status.Errorf(codes.Internal, "Invalid type %v", lastRerun.Type)
	}

	// Culprit verification
	if lastRerun.Type == model.RerunBuildType_CulpritVerification {
		err := updateSuspectWithRerunData(c, lastRerun)
		if err != nil {
			err = errors.Annotate(err, "updateSuspectWithRerunData for build id %d. Analysis ID: %d", rerunModel.Id, req.AnalysisId).Err()
			errors.Log(c, err)
			return nil, status.Errorf(codes.Internal, "error updating suspect")
		}

		// Update analysis status
		err = statusupdater.UpdateAnalysisStatus(c, cfa)
		if err != nil {
			err = errors.Annotate(err, "statusupdater.UpdateAnalysisStatus. Analysis ID: %d", req.AnalysisId).Err()
			errors.Log(c, err)
			return nil, status.Errorf(codes.Internal, "error UpdateAnalysisStatus")
		}

		// TODO (nqmtuan): It is possible that we schedule an nth-section run right after
		// a culprit verification run within the same build. We will do this later, for
		// safety, after we verify nth-section analysis is running fine.
		return &gfipb.UpdateAnalysisProgressResponse{}, nil
	}

	// Nth section
	if lastRerun.Type == model.RerunBuildType_NthSection {
		nsa, err := processNthSectionUpdate(c, req)
		if err != nil {
			err = errors.Annotate(err, "processNthSectionUpdate. Analysis ID: %d", req.AnalysisId).Err()
			logging.Errorf(c, err.Error())

			// If there is an error, then nthsection analysis may ended
			// if there is no unfinised nthsection runs
			e := setNthSectionError(c, nsa)
			if e != nil {
				e = errors.Annotate(e, "setNthSectionError. Analysis ID: %d", req.AnalysisId).Err()
				logging.Errorf(c, e.Error())
			}

			// Also the main analysis status may need to change as well
			e = statusupdater.UpdateAnalysisStatus(c, cfa)
			if e != nil {
				e = errors.Annotate(e, "UpdateAnalysisStatus. Analysis ID: %d", req.AnalysisId).Err()
				logging.Errorf(c, e.Error())
			}
			return nil, status.Errorf(codes.Internal, err.Error())
		}

		// Update analysis status
		err = statusupdater.UpdateAnalysisStatus(c, cfa)
		if err != nil {
			err = errors.Annotate(err, "statusupdater.UpdateAnalysisStatus. Analysis ID: %d", req.AnalysisId).Err()
			errors.Log(c, err)
			return nil, status.Errorf(codes.Internal, "error UpdateAnalysisStatus")
		}

		return &gfipb.UpdateAnalysisProgressResponse{}, nil
	}

	return nil, status.Errorf(codes.Internal, "unknown error")
}

func setNthSectionError(c context.Context, nsa *model.CompileNthSectionAnalysis) error {
	if nsa == nil {
		return nil
	}
	reruns, err := datastoreutil.GetRerunsForNthSectionAnalysis(c, nsa)
	if err != nil {
		return errors.Annotate(err, "GetRerunsForNthSectionAnalysis").Err()
	}

	for _, rerun := range reruns {
		// There are some rerun running, so do not mark this as error yet
		if rerun.Status == pb.RerunStatus_RERUN_STATUS_IN_PROGRESS {
			return nil
		}
	}

	return datastore.RunInTransaction(c, func(c context.Context) error {
		e := datastore.Get(c, nsa)
		if e != nil {
			return e
		}
		nsa.Status = pb.AnalysisStatus_ERROR
		nsa.RunStatus = pb.AnalysisRunStatus_ENDED
		nsa.EndTime = clock.Now(c)
		return datastore.Put(c, nsa)
	}, nil)
}

// processNthSectionUpdate processes the bot update for nthsection analysis run
// It will schedule the next run for nthsection analysis targeting the same bot
func processNthSectionUpdate(c context.Context, req *gfipb.UpdateAnalysisProgressRequest) (*model.CompileNthSectionAnalysis, error) {
	cfa, err := datastoreutil.GetCompileFailureAnalysis(c, req.AnalysisId)
	if err != nil {
		return nil, err
	}

	// We should not schedule any more run for this analysis
	if cfa.ShouldCancel {
		return nil, nil
	}

	nsa, err := datastoreutil.GetNthSectionAnalysis(c, cfa)
	if err != nil {
		return nil, err
	}

	// There is no nthsection analysis for this analysis
	if nsa == nil {
		return nil, nil
	}

	snapshot, err := nthsection.CreateSnapshot(c, nsa)
	if err != nil {
		return nsa, errors.Annotate(err, "couldn't create snapshot").Err()
	}

	// Check if we already found the culprit or not
	ok, cul := snapshot.GetCulprit()

	// Found culprit -> Update the nthsection analysis
	if ok {
		suspect, err := storeNthSectionResultToDatastore(c, nsa, snapshot.BlameList.Commits[cul], req)
		if err != nil {
			return nsa, errors.Annotate(err, "storeNthSectionResultToDatastore").Err()
		}

		// Run culprit verification
		shouldRunCulpritVerification, err := culpritverification.ShouldRunCulpritVerification(c)
		if err != nil {
			return nsa, errors.Annotate(err, "couldn't fetch shouldRunCulpritVerification config").Err()
		}
		if shouldRunCulpritVerification {
			// We run culprit verification in a task queue here because this is inside the
			// updateBotStatus endpoint. The culprit verification process may take some time,
			// because it needs to schedule build, and we don't want it to block.
			suspectID := nsa.Suspect.IntID()
			err = tq.AddTask(c, &tq.Task{
				Title: fmt.Sprintf("culprit_verification_%d_%d", req.AnalysisId, suspectID),
				Payload: &taskpb.CulpritVerificationTask{
					SuspectId:  suspectID,
					AnalysisId: req.AnalysisId,
					ParentKey:  nsa.Suspect.Parent().Encode(),
				},
			})
			if err != nil {
				// Non-critical, just log the error
				err := errors.Annotate(err, "schedule culprit verification task %d_%d", req.AnalysisId, suspectID).Err()
				logging.Errorf(c, err.Error())
			}
			// Update suspect verification status
			err = datastore.RunInTransaction(c, func(c context.Context) error {
				e := datastore.Get(c, suspect)
				if e != nil {
					return e
				}
				suspect.VerificationStatus = model.SuspectVerificationStatus_VerificationScheduled
				return datastore.Put(c, suspect)
			}, nil)
			if err != nil {
				// Non-critical, just log the error
				err := errors.Annotate(err, "saving suspect").Err()
				logging.Errorf(c, err.Error())
			}
		}
		return nsa, nil
	}

	shouldRunNthSection, err := nthsection.ShouldRunNthSectionAnalysis(c)
	if err != nil {
		return nsa, errors.Annotate(err, "couldn't fetch config for nthsection").Err()
	}
	if !shouldRunNthSection {
		return nsa, nil
	}

	shouldRun, commit, err := findNextNthSectionCommitToRun(c, snapshot)
	if err != nil {
		// Perhaps not found here?
		return nsa, errors.Annotate(err, "findNextNthSectionCommitToRun").Err()
	}
	if !shouldRun {
		// We don't have more run to wait -> we've failed to find the suspect
		if snapshot.NumInProgress == 0 {
			return nsa, updateNthSectionModelNotFound(c, nsa)
		}
		return nsa, nil
	}

	// We got the next commit to run. We will schedule a rerun targetting the same bot
	gitilesCommit := &bbpb.GitilesCommit{
		Host:    req.GitilesCommit.Host,
		Project: req.GitilesCommit.Project,
		Ref:     req.GitilesCommit.Ref,
		Id:      commit,
	}
	dims := map[string]string{
		"id": req.BotId,
	}
	err = nthsection.RerunCommit(c, nsa, gitilesCommit, cfa.FirstFailedBuildId, dims)
	if err != nil {
		return nsa, errors.Annotate(err, "rerun commit for %s", commit).Err()
	}
	return nsa, nil
}

func updateNthSectionModelNotFound(c context.Context, nsa *model.CompileNthSectionAnalysis) error {
	err := datastore.RunInTransaction(c, func(c context.Context) error {
		e := datastore.Get(c, nsa)
		if e != nil {
			return e
		}
		nsa.EndTime = clock.Now(c)
		nsa.Status = pb.AnalysisStatus_NOTFOUND
		nsa.RunStatus = pb.AnalysisRunStatus_ENDED
		return datastore.Put(c, nsa)
	}, nil)
	if err != nil {
		return errors.Annotate(err, "failed updating nthsectionModel").Err()
	}
	return nil
}

func storeNthSectionResultToDatastore(c context.Context, nsa *model.CompileNthSectionAnalysis, blCommit *pb.BlameListSingleCommit, req *gfipb.UpdateAnalysisProgressRequest) (*model.Suspect, error) {
	suspect := &model.Suspect{
		Type: model.SuspectType_NthSection,
		GitilesCommit: bbpb.GitilesCommit{
			Host:    req.GitilesCommit.Host,
			Project: req.GitilesCommit.Project,
			Ref:     req.GitilesCommit.Ref,
			Id:      blCommit.Commit,
		},
		ParentAnalysis:     datastore.KeyForObj(c, nsa),
		VerificationStatus: model.SuspectVerificationStatus_Unverified,
		ReviewUrl:          blCommit.ReviewUrl,
		ReviewTitle:        blCommit.ReviewTitle,
	}
	err := datastore.Put(c, suspect)
	if err != nil {
		return nil, errors.Annotate(err, "couldn't save suspect").Err()
	}

	err = datastore.RunInTransaction(c, func(ctx context.Context) error {
		e := datastore.Get(c, nsa)
		if e != nil {
			return e
		}
		nsa.Status = pb.AnalysisStatus_SUSPECTFOUND
		nsa.Suspect = datastore.KeyForObj(c, suspect)
		nsa.RunStatus = pb.AnalysisRunStatus_ENDED
		nsa.EndTime = clock.Now(c)
		return datastore.Put(c, nsa)
	}, nil)

	if err != nil {
		return nil, errors.Annotate(err, "couldn't save nthsection analysis").Err()
	}
	return suspect, nil
}

// findNextNthSectionCommitToRun return true (and the commit) if it can find a nthsection commit to run next
func findNextNthSectionCommitToRun(c context.Context, snapshot *nthsection.NthSectionSnapshot) (bool, string, error) {
	// We pass 1 as argument here because at this moment, we only have 1 "slot" left for nth section
	commits, err := snapshot.FindNextCommitsToRun(1)
	if err != nil {
		return false, "", errors.Annotate(err, "couldn't find next commits to run").Err()
	}
	// There is no commit to run, perhaps we already found a culprit, or we
	// have already scheduled the necessary build to be run.
	if len(commits) == 0 {
		return false, "", nil
	}
	if len(commits) != 1 {
		return false, "", errors.Annotate(err, "expect only 1 commits to rerun. Got %d", len(commits)).Err()
	}
	return true, commits[0], nil
}

func updateSuspectWithRerunData(c context.Context, rerun *model.SingleRerun) error {
	// Get the suspect for the rerun build
	if rerun.Suspect == nil {
		return fmt.Errorf("no suspect for rerun %d", rerun.Id)
	}

	suspect := &model.Suspect{
		Id:             rerun.Suspect.IntID(),
		ParentAnalysis: rerun.Suspect.Parent(),
	}
	err := datastore.Get(c, suspect)
	if err != nil {
		return errors.Annotate(err, "couldn't find suspect for rerun %d", rerun.Id).Err()
	}

	err = updateSuspect(c, suspect)
	if err != nil {
		return errors.Annotate(err, "error updating suspect for rerun %d", rerun.Id).Err()
	}

	if suspect.VerificationStatus == model.SuspectVerificationStatus_ConfirmedCulprit {
		err = updateSuspectAsConfirmedCulprit(c, suspect)
		if err != nil {
			return errors.Annotate(err, "error updateSuspectAsConfirmedCulprit for rerun %d", rerun.Id).Err()
		}

		// Cancel all remaining runs
		analysisID := suspect.ParentAnalysis.Parent().IntID()
		err = tq.AddTask(c, &tq.Task{
			Title: fmt.Sprintf("cancel_analysis_%d", analysisID),
			Payload: &taskpb.CancelAnalysisTask{
				AnalysisId: analysisID,
			},
		})
		if err != nil {
			// Non-critical, just log the error
			err := errors.Annotate(err, "schedule canceling analysis %d", analysisID).Err()
			logging.Errorf(c, err.Error())
		}

		// Add task to revert the heuristic confirmed culprit
		err = tq.AddTask(c, &tq.Task{
			Title: fmt.Sprintf("revert_culprit_%d_%d", suspect.Id, analysisID),
			Payload: &taskpb.RevertCulpritTask{
				AnalysisId: analysisID,
				CulpritId:  suspect.Id,
			},
		})
		if err != nil {
			return errors.Annotate(err,
				"error creating task in task queue to revert culprit (analysis ID=%d, suspect ID=%d)",
				analysisID, suspect.Id).Err()
		}
	}
	return nil
}

func verifyUpdateAnalysisProgressRequest(c context.Context, req *gfipb.UpdateAnalysisProgressRequest) error {
	if req.AnalysisId == 0 {
		return fmt.Errorf("analysis_id is required")
	}
	if req.Bbid == 0 {
		return fmt.Errorf("build bucket id is required")
	}
	if req.GitilesCommit == nil {
		return fmt.Errorf("gitiles commit is required")
	}
	if req.RerunResult == nil {
		return fmt.Errorf("rerun result is required")
	}
	if req.BotId == "" {
		return fmt.Errorf("bot_id is required")
	}
	return nil
}

// updateSuspect looks at rerun and set the suspect status
func updateSuspect(c context.Context, suspect *model.Suspect) error {
	rerunStatus, err := getSingleRerunStatus(c, suspect.SuspectRerunBuild.IntID())
	if err != nil {
		return err
	}
	parentRerunStatus, err := getSingleRerunStatus(c, suspect.ParentRerunBuild.IntID())
	if err != nil {
		return err
	}

	// Update suspect based on rerunStatus and parentRerunStatus
	suspectStatus := getSuspectStatus(c, rerunStatus, parentRerunStatus)

	return datastore.RunInTransaction(c, func(ctx context.Context) error {
		e := datastore.Get(c, suspect)
		if e != nil {
			return e
		}
		suspect.VerificationStatus = suspectStatus
		return datastore.Put(c, suspect)
	}, nil)
}

// updateSuspectAsConfirmedCulprit update the suspect as the confirmed culprit of analysis
func updateSuspectAsConfirmedCulprit(c context.Context, suspect *model.Suspect) error {
	analysisKey := suspect.ParentAnalysis.Parent()
	analysis := &model.CompileFailureAnalysis{
		Id: analysisKey.IntID(),
	}
	err := datastore.Get(c, analysis)
	if err != nil {
		return err
	}
	verifiedCulprits := analysis.VerifiedCulprits
	verifiedCulprits = append(verifiedCulprits, datastore.KeyForObj(c, suspect))
	if len(verifiedCulprits) > 1 {
		// Just log the warning here, as it is a rare case
		logging.Warningf(c, "found more than 2 suspects for analysis %d", analysis.Id)
	}

	err = datastore.RunInTransaction(c, func(ctx context.Context) error {
		e := datastore.Get(c, analysis)
		if e != nil {
			return e
		}
		analysis.VerifiedCulprits = verifiedCulprits
		return datastore.Put(c, analysis)
	}, nil)
	if err != nil {
		return err
	}
	return statusupdater.UpdateAnalysisStatus(c, analysis)
}

func getSuspectStatus(c context.Context, rerunStatus pb.RerunStatus, parentRerunStatus pb.RerunStatus) model.SuspectVerificationStatus {
	if rerunStatus == pb.RerunStatus_RERUN_STATUS_FAILED && parentRerunStatus == pb.RerunStatus_RERUN_STATUS_PASSED {
		return model.SuspectVerificationStatus_ConfirmedCulprit
	}
	if rerunStatus == pb.RerunStatus_RERUN_STATUS_PASSED || parentRerunStatus == pb.RerunStatus_RERUN_STATUS_FAILED {
		return model.SuspectVerificationStatus_Vindicated
	}
	if rerunStatus == pb.RerunStatus_RERUN_STATUS_INFRA_FAILED || parentRerunStatus == pb.RerunStatus_RERUN_STATUS_INFRA_FAILED {
		return model.SuspectVerificationStatus_VerificationError
	}
	if rerunStatus == pb.RerunStatus_RERUN_STATUS_UNSPECIFIED || parentRerunStatus == pb.RerunStatus_RERUN_STATUS_UNSPECIFIED {
		return model.SuspectVerificationStatus_Unverified
	}
	return model.SuspectVerificationStatus_UnderVerification
}

// updateRerun updates the last SingleRerun for rerunModel with the information from req.
// Returns the last SingleRerun and error (if it occur).
func updateRerun(c context.Context, req *gfipb.UpdateAnalysisProgressRequest, rerun *model.SingleRerun) error {
	// Verify the gitiles commit, making sure it was the right rerun we are updating
	if !sameGitilesCommit(req.GitilesCommit, &rerun.GitilesCommit) {
		logging.Errorf(c, "Got different Gitles commit for rerun build %d", req.Bbid)
		return fmt.Errorf("different gitiles commit for rerun")
	}

	err := datastore.RunInTransaction(c, func(ctx context.Context) error {
		e := datastore.Get(c, rerun)
		if e != nil {
			return e
		}
		rerun.EndTime = clock.Now(c)
		rerun.Status = pb.RerunStatus(req.RerunResult.RerunStatus)
		return datastore.Put(c, rerun)
	}, nil)

	if err != nil {
		logging.Errorf(c, "Error updating SingleRerun for build %d: %s", req.Bbid, rerun)
		return errors.Annotate(err, "saving SingleRerun").Err()
	}
	return nil
}

func getSingleRerunStatus(c context.Context, rerunId int64) (pb.RerunStatus, error) {
	rerunBuild := &model.CompileRerunBuild{
		Id: rerunId,
	}
	err := datastore.Get(c, rerunBuild)
	if err != nil {
		return pb.RerunStatus_RERUN_STATUS_UNSPECIFIED, err
	}

	// Get SingleRerun
	singleRerun, err := datastoreutil.GetLastRerunForRerunBuild(c, rerunBuild)
	if err != nil {
		return pb.RerunStatus_RERUN_STATUS_UNSPECIFIED, err
	}

	return singleRerun.Status, nil
}

func sameGitilesCommit(g1 *bbpb.GitilesCommit, g2 *bbpb.GitilesCommit) bool {
	return g1.Host == g2.Host && g1.Project == g2.Project && g1.Id == g2.Id && g1.Ref == g2.Ref
}
