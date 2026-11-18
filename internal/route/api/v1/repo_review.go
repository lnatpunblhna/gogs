package v1

import (
	"net/http"
	"strings"

	"github.com/cockroachdb/errors"

	"gogs.io/gogs/internal/database"
	"gogs.io/gogs/internal/git"
	"gogs.io/gogs/internal/lazyregexp"
	"gogs.io/gogs/internal/route/api/v1/types"
)

var fullCommitIDPattern = lazyregexp.New(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// reviewCommitID returns the full ID of the commit in the request path, after
// checking that the repository has it. Only full IDs are accepted, so that a
// result is never reported for another commit than the review tool meant. It
// writes the response and returns false otherwise.
func reviewCommitID(c *apiContext) (string, bool) {
	commitID := strings.ToLower(c.Param("sha"))
	if !fullCommitIDPattern.MatchString(commitID) {
		c.ErrorStatus(http.StatusUnprocessableEntity, errors.New("The SHA must be the full ID of a commit."))
		return "", false
	}

	gitRepo, err := git.Open(c.Repo.Repository.RepoPath())
	if err != nil {
		c.Error(err, "open repository")
		return "", false
	}
	if _, err = gitRepo.CatFileCommit(c.Request().Context(), commitID); err != nil {
		if git.IsErrRevisionNotExist(err) {
			c.NotFound()
			return "", false
		}
		c.Error(err, "get commit")
		return "", false
	}
	return commitID, true
}

func toExternalReviewStatus(c *apiContext, status *database.ExternalReviewStatus) (*types.ExternalReviewStatus, error) {
	creator, err := database.Handle.Users().GetByID(c.Request().Context(), status.CreatorID)
	if err != nil {
		if !database.IsErrUserNotExist(err) {
			return nil, errors.Wrap(err, "get creator")
		}
		creator = database.NewGhostUser()
	}
	return &types.ExternalReviewStatus{
		SHA:         status.CommitID,
		Context:     status.Context,
		State:       string(status.State),
		TargetURL:   status.TargetURL,
		Description: status.Description,
		Creator:     creator.APIFormat(),
		Created:     status.Created,
		Updated:     status.Updated,
	}, nil
}

// listExternalReviewStatuses lists the results that external review tools
// reported for a commit.
func listExternalReviewStatuses(c *apiContext) {
	commitID, ok := reviewCommitID(c)
	if !ok {
		return
	}

	statuses, err := database.Handle.ExternalReviews().ListStatuses(c.Request().Context(), c.Repo.Repository.ID, commitID)
	if err != nil {
		c.Error(err, "list external review statuses")
		return
	}
	apiStatuses := make([]*types.ExternalReviewStatus, 0, len(statuses))
	for _, status := range statuses {
		apiStatus, err := toExternalReviewStatus(c, status)
		if err != nil {
			c.Error(err, "convert external review status")
			return
		}
		apiStatuses = append(apiStatuses, apiStatus)
	}
	c.JSONSuccess(apiStatuses)
}

type setExternalReviewStatusRequest struct {
	// Context is the name of the review tool, which must match the one that
	// the rule of the target branch names for the result to count.
	Context     string `json:"context" binding:"Required;MaxSize(255)"`
	State       string `json:"state" binding:"Required;In(pending,passed,failed)"`
	TargetURL   string `json:"target_url"`
	Description string `json:"description" binding:"MaxSize(255)"`
}

// setExternalReviewStatus creates or replaces the result that an external
// review tool reports for a commit.
func setExternalReviewStatus(c *apiContext, form setExternalReviewStatusRequest) {
	if !database.IsValidExternalReviewContext(form.Context) {
		c.ErrorStatus(http.StatusUnprocessableEntity, errors.New(`The context must consist of letters, digits, ".", "_", "/" and "-", and start with a letter or a digit.`))
		return
	} else if !database.IsValidExternalReviewTargetURL(form.TargetURL) {
		c.ErrorStatus(http.StatusUnprocessableEntity, errors.New("The target URL must be an absolute HTTP or HTTPS URL."))
		return
	}

	commitID, ok := reviewCommitID(c)
	if !ok {
		return
	}

	status, err := database.Handle.ExternalReviews().SetStatus(c.Request().Context(), c.Repo.Repository.ID, database.SetStatusOptions{
		CommitID:    commitID,
		Context:     form.Context,
		State:       database.ExternalReviewState(form.State),
		TargetURL:   form.TargetURL,
		Description: form.Description,
		CreatorID:   c.User.ID,
	})
	if err != nil {
		c.Error(err, "set external review status")
		return
	}
	apiStatus, err := toExternalReviewStatus(c, status)
	if err != nil {
		c.Error(err, "convert external review status")
		return
	}
	c.JSON(http.StatusCreated, apiStatus)
}
