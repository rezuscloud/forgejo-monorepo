// Package integration — exercises every hand-written (non-generated) fj CLI
// subcommand against a live Forgejo instance.
//
// The instance is a throwaway container started fresh by CI (see
// .github/workflows/ci.yml), so this suite is free to run BOTH read and write
// commands (create issue/PR/release/tag, comment, merge, delete, ...) and then
// throw the whole server away. Nothing here ever touches a real instance.
//
// Seeding (repos, branches, PRs, orgs, wiki pages) is done via raw HTTP so the
// test does not depend on the very CLI it is validating.
package integration

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// --- raw-HTTP seed helpers (independent of the CLI under test) ------------

// apiJSON performs an authenticated JSON request and returns status + body.
func apiJSON(t *testing.T, method, path string, body interface{}) (int, string) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, testURL()+"/api/v1"+path, r)
	if err != nil {
		t.Fatalf("apiJSON %s %s: %v", method, path, err)
	}
	req.Header.Set("Authorization", "token "+testToken())
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("apiJSON %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// createFileOnBranch commits a new file, optionally on a new branch (PR seed).
func createFileOnBranch(t *testing.T, repo, filePath, branch, newBranch, content string) {
	t.Helper()
	body := map[string]interface{}{
		"branch":  branch,
		"message": "seed: " + filePath,
		"content": base64.StdEncoding.EncodeToString([]byte(content)),
	}
	if newBranch != "" {
		body["new_branch"] = newBranch
	}
	status, out := apiJSON(t, "POST",
		fmt.Sprintf("/repos/%s/%s/contents/%s", testUser(), repo, filePath), body)
	if status >= 300 {
		t.Fatalf("createFileOnBranch(%s) status %d: %s", filePath, status, out)
	}
}

// createPR creates a pull request via the API and returns its index.
func createPR(t *testing.T, repo, title, head, base string) int64 {
	t.Helper()
	body := map[string]interface{}{"title": title, "head": head, "base": base}
	status, out := apiJSON(t, "POST", fmt.Sprintf("/repos/%s/%s/pulls", testUser(), repo), body)
	if status >= 300 {
		t.Fatalf("createPR status %d: %s", status, out)
	}
	var pr struct {
		Number int64 `json:"number"`
	}
	json.Unmarshal([]byte(out), &pr)
	return pr.Number
}

func createWikiPage(t *testing.T, repo, title, content string) {
	t.Helper()
	body := map[string]interface{}{
		"title":          title,
		"content_base64": base64.StdEncoding.EncodeToString([]byte(content)),
		"message":        "seed wiki",
	}
	status, out := apiJSON(t, "POST", fmt.Sprintf("/repos/%s/%s/wiki/new", testUser(), repo), body)
	if status >= 300 {
		t.Fatalf("createWikiPage status %d: %s", status, out)
	}
}

func createOrg(t *testing.T, name string) {
	t.Helper()
	body := map[string]interface{}{"username": name, "visibility": "public"}
	status, out := apiJSON(t, "POST", "/orgs", body)
	if status >= 300 {
		t.Fatalf("createOrg(%s) status %d: %s", name, status, out)
	}
}

// extractID pulls the first "#NN" from CLI output (issue/PR/release create).
var idRe = regexp.MustCompile(`#(\d+)`)

func extractID(t *testing.T, out string) int64 {
	t.Helper()
	m := idRe.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no #ID in output: %q", out)
	}
	id, _ := strconv.ParseInt(m[1], 10, 64)
	return id
}

// contains is a tiny test helper.
func contains(t *testing.T, out, want string) {
	t.Helper()
	if !strings.Contains(out, want) {
		t.Errorf("output missing %q:\n%s", want, out)
	}
}

// TestCLICommands exercises every fj subcommand — hand-written and
// descriptor-driven polished (gen/polish.json) alike (read + write).
func TestCLICommands(t *testing.T) {
	skipIfNoInstance(t)
	binary := buildFjBinary(t)

	// Shared scratch repo for repo-scoped commands. Unique per-run so local
	// re-runs against the same container do not 409 on an existing repo.
	repo := fmt.Sprintf("cli-all-%d", time.Now().UnixNano())
	createTestRepo(t, repo)
	ownerRepo := testUser() + "/" + repo

	// ---- top-level / host-scoped commands ------------------------------
	t.Run("version", func(t *testing.T) {
		out, err := runFj(t, binary, "version")
		if err != nil {
			t.Fatal(err)
		}
		contains(t, out, "Version")
	})

	t.Run("whoami", func(t *testing.T) {
		out, err := runFj(t, binary, "whoami")
		if err != nil {
			t.Fatal(err)
		}
		contains(t, out, testUser())
	})

	t.Run("auth/list", func(t *testing.T) {
		// auth list reads the local keys.json (not the FORGEJO_TOKEN the rest of
		// the suite uses), so its output reflects the host's login state, not the
		// test instance. Assert only that the command runs cleanly.
		if _, err := runFj(t, binary, "auth", "list"); err != nil {
			t.Fatal(err)
		}
	})

	// ---- repo ----------------------------------------------------------
	t.Run("repo/view", func(t *testing.T) {
		// positional OWNER/NAME form
		out, err := runFj(t, binary, "repo", "view", ownerRepo)
		if err != nil {
			t.Fatal(err)
		}
		contains(t, out, repo)
	})

	// ---- issue: full lifecycle (create → list → view → comment → comments →
	// close → reopen). Descriptor-driven (gen/polish.json): every polished
	// endpoint gets a write-path assertion here.
	t.Run("issue", func(t *testing.T) {
		out, err := runFj(t, binary, "issue", "create", "-r", ownerRepo,
			"-t", "fj integration issue", "-b", "created by the integration suite")
		if err != nil {
			t.Fatal(err)
		}
		idx := extractID(t, out)

		if out, err = runFj(t, binary, "issue", "list", "-r", ownerRepo); err != nil {
			t.Fatal(err)
		}
		contains(t, out, "fj integration issue")

		if out, err = runFj(t, binary, "issue", "view", strconv.FormatInt(idx, 10),
			"-r", ownerRepo); err != nil {
			t.Fatal(err)
		}
		contains(t, out, "fj integration issue")
		contains(t, out, "State: open")

		if _, err = runFj(t, binary, "issue", "comment", strconv.FormatInt(idx, 10),
			"-b", "a test comment", "-r", ownerRepo); err != nil {
			t.Fatal(err)
		}
		// issue comments must render the thread we just added
		if out, err = runFj(t, binary, "issue", "comments", strconv.FormatInt(idx, 10),
			"-r", ownerRepo); err != nil {
			t.Fatal(err)
		}
		contains(t, out, "a test comment")

		if _, err = runFj(t, binary, "issue", "close", strconv.FormatInt(idx, 10),
			"-r", ownerRepo); err != nil {
			t.Fatal(err)
		}
		// close must be observable ...
		if out, err = runFj(t, binary, "issue", "view", strconv.FormatInt(idx, 10),
			"-r", ownerRepo); err != nil {
			t.Fatal(err)
		}
		contains(t, out, "State: closed")
		// ... via list too — the -s closed filter binding + row state column
		if out, err = runFj(t, binary, "issue", "list", "-s", "closed",
			"-r", ownerRepo); err != nil {
			t.Fatal(err)
		}
		contains(t, out, "fj integration issue")
		// ... and reopen (#116) must flip it back
		if out, err = runFj(t, binary, "issue", "reopen", strconv.FormatInt(idx, 10),
			"-r", ownerRepo); err != nil {
			t.Fatal(err)
		}
		contains(t, out, "Reopened #")
		if out, err = runFj(t, binary, "issue", "view", strconv.FormatInt(idx, 10),
			"-r", ownerRepo); err != nil {
			t.Fatal(err)
		}
		contains(t, out, "State: open")

		// labels (#104): names resolve server-side — add / list / remove / set
		// through the polished issue group's extra command.
		idxs := strconv.FormatInt(idx, 10)
		// label creation rides the generated api surface; its --repo path
		// param shadows the root -r shorthand, so owner/repo go as full flags
		split := strings.SplitN(ownerRepo, "/", 2)
		for _, lbl := range []string{
			`{"name":"area/cli","color":"#00aabb"}`,
			`{"name":"area/api","color":"#aa00bb"}`,
		} {
			if _, err = runFj(t, binary, "api", "repo", "issue-create-label",
				"--owner", split[0], "--repo", split[1], "--body", lbl); err != nil {
				t.Fatal(err)
			}
		}
		if out, err = runFj(t, binary, "issue", "label", idxs,
			"-r", ownerRepo, "--add", "area/cli, area/api"); err != nil {
			t.Fatal(err)
		}
		contains(t, out, "area/cli")
		contains(t, out, "area/api")
		// bare invocation lists
		if out, err = runFj(t, binary, "issue", "label", idxs, "-r", ownerRepo); err != nil {
			t.Fatal(err)
		}
		contains(t, out, "area/cli")
		// remove one, the other survives
		if out, err = runFj(t, binary, "issue", "label", idxs,
			"-r", ownerRepo, "--remove", "area/cli"); err != nil {
			t.Fatal(err)
		}
		contains(t, out, "area/api")
		if strings.Contains(out, "area/cli") {
			t.Fatalf("removed label still present: %s", out)
		}
		// set replaces the whole set
		if out, err = runFj(t, binary, "issue", "label", idxs,
			"-r", ownerRepo, "--set", "area/cli"); err != nil {
			t.Fatal(err)
		}
		contains(t, out, "area/cli")
		if strings.Contains(out, "area/api") {
			t.Fatalf("--set must replace, not append: %s", out)
		}
		// --set refuses to combine (add/remove with set is a footgun)
		if _, err = runFj(t, binary, "issue", "label", idxs,
			"-r", ownerRepo, "--set", "area/cli", "--add", "area/api"); err == nil {
			t.Fatal("expected --set + --add to be rejected")
		}
		// unknown names are silently dropped by the forge (documented upstream
		// behavior — names resolve server-side, missing ones add nothing)
		if out, err = runFj(t, binary, "issue", "label", idxs,
			"-r", ownerRepo, "--add", "no-such-label"); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, "no-such-label") {
			t.Fatalf("unknown label leaked into the issue's labels: %s", out)
		}
	})

	// ---- milestone: full lifecycle (create → list → view → edit → close →
	// delete), mirroring the issue/release blocks --------------------
	t.Run("milestone", func(t *testing.T) {
		out, err := runFj(t, binary, "milestone", "create", "-r", ownerRepo,
			"-t", "fj integration milestone", "-d", "created by the integration suite")
		if err != nil {
			t.Fatal(err)
		}
		id := extractID(t, out)

		if out, err = runFj(t, binary, "milestone", "list", "-r", ownerRepo); err != nil {
			t.Fatal(err)
		}
		contains(t, out, "fj integration milestone")

		if out, err = runFj(t, binary, "milestone", "view", strconv.FormatInt(id, 10),
			"-r", ownerRepo); err != nil {
			t.Fatal(err)
		}
		contains(t, out, "fj integration milestone")
		contains(t, out, "created by the integration suite")

		if _, err = runFj(t, binary, "milestone", "edit", strconv.FormatInt(id, 10),
			"-r", ownerRepo, "-t", "fj integration milestone renamed"); err != nil {
			t.Fatal(err)
		}

		if _, err = runFj(t, binary, "milestone", "close", strconv.FormatInt(id, 10),
			"-r", ownerRepo); err != nil {
			t.Fatal(err)
		}
		// closed milestone visible under -s closed
		if out, err = runFj(t, binary, "milestone", "list", "-s", "closed", "-r", ownerRepo); err != nil {
			t.Fatal(err)
		}
		contains(t, out, "fj integration milestone renamed")

		if _, err = runFj(t, binary, "milestone", "delete", strconv.FormatInt(id, 10),
			"-r", ownerRepo); err != nil {
			t.Fatal(err)
		}
	})

	// ---- pull request: seed branch via API, then create/list/view/status/merge
	t.Run("pr", func(t *testing.T) {
		createFileOnBranch(t, repo, "feature.txt", "", "feature-branch", "hello")
		out, err := runFj(t, binary, "pr", "create", "-r", ownerRepo,
			"-t", "fj integration PR", "--head", "feature-branch", "--base", "main")
		if err != nil {
			t.Fatal(err)
		}
		idx := extractID(t, out)

		if out, err = runFj(t, binary, "pr", "list", "-r", ownerRepo); err != nil {
			t.Fatal(err)
		}
		contains(t, out, "fj integration PR")

		if out, err = runFj(t, binary, "pr", "view", strconv.FormatInt(idx, 10),
			"-r", ownerRepo); err != nil {
			t.Fatal(err)
		}
		contains(t, out, "fj integration PR")

		// status must not crash even when there are no CI checks yet
		if _, err = runFj(t, binary, "pr", "status", strconv.FormatInt(idx, 10),
			"-r", ownerRepo); err != nil {
			t.Fatal(err)
		}

		// a PR is an issue to the labels API — the same command under `pr`
		// must work unchanged (#104)
		if out, err = runFj(t, binary, "pr", "label", strconv.FormatInt(idx, 10),
			"-r", ownerRepo, "--add", "area/cli"); err != nil {
			t.Fatal(err)
		}
		contains(t, out, "area/cli")

		if _, err = runFj(t, binary, "pr", "merge", strconv.FormatInt(idx, 10),
			"-s", "merge", "-r", ownerRepo); err != nil {
			t.Fatal(err)
		}
	})

	// ---- status: combined + raw commit-status views. The per-entry JSON
	// field is `status` (not GitHub's `state`) — the polished view normalizes
	// it to a rendered STATE column so GitHub-schema reflexes cannot misread
	// it (the #132 incident class). Descriptor-driven via `entries` (#135).
	t.Run("status", func(t *testing.T) {
		// resolve the seed repo's current head (earlier subtests merged PRs)
		code, body := apiJSON(t, "GET", "/repos/"+ownerRepo+"/branches/main", nil)
		if code >= 300 {
			t.Fatalf("get main branch: %d %s", code, body)
		}
		var br struct {
			Commit struct {
				ID string `json:"id"`
			} `json:"commit"`
		}
		if err := json.Unmarshal([]byte(body), &br); err != nil {
			t.Fatal(err)
		}
		sha := br.Commit.ID

		post := func(context, state string) {
			t.Helper()
			code, out := apiJSON(t, "POST", "/repos/"+ownerRepo+"/statuses/"+sha, map[string]interface{}{
				"state":       state,
				"context":     context,
				"description": "seed " + context + " " + state,
			})
			if code >= 300 {
				t.Fatalf("seed status %s=%s: %d %s", context, state, code, out)
			}
		}
		post("ci/seed-superseded", "failure")
		post("ci/seed-superseded", "success") // latest record per context wins
		post("ci/seed-pending", "pending")

		// view by sha: normalized overall + per-context states
		out, err := runFj(t, binary, "status", "view", sha, "-r", ownerRepo)
		if err != nil {
			t.Fatal(err)
		}
		contains(t, out, "Overall:")
		contains(t, out, "pending") // combined state: one pending context
		contains(t, out, "ci/seed-superseded")
		contains(t, out, "success") // per-entry state rendered — the raw JSON field is `status`
		if strings.Contains(out, "failure") {
			t.Errorf("superseded failure leaked into the combined view:\n%s", out)
		}

		// view by branch ref resolves the same head
		if out, err = runFj(t, binary, "status", "view", "main", "-r", ownerRepo); err != nil {
			t.Fatal(err)
		}
		contains(t, out, "Overall:")
		contains(t, out, "ci/seed-pending")

		// list: raw records with descriptions rendered; ordering asserted via
		// the deterministic index sort (created_unix ties break arbitrarily —
		// the records post within the same second)
		if out, err = runFj(t, binary, "status", "list", sha, "-r", ownerRepo); err != nil {
			t.Fatal(err)
		}
		contains(t, out, "seed ci/seed-pending pending")
		contains(t, out, "seed ci/seed-superseded success")
		contains(t, out, "seed ci/seed-superseded failure")
		if out, err = runFj(t, binary, "status", "list", sha, "-r", ownerRepo,
			"--sort", "leastindex"); err != nil {
			t.Fatal(err)
		}
		pendingAt := strings.Index(out, "ci/seed-pending")
		successAt := strings.Index(out, "seed ci/seed-superseded success")
		failureAt := strings.Index(out, "seed ci/seed-superseded failure")
		if pendingAt < 0 || successAt < 0 || failureAt < 0 {
			t.Fatalf("status list missing records:\n%s", out)
		}
		if !(pendingAt < successAt && successAt < failureAt) {
			t.Errorf("status list --sort leastindex not newest-posted-first (pending@%d success@%d failure@%d):\n%s",
				pendingAt, successAt, failureAt, out)
		}

		// state filter: only matching records
		if out, err = runFj(t, binary, "status", "list", sha, "-r", ownerRepo,
			"--state", "pending"); err != nil {
			t.Fatal(err)
		}
		contains(t, out, "ci/seed-pending")
		if strings.Contains(out, "ci/seed-superseded") {
			t.Errorf("--state pending leaked non-pending records:\n%s", out)
		}

		// empty head: a fresh branch commit with no statuses
		createFileOnBranch(t, repo, "status-empty.txt", "", "status-empty-branch", "x")
		if out, err = runFj(t, binary, "status", "view", "status-empty-branch", "-r", ownerRepo); err != nil {
			t.Fatal(err)
		}
		contains(t, out, "no statuses reported")
	})

	// ---- review: pending → comment → submit → reply → resolve (#115) ----
	t.Run("review", func(t *testing.T) {
		createFileOnBranch(t, repo, "review.txt", "", "review-branch", "hello")
		out, err := runFj(t, binary, "pr", "create", "-r", ownerRepo,
			"-t", "fj integration review PR", "--head", "review-branch", "--base", "main")
		if err != nil {
			t.Fatal(err)
		}
		idx := extractID(t, out)

		// no event => pending review (API semantics: CreatePullReview with empty event)
		if out, err = runFj(t, binary, "review", "create", strconv.FormatInt(idx, 10),
			"-r", ownerRepo, "--body", "adversarial pass"); err != nil {
			t.Fatal(err)
		}
		contains(t, out, "review #")
		reviewID := extractID(t, out)

		if out, err = runFj(t, binary, "review", "comment", strconv.FormatInt(idx, 10),
			strconv.FormatInt(reviewID, 10), "-r", ownerRepo,
			"--body", "finding: loosen the coupling", "--path", "review.txt", "--new-position", "1"); err != nil {
			t.Fatal(err)
		}
		commentID := extractID(t, out)

		if out, err = runFj(t, binary, "review", "submit", strconv.FormatInt(idx, 10),
			strconv.FormatInt(reviewID, 10), "-r", ownerRepo, "--event", "COMMENT",
			"--body", "verdict: findings tracked in threads"); err != nil {
			t.Fatal(err)
		}
		contains(t, out, fmt.Sprintf("review #%d submitted", reviewID))

		// reply lands in the same thread (#115)
		if out, err = runFj(t, binary, "review", "reply", strconv.FormatInt(idx, 10),
			strconv.FormatInt(commentID, 10), "-r", ownerRepo, "--body", "fix: decoupled in abc123"); err != nil {
			t.Fatal(err)
		}
		contains(t, out, "Replied to comment #")

		// resolve / unresolve the conversation (#115) — observable via comments listing
		if out, err = runFj(t, binary, "review", "resolve", strconv.FormatInt(idx, 10),
			strconv.FormatInt(commentID, 10), "-r", ownerRepo); err != nil {
			t.Fatal(err)
		}
		contains(t, out, fmt.Sprintf("Resolved #%d", commentID))
		if out, err = runFj(t, binary, "review", "comments", strconv.FormatInt(idx, 10),
			strconv.FormatInt(reviewID, 10), "-r", ownerRepo); err != nil {
			t.Fatal(err)
		}
		contains(t, out, fmt.Sprintf("#%d [true]", commentID))

		if out, err = runFj(t, binary, "review", "unresolve", strconv.FormatInt(idx, 10),
			strconv.FormatInt(commentID, 10), "-r", ownerRepo); err != nil {
			t.Fatal(err)
		}
		contains(t, out, fmt.Sprintf("Unresolved #%d", commentID))
		if out, err = runFj(t, binary, "review", "comments", strconv.FormatInt(idx, 10),
			strconv.FormatInt(reviewID, 10), "-r", ownerRepo); err != nil {
			t.Fatal(err)
		}
		contains(t, out, fmt.Sprintf("#%d [false]", commentID))
	})

	// ---- release: create → list → view → delete -----------------------
	t.Run("release", func(t *testing.T) {
		out, err := runFj(t, binary, "release", "create", "-r", ownerRepo,
			"--tag", "v-rel-test", "-n", "release one", "-b", "body")
		if err != nil {
			t.Fatal(err)
		}
		id := extractID(t, out)

		if out, err = runFj(t, binary, "release", "list", "-r", ownerRepo); err != nil {
			t.Fatal(err)
		}
		contains(t, out, "release one")

		if _, err = runFj(t, binary, "release", "view", strconv.FormatInt(id, 10),
			"-r", ownerRepo); err != nil {
			t.Fatal(err)
		}

		if _, err = runFj(t, binary, "release", "delete", strconv.FormatInt(id, 10),
			"-r", ownerRepo); err != nil {
			t.Fatal(err)
		}
	})

	// ---- tag: create → list → delete ----------------------------------
	t.Run("tag", func(t *testing.T) {
		if _, err := runFj(t, binary, "tag", "create", "v-tag-test",
			"-m", "annotated by integration", "-r", ownerRepo); err != nil {
			t.Fatal(err)
		}
		out, err := runFj(t, binary, "tag", "list", "-r", ownerRepo)
		if err != nil {
			t.Fatal(err)
		}
		contains(t, out, "v-tag-test")
		if _, err = runFj(t, binary, "tag", "delete", "v-tag-test", "-r", ownerRepo); err != nil {
			t.Fatal(err)
		}
	})

	// ---- user: view / search / repos (host-scoped) --------------------
	t.Run("user", func(t *testing.T) {
		if out, err := runFj(t, binary, "user", "view", testUser()); err != nil {
			t.Fatal(err)
		} else {
			contains(t, out, testUser())
		}
		if _, err := runFj(t, binary, "user", "search", testUser()); err != nil {
			t.Fatal(err)
		}
		// the admin owns at least the repo we just created
		if out, err := runFj(t, binary, "user", "repos", testUser()); err != nil {
			t.Fatal(err)
		} else {
			contains(t, out, repo)
		}
	})

	// ---- org: seed via API, then list / view --------------------------
	t.Run("org", func(t *testing.T) {
		org := fmt.Sprintf("org-%d", time.Now().UnixNano())
		createOrg(t, org)
		if out, err := runFj(t, binary, "org", "list"); err != nil {
			t.Fatal(err)
		} else {
			contains(t, out, org)
		}
		if out, err := runFj(t, binary, "org", "view", org); err != nil {
			t.Fatal(err)
		} else {
			contains(t, out, org)
		}

		// the friendly create surface (#105): --username maps into the body
		// field the raw api command's --organization blob could never reach
		created := fmt.Sprintf("org-fj-%d", time.Now().UnixNano())
		if out, err := runFj(t, binary, "org", "create", "--username", created,
			"--full-name", "fj integration", "--visibility", "limited"); err != nil {
			t.Fatal(err)
		} else {
			contains(t, out, created)
			contains(t, out, "limited")
		}
		if out, err := runFj(t, binary, "org", "view", created); err != nil {
			t.Fatal(err)
		} else {
			contains(t, out, "fj integration")
		}
		// missing --username fails before any request
		if _, err := runFj(t, binary, "org", "create"); err == nil {
			t.Fatal("expected org create without --username to fail")
		}
	})

	// ---- wiki: seed a page via API, then list / view ------------------
	t.Run("wiki", func(t *testing.T) {
		createWikiPage(t, repo, "Home", "welcome to the integration wiki")
		if out, err := runFj(t, binary, "wiki", "list", "-r", ownerRepo); err != nil {
			t.Fatal(err)
		} else {
			contains(t, out, "Home")
		}
		if _, err := runFj(t, binary, "wiki", "view", "Home", "-r", ownerRepo); err != nil {
			t.Fatal(err)
		}
	})

	// ---- actions: list commands + variables/secrets CRUD --------------
	// (jobs/logs require a workflow + runner; those are exercised against the
	// real CI in the manual smoke test. Here we cover the CRUD surface.)
	t.Run("actions", func(t *testing.T) {
		// read commands — succeed even with no workflows yet
		if _, err := runFj(t, binary, "actions", "tasks", "-r", ownerRepo); err != nil {
			t.Fatal(err)
		}
		if _, err := runFj(t, binary, "actions", "runs", "-r", ownerRepo); err != nil {
			t.Fatal(err)
		}
		// filters pass through to list-action-runs (#74) — exit-0 is the
		// contract on an empty repo ("no runs"), flags must at least bind.
		if _, err := runFj(t, binary, "actions", "runs", "-r", ownerRepo, "--limit", "3", "--page", "1"); err != nil {
			t.Fatal(err)
		}
		if _, err := runFj(t, binary, "actions", "runs", "-r", ownerRepo,
			"--status", "success,failure", "--event", "push", "--head-sha", "0af1a3633115fe49317c0289ecb45b18ba3cf0ee",
			"--ref", "main", "--workflow-id", "1", "--run-number", "1"); err != nil {
			t.Fatal(err)
		}

		// job metadata endpoint (#126 backport): flag binding + not-found error
		// surface. A real job needs a runner, which the harness lacks — a
		// missing job must fail SERVER-SIDE (handler 404 message on stderr),
		// never at flag parsing, proving the command reaches the endpoint.
		out, errOut, err := runFjFull(t, binary, "actions", "job", "999999", "-r", ownerRepo)
		if err == nil {
			t.Fatalf("expected not-found error for nonexistent job, got output: %s", out)
		}
		if strings.Contains(errOut, "unknown flag") || strings.Contains(errOut, "invalid argument") {
			t.Fatalf("command failed at flag parsing, not at the endpoint: %s", errOut)
		}

		// #108: jobs/logs address runs by INDEX (the number 'actions runs'
		// prints); resolution happens server-side — a nonexistent index must
		// fail at the by-index endpoint (404 on stderr), never at flag
		// parsing. --run-id must bind and take the raw DB-id path.
		_, errOut, err = runFjFull(t, binary, "actions", "jobs", "1", "-r", ownerRepo)
		if err == nil {
			t.Fatalf("expected not-found error for nonexistent run index, got success")
		}
		if strings.Contains(errOut, "unknown flag") || strings.Contains(errOut, "invalid argument") {
			t.Fatalf("jobs <index> failed at flag parsing, not at the endpoint: %s", errOut)
		}
		// #153: rerun wraps the rerun endpoints — this harness has no
		// completed run, so the not-found surface proves flag binding and
		// endpoint reach for all three modes (all jobs via index, failed-only
		// via index, single job via --job); none may fail at flag parsing.
		_, errOut, err = runFjFull(t, binary, "actions", "rerun", "1", "-r", ownerRepo)
		if err == nil {
			t.Fatalf("expected not-found error for nonexistent run index, got success")
		}
		if strings.Contains(errOut, "unknown flag") || strings.Contains(errOut, "invalid argument") {
			t.Fatalf("rerun <index> failed at flag parsing, not at the endpoint: %s", errOut)
		}
		_, errOut, err = runFjFull(t, binary, "actions", "rerun", "1", "--failed-only", "-r", ownerRepo)
		if err == nil {
			t.Fatalf("expected not-found error for nonexistent run index (failed-only), got success")
		}
		if strings.Contains(errOut, "unknown flag") {
			t.Fatalf("rerun --failed-only failed at flag parsing: %s", errOut)
		}
		_, errOut, err = runFjFull(t, binary, "actions", "rerun", "1", "--job", "999999", "-r", ownerRepo)
		if err == nil {
			t.Fatalf("expected not-found error for nonexistent job, got success")
		}
		if strings.Contains(errOut, "unknown flag") {
			t.Fatalf("rerun --job failed at flag parsing: %s", errOut)
		}
		_, errOut, err = runFjFull(t, binary, "actions", "jobs", "1", "--run-id", "-r", ownerRepo)
		if err == nil {
			t.Fatalf("expected not-found error for nonexistent raw run id, got success")
		}
		if strings.Contains(errOut, "unknown flag") {
			t.Fatalf("--run-id flag did not bind: %s", errOut)
		}
		_, errOut, err = runFjFull(t, binary, "actions", "logs", "--run", "1", "-r", ownerRepo)
		if err == nil {
			t.Fatalf("expected not-found error for logs of nonexistent run index, got success")
		}
		if strings.Contains(errOut, "unknown flag") {
			t.Fatalf("logs --run <index> failed at flag parsing, not at the endpoint: %s", errOut)
		}

		// variables CRUD
		if _, err := runFj(t, binary, "actions", "variables", "create",
			"VAR_TEST", "v1", "-r", ownerRepo); err != nil {
			t.Fatal(err)
		}
		if out, err := runFj(t, binary, "actions", "variables", "list", "-r", ownerRepo); err != nil {
			t.Fatal(err)
		} else {
			contains(t, out, "VAR_TEST")
		}
		if _, err := runFj(t, binary, "actions", "variables", "delete",
			"VAR_TEST", "-r", ownerRepo); err != nil {
			t.Fatal(err)
		}

		// secrets CRUD
		if _, err := runFj(t, binary, "actions", "secrets", "create",
			"SEC_TEST", "topsecret", "-r", ownerRepo); err != nil {
			t.Fatal(err)
		}
		if out, err := runFj(t, binary, "actions", "secrets", "list", "-r", ownerRepo); err != nil {
			t.Fatal(err)
		} else {
			contains(t, out, "SEC_TEST")
		}
		if _, err := runFj(t, binary, "actions", "secrets", "delete",
			"SEC_TEST", "-r", ownerRepo); err != nil {
			t.Fatal(err)
		}
	})

	// ---- actions workflows + dispatch (#114): the filename the dispatch API
	// keys on gets a discovery endpoint; a missing workflow/ref maps to 404
	// (was: bare error -> 500) and the 204 dispatch response no longer
	// EOF-errors the SDK decode.
	t.Run("actions workflows+dispatch", func(t *testing.T) {
		const wfContent = `name: cli-probe
on:
  workflow_dispatch:
jobs:
  probe:
    runs-on: [no-such-runner]
    steps:
      - run: echo hi
`
		createFileOnBranch(t, repo, ".forgejo/workflows/cli-probe.yml", "main", "", wfContent)

		// list: the new discovery endpoint via the CLI
		out, err := runFj(t, binary, "actions", "workflows", "-r", ownerRepo)
		if err != nil {
			t.Fatal(err)
		}
		contains(t, out, "cli-probe.yml")
		contains(t, out, "cli-probe") // the workflow's display name

		// raw shape: 200 + total_count
		status, body := apiJSON(t, "GET", "/repos/"+ownerRepo+"/actions/workflows", nil)
		if status != 200 {
			t.Fatalf("GET workflows status %d: %s", status, body)
		}
		contains(t, body, `"total_count":1`)

		// dispatch the real workflow: 204 empty body must not error the CLI
		if _, err := runFj(t, binary, "actions", "dispatch", "cli-probe.yml", "main", "-r", ownerRepo); err != nil {
			t.Fatal(err)
		}

		// missing workflow: 404 (was 500 + a bare "workflow not found")
		status, body = apiJSON(t, "POST", "/repos/"+ownerRepo+"/actions/workflows/missing.yml/dispatches",
			map[string]interface{}{"ref": "main"})
		if status != 404 {
			t.Fatalf("dispatch missing workflow status %d (want 404): %s", status, body)
		}

		// missing ref: 404 too (was 500 via ExpandRef's bare error)
		status, body = apiJSON(t, "POST", "/repos/"+ownerRepo+"/actions/workflows/cli-probe.yml/dispatches",
			map[string]interface{}{"ref": "no-such-ref"})
		if status != 404 {
			t.Fatalf("dispatch missing ref status %d (want 404): %s", status, body)
		}
	})

	// ---- repo clone: needs git + auth; verify it at least shells out --
	// (best-effort — skip if git is unavailable rather than fail the suite)
	t.Run("repo/clone", func(t *testing.T) {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git not installed")
		}
		dir := t.TempDir()
		// clone over HTTP with the token embedded (private repo)
		cloneURL := strings.Replace(testURL()+"/"+ownerRepo+".git",
			"http://", "http://"+testUser()+":"+testToken()+"@", 1)
		cmd := exec.Command("git", "clone", cloneURL, dir+"/repo")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git clone failed (auth/network in this env): %v\n%s", err, out)
		}
	})
}
