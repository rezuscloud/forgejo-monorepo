// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	actions_model "forgejo.org/models/actions"
	auth_model "forgejo.org/models/auth"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/setting"

	runnerv1 "code.forgejo.org/forgejo/actions-proto/runner/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// rerunWorkflow builds a single-job workflow used by the rerun API tests.
func rerunWorkflow(name string) string {
	return fmt.Sprintf(`name: %s
on: push
jobs:
  job1:
    runs-on: ubuntu-latest
    steps:
      - run: echo hello
`, name)
}

func rerunOutcome(result runnerv1.Result) *mockTaskOutcome {
	return &mockTaskOutcome{
		result: result,
		logRows: []*runnerv1.LogRow{
			{Time: timestamppb.New(time.Now()), Content: "line"},
		},
	}
}

// rerunSetup creates a repo with the workflow pushed and a mock runner
// registered, executes the first task with the given outcome, and returns
// the completed run (retrieved by index 1, the first run of the repo).
func rerunSetup(t *testing.T, u *url.URL, repoName string, result runnerv1.Result) (*repo_model.Repository, *user_model.User, string, *actions_model.ActionRun) {
	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	session := loginUser(t, user2.Name)
	token := getTokenForLoggedInUser(t, session,
		auth_model.AccessTokenScopeWriteRepository,
		auth_model.AccessTokenScopeWriteUser,
	)

	apiRepo := createActionsTestRepo(t, token, repoName, false)
	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: apiRepo.ID})

	runner := newMockRunner()
	runner.registerAsRepoRunner(t, user2.Name, repo.Name, "mock-runner", []string{"ubuntu-latest"})

	treePath := fmt.Sprintf(".forgejo/workflows/%s.yml", repoName)
	opts := getWorkflowCreateFileOptions(user2, repo.DefaultBranch,
		fmt.Sprintf("create %s", treePath), rerunWorkflow(repoName))
	createWorkflowFile(t, token, user2.Name, repo.Name, treePath, opts)

	task := runner.fetchTask(t)
	runner.execTask(t, task, rerunOutcome(result))

	run, err := actions_model.GetRunByIndex(t.Context(), repo.ID, 1)
	require.NoError(t, err)
	require.NoError(t, run.LoadAttributes(t.Context()))
	return repo, user2, token, run
}

func TestAPIRerunActionRun(t *testing.T) {
	if !setting.Database.Type.IsSQLite3() {
		t.Skip()
	}
	onApplicationRun(t, func(t *testing.T, u *url.URL) {
		repo, user2, token, run := rerunSetup(t, u, "actions-rerun", runnerv1.Result_RESULT_SUCCESS)

		// Rerun the whole (completed) run: 204, and the mock runner receives
		// the re-fired task.
		req := NewRequestf(t, "POST", "/api/v1/repos/%s/%s/actions/runs/%d/rerun", user2.Name, repo.Name, run.ID).
			AddTokenAuth(token)
		MakeRequest(t, req, http.StatusNoContent)

		runner := newMockRunner()
		runner.registerAsRepoRunner(t, user2.Name, repo.Name, "mock-runner-2", []string{"ubuntu-latest"})
		task := runner.fetchTask(t)
		actionTask := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionTask{ID: task.Id})
		require.NoError(t, actionTask.LoadJob(t.Context()))
		assert.Equal(t, "job1", actionTask.Job.Name)
		runner.execTask(t, task, rerunOutcome(runnerv1.Result_RESULT_SUCCESS))

		// Cross-repo guard: the run id does not resolve under another repo.
		other := createActionsTestRepo(t, token, "actions-rerun-other", false)
		req = NewRequestf(t, "POST", "/api/v1/repos/%s/%s/actions/runs/%d/rerun", user2.Name, other.Name, run.ID).
			AddTokenAuth(token)
		MakeRequest(t, req, http.StatusNotFound)
	})
}

func TestAPIRerunActionRunStillRunning(t *testing.T) {
	if !setting.Database.Type.IsSQLite3() {
		t.Skip()
	}
	onApplicationRun(t, func(t *testing.T, u *url.URL) {
		user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
		session := loginUser(t, user2.Name)
		token := getTokenForLoggedInUser(t, session,
			auth_model.AccessTokenScopeWriteRepository,
			auth_model.AccessTokenScopeWriteUser,
		)
		apiRepo := createActionsTestRepo(t, token, "actions-rerun-running", false)
		repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: apiRepo.ID})

		runner := newMockRunner()
		runner.registerAsRepoRunner(t, user2.Name, repo.Name, "mock-runner", []string{"ubuntu-latest"})

		treePath := ".forgejo/workflows/actions-rerun-running.yml"
		opts := getWorkflowCreateFileOptions(user2, repo.DefaultBranch, "create", rerunWorkflow("actions-rerun-running"))
		createWorkflowFile(t, token, user2.Name, repo.Name, treePath, opts)

		// Fetch but do NOT execute: the job is running, the run is not done.
		task := runner.fetchTask(t)

		run, err := actions_model.GetRunByIndex(t.Context(), repo.ID, 1)
		require.NoError(t, err)

		req := NewRequestf(t, "POST", "/api/v1/repos/%s/%s/actions/runs/%d/rerun", user2.Name, repo.Name, run.ID).
			AddTokenAuth(token)
		MakeRequest(t, req, http.StatusConflict)

		runner.execTask(t, task, rerunOutcome(runnerv1.Result_RESULT_SUCCESS))
	})
}

func TestAPIRerunFailedActionRun(t *testing.T) {
	if !setting.Database.Type.IsSQLite3() {
		t.Skip()
	}
	onApplicationRun(t, func(t *testing.T, u *url.URL) {
		repo, user2, token, run := rerunSetup(t, u, "actions-rerun-failed", runnerv1.Result_RESULT_FAILURE)

		// The failed job is re-fired.
		req := NewRequestf(t, "POST", "/api/v1/repos/%s/%s/actions/runs/%d/rerun-failed", user2.Name, repo.Name, run.ID).
			AddTokenAuth(token)
		MakeRequest(t, req, http.StatusNoContent)

		runner := newMockRunner()
		runner.registerAsRepoRunner(t, user2.Name, repo.Name, "mock-runner-2", []string{"ubuntu-latest"})
		task := runner.fetchTask(t)
		runner.execTask(t, task, rerunOutcome(runnerv1.Result_RESULT_SUCCESS))

		// No failed jobs anymore: 409 rather than a silent no-op.
		req = NewRequestf(t, "POST", "/api/v1/repos/%s/%s/actions/runs/%d/rerun-failed", user2.Name, repo.Name, run.ID).
			AddTokenAuth(token)
		MakeRequest(t, req, http.StatusConflict)
	})
}

func TestAPIRerunActionJob(t *testing.T) {
	if !setting.Database.Type.IsSQLite3() {
		t.Skip()
	}
	onApplicationRun(t, func(t *testing.T, u *url.URL) {
		repo, user2, token, run := rerunSetup(t, u, "actions-rerun-job", runnerv1.Result_RESULT_SUCCESS)

		jobs, err := actions_model.GetRunJobsByRunID(t.Context(), run.ID)
		require.NoError(t, err)
		require.Len(t, jobs, 1)

		req := NewRequestf(t, "POST", "/api/v1/repos/%s/%s/actions/jobs/%d/rerun", user2.Name, repo.Name, jobs[0].ID).
			AddTokenAuth(token)
		MakeRequest(t, req, http.StatusNoContent)

		runner := newMockRunner()
		runner.registerAsRepoRunner(t, user2.Name, repo.Name, "mock-runner-2", []string{"ubuntu-latest"})
		task := runner.fetchTask(t)
		runner.execTask(t, task, rerunOutcome(runnerv1.Result_RESULT_SUCCESS))
	})
}
