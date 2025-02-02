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

package buganizer

import (
	"context"
	"fmt"

	"go.chromium.org/luci/common/clock"
	"go.chromium.org/luci/common/errors"
	"go.chromium.org/luci/third_party/google.golang.org/genproto/googleapis/devtools/issuetracker/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// issueData represents all data that the store keeps for an issue.
type IssueData struct {
	// The issue itself.
	Issue *issuetracker.Issue
	// All comments for the issue.
	Comments []*issuetracker.IssueComment
	// The updates on the issue.
	IssueUpdates []*issuetracker.IssueUpdate
	// The list of issue relationships this issue has.
	// Use this field to set source and destination relationsships
	// of duplicate issues.
	IssueRelationships []*issuetracker.IssueRelationship
	// Determines whether the issue should fail update requests.
	// Use this flag to validate behaviours of failed updates.
	ShouldFailUpdates bool
	// Determines whether the issue should return grpc permission
	// error when accessed or updated.
	ShouldReturnAccessPermissionError bool
}

// fakeIssueStore is an in-memory store for issues.
// The store doesn't generate the corresponding
// IssueUpdate for an issue update.
type FakeIssueStore struct {
	// A map of issue id to issue data. Used as an in-memory store.
	Issues map[int64]*IssueData
	// The state of ids, this is incremented for every issue that is created.
	lastID int64
}

func NewFakeIssue(issueId int64) *issuetracker.Issue {
	return &issuetracker.Issue{
		IssueId: issueId,
		IssueState: &issuetracker.IssueState{
			ComponentId: 1,
			Type:        issuetracker.Issue_BUG,
			Status:      issuetracker.Issue_ACCEPTED,
			Priority:    issuetracker.Issue_P2,
			Severity:    issuetracker.Issue_S0,
			Title:       "new bug",
		},
		IssueComment: &issuetracker.IssueComment{
			Comment: "new bug",
		},
	}
}

// Creates a new in-memory fake issue store.
func NewFakeIssueStore() *FakeIssueStore {
	return &FakeIssueStore{
		Issues: make(map[int64]*IssueData),
	}
}

// StoreIssue stores an issue in the in-memory store.
// Ids are created incrementally from 1.
// If the issue already has an id that is greater than 0, the id will not change.
func (fis *FakeIssueStore) StoreIssue(ctx context.Context, issue *issuetracker.Issue) *issuetracker.Issue {
	_, ok := fis.Issues[issue.IssueId]
	if ok {
		return issue
	} else {
		if issue.IssueId == 0 {
			fis.lastID++
			id := fis.lastID
			issue.IssueId = id
			issue.IssueComment.IssueId = id
			issue.Description = &issuetracker.IssueComment{
				CommentNumber: 1,
				Comment:       issue.IssueComment.Comment,
			}
			issue.CreatedTime = timestamppb.New(clock.Now(ctx))
		} else {
			if issue.IssueId > fis.lastID {
				fis.lastID = issue.IssueId
			}
		}
		issue.ModifiedTime = timestamppb.New(clock.Now(ctx))
		comments := make([]*issuetracker.IssueComment, 0)
		comments = append(comments, &issuetracker.IssueComment{
			CommentNumber: 1,
			Comment:       issue.IssueComment.Comment,
		})
		fis.Issues[issue.IssueId] = &IssueData{
			Issue:        issue,
			Comments:     comments,
			IssueUpdates: make([]*issuetracker.IssueUpdate, 0),
		}
		return issue
	}
}

func (fis *FakeIssueStore) BatchGetIssues(issueIds []int64) ([]*issuetracker.Issue, error) {
	issues := make([]*issuetracker.Issue, 0)
	for _, id := range issueIds {
		issueData, ok := fis.Issues[id]
		if ok {
			issues = append(issues, issueData.Issue)
		}
	}
	return issues, nil
}

func (fis *FakeIssueStore) GetIssue(id int64) (*IssueData, error) {
	issueData, ok := fis.Issues[id]
	if !ok {
		return nil, errors.New(fmt.Sprintf("Issue does not exist: %d", id))
	}
	return issueData, nil
}

func (fis *FakeIssueStore) ListIssueUpdates(id int64) ([]*issuetracker.IssueUpdate, error) {
	issueData, ok := fis.Issues[id]
	if !ok {
		return nil, errors.New(fmt.Sprintf("Issue does not exist: %d", id))
	}
	return issueData.IssueUpdates, nil
}
