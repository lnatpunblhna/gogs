package integrationtests

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type externalReviewPayload struct {
	Action      string `json:"action"`
	Context     string `json:"context"`
	Mode        string `json:"mode"`
	SHA         string `json:"sha"`
	StatusURL   string `json:"status_url"`
	Number      int64  `json:"number"`
	PullRequest struct {
		BaseBranch string `json:"base_branch"`
		HeadBranch string `json:"head_branch"`
	} `json:"pull_request"`
}

// externalReviewNextRequest waits for the next delivery of the receiver, which
// must be a request for an external review.
func externalReviewNextRequest(t *testing.T, deliveries <-chan repoSettingsDelivery) externalReviewPayload {
	t.Helper()

	d := repoSettingsNextDelivery(t, deliveries)
	require.Equal(t, "external_review", d.Header.Get("X-Gogs-Event"), "payload: %s", d.Body)
	var payload externalReviewPayload
	require.NoError(t, json.Unmarshal(d.Body, &payload), "payload: %s", d.Body)
	return payload
}

type externalReviewPullView struct {
	Pull struct {
		State          string `json:"state"`
		ViewerCanMerge bool   `json:"viewerCanMerge"`
		ExternalReview *struct {
			Mode        string `json:"mode"`
			Context     string `json:"context"`
			State       string `json:"state"`
			TargetURL   string `json:"targetURL"`
			Description string `json:"description"`
			CommitID    string `json:"commitID"`
			BlocksMerge bool   `json:"blocksMerge"`
		} `json:"externalReview"`
	} `json:"pull"`
}

func externalReviewPull(t *testing.T, c *client, repo *Repository, index int64) *externalReviewPullView {
	t.Helper()

	var v externalReviewPullView
	c.doJSON(http.MethodGet, issueAPIPath(repo, "pulls", index), nil, http.StatusOK, &v)
	return &v
}

func TestExternalReview(t *testing.T) {
	t.Parallel()

	owner := seedUser(t, CreateUserOptions{})
	other := seedUser(t, CreateUserOptions{})
	repo := seedRepository(t, owner.ID, CreateRepositoryOptions{AutoInit: true})
	ownerClient := authenticatedClient(t, owner)
	ownerAPI := apiV1Client(t, owner)
	otherAPI := apiV1Client(t, other)

	hookURL, deliveries := repoSettingsWebhookReceiver(t)
	repoSettingsCreateWebhookFor(t, ownerClient, repo, hookURL, true, map[string]bool{"externalReview": true})

	settingsPath := issueAPIPath(repo, "settings", "branches", "main")
	setRule := func(t *testing.T, mode string) {
		t.Helper()
		status, body := ownerClient.do(http.MethodPut, settingsPath, map[string]any{
			"externalReviewMode":    mode,
			"externalReviewContext": "opencodereview",
		})
		require.Equal(t, http.StatusNoContent, status, "response: %s", body)
	}

	t.Run("branch settings", func(t *testing.T) {
		status, body := ownerClient.do(http.MethodPut, settingsPath, map[string]any{
			"externalReviewMode":    "required",
			"externalReviewContext": "not valid",
		})
		require.Equal(t, http.StatusUnprocessableEntity, status, "response: %s", body)
		assert.Contains(t, string(body), "externalReviewContext")

		setRule(t, "required")
		var got struct {
			Protected             bool   `json:"protected"`
			ExternalReviewMode    string `json:"externalReviewMode"`
			ExternalReviewContext string `json:"externalReviewContext"`
		}
		ownerClient.doJSON(http.MethodGet, settingsPath, nil, http.StatusOK, &got)
		assert.False(t, got.Protected, "external reviews do not need the branch to be protected")
		assert.Equal(t, "required", got.ExternalReviewMode)
		assert.Equal(t, "opencodereview", got.ExternalReviewContext)

		var branches struct {
			ExternalReviews []struct {
				Branch string `json:"branch"`
				Mode   string `json:"mode"`
			} `json:"externalReviews"`
		}
		ownerClient.doJSON(http.MethodGet, issueAPIPath(repo, "settings", "branches"), nil, http.StatusOK, &branches)
		require.Len(t, branches.ExternalReviews, 1)
		assert.Equal(t, "main", branches.ExternalReviews[0].Branch)
		assert.Equal(t, "required", branches.ExternalReviews[0].Mode)
	})

	branch := UniqueName("feature-")
	pullCommitToNewBranch(t, ownerClient, repo, branch, "feature.txt", "Hello external review\n")
	firstCommit := pullMustGit(t, repo, "rev-parse", "refs/heads/"+branch)
	index := pullCreate(t, ownerClient, repo, branch, "Add feature")
	merge := func(t *testing.T) (int, []byte) {
		t.Helper()
		return ownerClient.do(http.MethodPost, issueAPIPath(repo, "pulls", index, "merge"), map[string]any{"style": "create_merge_commit"})
	}

	t.Run("opening requests a review", func(t *testing.T) {
		payload := externalReviewNextRequest(t, deliveries)
		assert.Equal(t, "requested", payload.Action)
		assert.Equal(t, "opencodereview", payload.Context)
		assert.Equal(t, "required", payload.Mode)
		assert.Equal(t, firstCommit, payload.SHA)
		assert.Equal(t, index, payload.Number)
		assert.Equal(t, "main", payload.PullRequest.BaseBranch)
		assert.Equal(t, branch, payload.PullRequest.HeadBranch)
		assert.True(t, strings.HasSuffix(payload.StatusURL, "/api/v1/repos/"+repo.Owner+"/"+repo.Name+"/reviews/"+firstCommit), payload.StatusURL)
	})

	t.Run("pending review blocks merging", func(t *testing.T) {
		pullWaitMergeable(t, ownerClient, repo, index)
		v := externalReviewPull(t, ownerClient, repo, index)
		require.NotNil(t, v.Pull.ExternalReview)
		assert.Equal(t, "pending", v.Pull.ExternalReview.State)
		assert.Equal(t, firstCommit, v.Pull.ExternalReview.CommitID)
		assert.True(t, v.Pull.ExternalReview.BlocksMerge)
		assert.False(t, v.Pull.ViewerCanMerge)

		before := gitMainOnDisk(t, repo)
		status, body := merge(t)
		require.Equal(t, http.StatusUnprocessableEntity, status, "response: %s", body)
		assert.Contains(t, string(body), "must pass the required external review")
		assert.Equal(t, before, gitMainOnDisk(t, repo))
		assert.Equal(t, "mergeable", externalReviewPull(t, ownerClient, repo, index).Pull.State)
	})

	reviewPath := apiV1RepoPath(repo, "/reviews/"+firstCommit)
	t.Run("reporting results", func(t *testing.T) {
		status, body := otherAPI.do(http.MethodPost, reviewPath, map[string]any{"context": "opencodereview", "state": "passed"})
		assert.Equal(t, http.StatusForbidden, status, "people who cannot write cannot report: %s", body)

		for name, tc := range map[string]struct {
			path string
			body map[string]any
			want int
		}{
			"unknown state":     {reviewPath, map[string]any{"context": "opencodereview", "state": "success"}, http.StatusUnprocessableEntity},
			"invalid context":   {reviewPath, map[string]any{"context": "not valid", "state": "passed"}, http.StatusUnprocessableEntity},
			"unsafe target URL": {reviewPath, map[string]any{"context": "opencodereview", "state": "passed", "target_url": "javascript:alert(1)"}, http.StatusUnprocessableEntity},
			"short SHA":         {apiV1RepoPath(repo, "/reviews/"+firstCommit[:7]), map[string]any{"context": "opencodereview", "state": "passed"}, http.StatusUnprocessableEntity},
			"unknown commit":    {apiV1RepoPath(repo, "/reviews/"+strings.Repeat("0", len(firstCommit))), map[string]any{"context": "opencodereview", "state": "passed"}, http.StatusNotFound},
		} {
			status, body := ownerAPI.do(http.MethodPost, tc.path, tc.body)
			assert.Equal(t, tc.want, status, "%s: %s", name, body)
		}

		var failed struct {
			SHA     string `json:"sha"`
			Context string `json:"context"`
			State   string `json:"state"`
			Creator struct {
				UserName string `json:"username"`
			} `json:"creator"`
		}
		ownerAPI.doJSON(http.MethodPost, reviewPath, map[string]any{
			"context":     "opencodereview",
			"state":       "failed",
			"description": "2 blocking issues",
		}, http.StatusCreated, &failed)
		assert.Equal(t, firstCommit, failed.SHA)
		assert.Equal(t, "failed", failed.State)
		assert.Equal(t, owner.Name, failed.Creator.UserName)
		v := externalReviewPull(t, ownerClient, repo, index)
		assert.Equal(t, "failed", v.Pull.ExternalReview.State)
		assert.Equal(t, "2 blocking issues", v.Pull.ExternalReview.Description)
		assert.False(t, v.Pull.ViewerCanMerge)

		// A result of another tool does not count.
		ownerAPI.doJSON(http.MethodPost, reviewPath, map[string]any{"context": "lint", "state": "passed"}, http.StatusCreated, &map[string]any{})
		assert.False(t, externalReviewPull(t, ownerClient, repo, index).Pull.ViewerCanMerge)

		ownerAPI.doJSON(http.MethodPost, reviewPath, map[string]any{
			"context":    "opencodereview",
			"state":      "passed",
			"target_url": "https://review.example.com/reports/1",
		}, http.StatusCreated, &map[string]any{})
		v = externalReviewPull(t, ownerClient, repo, index)
		assert.Equal(t, "passed", v.Pull.ExternalReview.State)
		assert.Equal(t, "https://review.example.com/reports/1", v.Pull.ExternalReview.TargetURL)
		assert.False(t, v.Pull.ExternalReview.BlocksMerge)
		assert.True(t, v.Pull.ViewerCanMerge)

		var statuses []struct {
			Context string `json:"context"`
			State   string `json:"state"`
		}
		otherAPI.doJSON(http.MethodGet, reviewPath, nil, http.StatusOK, &statuses)
		require.Len(t, statuses, 2)
		assert.Equal(t, "lint", statuses[0].Context)
		assert.Equal(t, "opencodereview", statuses[1].Context)
		assert.Equal(t, "passed", statuses[1].State)
	})

	t.Run("new commits need a new review", func(t *testing.T) {
		dir := gitClone(t, gitRemoteURL(t, repo, owner.Name, owner.Password))
		gitRun(t, dir, "checkout", branch)
		secondCommit := gitCommitFile(t, dir, UniqueName("file-")+".txt")
		gitRun(t, dir, "push", "origin", branch)

		payload := externalReviewNextRequest(t, deliveries)
		assert.Equal(t, secondCommit, payload.SHA)

		var v *externalReviewPullView
		require.Eventually(t, func() bool {
			v = externalReviewPull(t, ownerClient, repo, index)
			return v.Pull.State == "mergeable" && v.Pull.ExternalReview != nil && v.Pull.ExternalReview.CommitID == secondCommit
		}, 30*time.Second, 200*time.Millisecond)
		assert.Equal(t, "pending", v.Pull.ExternalReview.State)
		assert.False(t, v.Pull.ViewerCanMerge)
		status, body := merge(t)
		require.Equal(t, http.StatusUnprocessableEntity, status, "response: %s", body)
	})

	t.Run("advisory review keeps merging available", func(t *testing.T) {
		setRule(t, "advisory")
		v := externalReviewPull(t, ownerClient, repo, index)
		require.NotNil(t, v.Pull.ExternalReview)
		assert.Equal(t, "advisory", v.Pull.ExternalReview.Mode)
		assert.Equal(t, "pending", v.Pull.ExternalReview.State)
		assert.True(t, v.Pull.ViewerCanMerge)

		status, body := merge(t)
		require.Equal(t, http.StatusNoContent, status, "response: %s", body)
		v = externalReviewPull(t, ownerClient, repo, index)
		assert.Equal(t, "merged", v.Pull.State)
		assert.Nil(t, v.Pull.ExternalReview)
	})

	t.Run("turning external reviews off", func(t *testing.T) {
		setRule(t, "")
		var got struct {
			ExternalReviewMode string `json:"externalReviewMode"`
		}
		ownerClient.doJSON(http.MethodGet, settingsPath, nil, http.StatusOK, &got)
		assert.Empty(t, got.ExternalReviewMode)
	})
}
