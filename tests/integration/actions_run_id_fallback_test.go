// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"

	actions_model "forgejo.org/models/actions"
	auth_model "forgejo.org/models/auth"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestActionsWebRouteRunIDFallback covers the /actions/runs/{run} route
// accepting the global run ID where the per-repo index is expected: run
// links carry the index, but the API and DB expose the ID, and numbers
// pasted from those surfaces 404'd before (#132, #136).
func TestActionsWebRouteRunIDFallback(t *testing.T) {
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

		// two repos with one run each: distinct indexes AND distinct run IDs.
		// No runner is registered — pushing the workflow file creates the run,
		// which stays queued; that is all this test needs.
		newRepo := func(name string) *repo_model.Repository {
			apiRepo := createActionsTestRepo(t, token, name, false)
			repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: apiRepo.ID})
			treePath := ".gitea/workflows/pr.yml"
			opts := getWorkflowCreateFileOptions(user2, repo.DefaultBranch,
				fmt.Sprintf("create %s", treePath),
				"name: test\non:\n  push:\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo helloworld\n")
			createWorkflowFile(t, token, user2.Name, repo.Name, treePath, opts)
			assert.Equal(t, 1, unittest.GetCount(t, &actions_model.ActionRun{RepoID: repo.ID}))
			return repo
		}

		repo := newRepo("actionsRunIDFallback1")
		otherRepo := newRepo("actionsRunIDFallback2")

		run := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRun{RepoID: repo.ID})
		require.NoError(t, run.LoadAttributes(t.Context()))
		assert.NotEqual(t, run.Index, run.ID, "test needs index and id to differ")

		// canonical index URL keeps its normal redirect to the latest attempt
		req := NewRequest(t, "GET", fmt.Sprintf("%s/actions/runs/%d", repo.HTMLURL(), run.Index))
		resp := MakeRequest(t, req, http.StatusTemporaryRedirect)
		assert.Contains(t, resp.Header().Get("Location"), fmt.Sprintf("/actions/runs/%d/jobs/", run.Index))

		// the run ID redirects to the canonical index URL (same sub-paths)
		req = NewRequest(t, "GET", fmt.Sprintf("%s/actions/runs/%d", repo.HTMLURL(), run.ID))
		resp = MakeRequest(t, req, http.StatusTemporaryRedirect)
		assert.Equal(t, fmt.Sprintf("%s/actions/runs/%d", repo.Link(), run.Index), resp.Header().Get("Location"))

		// a foreign repo's run ID must NOT redirect — the handler 404s
		otherRun := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRun{RepoID: otherRepo.ID})
		req = NewRequest(t, "GET", fmt.Sprintf("%s/actions/runs/%d", repo.HTMLURL(), otherRun.ID))
		MakeRequest(t, req, http.StatusNotFound)

		// a number that is neither this repo's index nor its run ID 404s
		req = NewRequest(t, "GET", fmt.Sprintf("%s/actions/runs/%d", repo.HTMLURL(), otherRun.ID+run.ID+9999))
		MakeRequest(t, req, http.StatusNotFound)
	})
}
