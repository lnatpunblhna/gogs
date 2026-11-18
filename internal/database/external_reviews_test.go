package database

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsValidExternalReviewContext(t *testing.T) {
	for _, tc := range []struct {
		context string
		want    bool
	}{
		{"opencodereview", true},
		{"ci/review-bot_2.0", true},
		{"", false},
		{"-leading-dash", false},
		{"with space", false},
		{"<script>", false},
		{strings.Repeat("a", MaxExternalReviewContextLength), true},
		{strings.Repeat("a", MaxExternalReviewContextLength+1), false},
	} {
		t.Run(tc.context, func(t *testing.T) {
			assert.Equal(t, tc.want, IsValidExternalReviewContext(tc.context))
		})
	}
}

func TestIsValidExternalReviewTargetURL(t *testing.T) {
	for _, tc := range []struct {
		url  string
		want bool
	}{
		{"", true},
		{"https://review.example.com/reports/1", true},
		{"http://localhost:8080/r", true},
		{"javascript:alert(1)", false},
		{"//review.example.com/reports/1", false},
		{"/reports/1", false},
		{"ftp://review.example.com/reports/1", false},
		{"https://", false},
		{"https://example.com/" + strings.Repeat("a", MaxExternalReviewTargetURLLength), false},
	} {
		t.Run(tc.url, func(t *testing.T) {
			assert.Equal(t, tc.want, IsValidExternalReviewTargetURL(tc.url))
		})
	}
}

func TestExternalReviews(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	t.Parallel()

	ctx := context.Background()
	s := &ExternalReviewsStore{
		db: newTestDB(t, "ExternalReviewsStore"),
	}

	for _, tc := range []struct {
		name string
		test func(t *testing.T, ctx context.Context, s *ExternalReviewsStore)
	}{
		{"Rules", externalReviewsRules},
		{"SetStatus", externalReviewsSetStatus},
		{"Check", externalReviewsCheck},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(func() {
				err := clearTables(t, s.db)
				require.NoError(t, err)
			})
			tc.test(t, ctx, s)
		})
		if t.Failed() {
			break
		}
	}
}

func externalReviewsRules(t *testing.T, ctx context.Context, s *ExternalReviewsStore) {
	_, err := s.GetRule(ctx, 1, "main")
	assert.True(t, IsErrExternalReviewRuleNotExist(err), "got %v", err)

	require.NoError(t, s.SaveRule(ctx, 1, "main", ExternalReviewModeRequired, "opencodereview"))
	require.NoError(t, s.SaveRule(ctx, 1, "develop", ExternalReviewModeAdvisory, "opencodereview"))
	require.NoError(t, s.SaveRule(ctx, 2, "main", ExternalReviewModeAdvisory, "other"))

	got, err := s.GetRule(ctx, 1, "main")
	require.NoError(t, err)
	assert.Equal(t, ExternalReviewModeRequired, got.Mode)
	assert.Equal(t, "opencodereview", got.Context)
	assert.NotZero(t, got.CreatedUnix)

	// Saving again replaces the rule of the branch.
	require.NoError(t, s.SaveRule(ctx, 1, "main", ExternalReviewModeAdvisory, "review-bot"))
	got, err = s.GetRule(ctx, 1, "main")
	require.NoError(t, err)
	assert.Equal(t, ExternalReviewModeAdvisory, got.Mode)
	assert.Equal(t, "review-bot", got.Context)

	rules, err := s.ListRules(ctx, 1)
	require.NoError(t, err)
	require.Len(t, rules, 2)
	assert.Equal(t, "develop", rules[0].Branch)
	assert.Equal(t, "main", rules[1].Branch)

	assert.Error(t, s.SaveRule(ctx, 1, "main", "blocking", "opencodereview"))
	assert.Error(t, s.SaveRule(ctx, 1, "main", ExternalReviewModeRequired, ""))

	require.NoError(t, s.DeleteRule(ctx, 1, "main"))
	_, err = s.GetRule(ctx, 1, "main")
	assert.True(t, IsErrExternalReviewRuleNotExist(err), "got %v", err)
	// Deleting a rule that does not exist is not an error.
	require.NoError(t, s.DeleteRule(ctx, 1, "main"))
	_, err = s.GetRule(ctx, 2, "main")
	require.NoError(t, err)
}

func externalReviewsSetStatus(t *testing.T, ctx context.Context, s *ExternalReviewsStore) {
	const commitID = "d3c6e4a1f0c2b8e7a9d5f4c3b2a1908e7d6c5b4a"
	pending, err := s.SetStatus(ctx, 1, SetStatusOptions{
		CommitID:  commitID,
		Context:   "opencodereview",
		State:     ExternalReviewStatePending,
		CreatorID: 1,
	})
	require.NoError(t, err)
	require.NotZero(t, pending.ID)
	assert.NotZero(t, pending.Created.Unix())

	// Reporting again for the same commit and context replaces the result.
	passed, err := s.SetStatus(ctx, 1, SetStatusOptions{
		CommitID:    commitID,
		Context:     "opencodereview",
		State:       ExternalReviewStatePassed,
		TargetURL:   "https://review.example.com/reports/1",
		Description: "No blocking issues",
		CreatorID:   2,
	})
	require.NoError(t, err)
	assert.Equal(t, pending.ID, passed.ID)

	got, err := s.GetStatus(ctx, 1, commitID, "opencodereview")
	require.NoError(t, err)
	assert.Equal(t, ExternalReviewStatePassed, got.State)
	assert.Equal(t, "https://review.example.com/reports/1", got.TargetURL)
	assert.Equal(t, "No blocking issues", got.Description)
	assert.Equal(t, int64(2), got.CreatorID)
	assert.Equal(t, pending.CreatedUnix, got.CreatedUnix)

	_, err = s.SetStatus(ctx, 1, SetStatusOptions{CommitID: commitID, Context: "lint", State: ExternalReviewStateFailed, CreatorID: 2})
	require.NoError(t, err)
	_, err = s.SetStatus(ctx, 2, SetStatusOptions{CommitID: commitID, Context: "opencodereview", State: ExternalReviewStateFailed, CreatorID: 2})
	require.NoError(t, err)
	statuses, err := s.ListStatuses(ctx, 1, commitID)
	require.NoError(t, err)
	require.Len(t, statuses, 2)
	assert.Equal(t, "lint", statuses[0].Context)
	assert.Equal(t, "opencodereview", statuses[1].Context)

	_, err = s.GetStatus(ctx, 1, "0000000000000000000000000000000000000000", "opencodereview")
	assert.True(t, IsErrExternalReviewStatusNotExist(err), "got %v", err)

	for _, opts := range []SetStatusOptions{
		{CommitID: commitID, Context: "opencodereview", State: "success"},
		{CommitID: commitID, Context: "", State: ExternalReviewStatePassed},
		{CommitID: commitID, Context: "opencodereview", State: ExternalReviewStatePassed, TargetURL: "javascript:alert(1)"},
		{CommitID: commitID, Context: "opencodereview", State: ExternalReviewStatePassed, Description: strings.Repeat("a", MaxExternalReviewDescriptionLength+1)},
	} {
		_, err = s.SetStatus(ctx, 1, opts)
		assert.Error(t, err, "%+v", opts)
	}
	got, err = s.GetStatus(ctx, 1, commitID, "opencodereview")
	require.NoError(t, err)
	assert.Equal(t, ExternalReviewStatePassed, got.State)
}

func externalReviewsCheck(t *testing.T, ctx context.Context, s *ExternalReviewsStore) {
	const commitID = "d3c6e4a1f0c2b8e7a9d5f4c3b2a1908e7d6c5b4a"

	check, err := s.Check(ctx, 1, "main", commitID)
	require.NoError(t, err)
	assert.Nil(t, check, "a branch without a rule has no review")

	require.NoError(t, s.SaveRule(ctx, 1, "main", ExternalReviewModeRequired, "opencodereview"))
	require.NoError(t, s.SaveRule(ctx, 1, "develop", ExternalReviewModeAdvisory, "opencodereview"))

	check, err = s.Check(ctx, 1, "main", commitID)
	require.NoError(t, err)
	require.NotNil(t, check)
	assert.Nil(t, check.Status)
	assert.True(t, check.BlocksMerge(), "a commit without a result is not reviewed")

	for _, tc := range []struct {
		state ExternalReviewState
		want  bool
	}{
		{ExternalReviewStatePending, true},
		{ExternalReviewStateFailed, true},
		{ExternalReviewStatePassed, false},
	} {
		_, err = s.SetStatus(ctx, 1, SetStatusOptions{CommitID: commitID, Context: "opencodereview", State: tc.state, CreatorID: 1})
		require.NoError(t, err)
		check, err = s.Check(ctx, 1, "main", commitID)
		require.NoError(t, err)
		require.NotNil(t, check.Status)
		assert.Equal(t, tc.state, check.Status.State)
		assert.Equal(t, tc.want, check.BlocksMerge(), "state %q", tc.state)
	}

	// A result of another tool does not count.
	require.NoError(t, s.SaveRule(ctx, 1, "main", ExternalReviewModeRequired, "review-bot"))
	check, err = s.Check(ctx, 1, "main", commitID)
	require.NoError(t, err)
	assert.Nil(t, check.Status)
	assert.True(t, check.BlocksMerge())

	// An advisory review never blocks merging.
	_, err = s.SetStatus(ctx, 1, SetStatusOptions{CommitID: commitID, Context: "opencodereview", State: ExternalReviewStateFailed, CreatorID: 1})
	require.NoError(t, err)
	check, err = s.Check(ctx, 1, "develop", commitID)
	require.NoError(t, err)
	assert.Equal(t, ExternalReviewStateFailed, check.Status.State)
	assert.False(t, check.BlocksMerge())
}
