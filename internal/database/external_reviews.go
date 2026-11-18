package database

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/cockroachdb/errors"
	"gorm.io/gorm"
	"unknwon.dev/x/logx"

	"gogs.io/gogs/internal/conf"
	"gogs.io/gogs/internal/errx"
	"gogs.io/gogs/internal/git"
	"gogs.io/gogs/internal/lazyregexp"
	apiv1types "gogs.io/gogs/internal/route/api/v1/types"
)

// ExternalReviewMode is how the result of an external review affects merging
// pull requests into a branch.
type ExternalReviewMode string

const (
	// ExternalReviewModeAdvisory shows the result and keeps merging available.
	ExternalReviewModeAdvisory ExternalReviewMode = "advisory"
	// ExternalReviewModeRequired allows merging only after the head commit
	// passes the review.
	ExternalReviewModeRequired ExternalReviewMode = "required"
)

// IsValid returns true if the mode is one of the known modes.
func (m ExternalReviewMode) IsValid() bool {
	return m == ExternalReviewModeAdvisory || m == ExternalReviewModeRequired
}

// ExternalReviewState is the result of an external review for a commit.
type ExternalReviewState string

const (
	ExternalReviewStatePending ExternalReviewState = "pending"
	ExternalReviewStatePassed  ExternalReviewState = "passed"
	ExternalReviewStateFailed  ExternalReviewState = "failed"
)

// IsValid returns true if the state is one of the known states.
func (s ExternalReviewState) IsValid() bool {
	return s == ExternalReviewStatePending || s == ExternalReviewStatePassed || s == ExternalReviewStateFailed
}

const (
	// MaxExternalReviewContextLength is the maximum length of the name of an
	// external review tool.
	MaxExternalReviewContextLength = 255
	// MaxExternalReviewDescriptionLength is the maximum length of the
	// description of an external review result.
	MaxExternalReviewDescriptionLength = 255
	// MaxExternalReviewTargetURLLength is the maximum length of the link to
	// the report of an external review.
	MaxExternalReviewTargetURLLength = 2048
)

var externalReviewContextPattern = lazyregexp.New(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

// IsValidExternalReviewContext returns true if the name of an external review
// tool is not empty, not too long, and consists of letters, digits, ".", "_",
// "/" and "-" that start with a letter or a digit.
func IsValidExternalReviewContext(context string) bool {
	return len(context) <= MaxExternalReviewContextLength && externalReviewContextPattern.MatchString(context)
}

// IsValidExternalReviewTargetURL returns true if the link to the report of an
// external review is empty, or an absolute HTTP or HTTPS URL that is not too
// long. Other schemes, e.g., "javascript:", are rejected because the link is
// rendered on pull request pages.
func IsValidExternalReviewTargetURL(targetURL string) bool {
	if targetURL == "" {
		return true
	}
	if len(targetURL) > MaxExternalReviewTargetURLLength {
		return false
	}
	u, err := url.Parse(targetURL)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// ExternalReviewRule names the external review tool that reviews pull requests
// targeting a branch of a repository, and how its result affects merging them.
type ExternalReviewRule struct {
	ID     int64              `gorm:"primaryKey"`
	RepoID int64              `gorm:"uniqueIndex:external_review_rule_repo_branch_unique;not null"`
	Branch string             `gorm:"size:255;uniqueIndex:external_review_rule_repo_branch_unique;not null"`
	Mode   ExternalReviewMode `gorm:"size:16;not null"`
	// Context is the name of the review tool, which reports its results with
	// the same name.
	Context     string `gorm:"size:255;not null"`
	CreatedUnix int64
	UpdatedUnix int64
}

// BeforeCreate implements the GORM create hook.
func (r *ExternalReviewRule) BeforeCreate(tx *gorm.DB) error {
	if r.CreatedUnix == 0 {
		r.CreatedUnix = tx.NowFunc().Unix()
		r.UpdatedUnix = r.CreatedUnix
	}
	return nil
}

// ExternalReviewStatus is the result that an external review tool reported for
// a commit of a repository.
type ExternalReviewStatus struct {
	ID          int64               `gorm:"primaryKey"`
	RepoID      int64               `gorm:"uniqueIndex:external_review_status_repo_commit_context_unique;not null"`
	CommitID    string              `gorm:"size:64;uniqueIndex:external_review_status_repo_commit_context_unique;not null"`
	Context     string              `gorm:"size:255;uniqueIndex:external_review_status_repo_commit_context_unique;not null"`
	State       ExternalReviewState `gorm:"size:16;not null"`
	TargetURL   string              `gorm:"type:TEXT"`
	Description string              `gorm:"size:255"`
	// CreatorID is the user who reported the result last, or who requested
	// the review for a pending result that Gogs created.
	CreatorID int64

	Created     time.Time `gorm:"-" json:"-"`
	CreatedUnix int64
	Updated     time.Time `gorm:"-" json:"-"`
	UpdatedUnix int64
}

// BeforeCreate implements the GORM create hook.
func (s *ExternalReviewStatus) BeforeCreate(tx *gorm.DB) error {
	if s.CreatedUnix == 0 {
		s.CreatedUnix = tx.NowFunc().Unix()
		s.UpdatedUnix = s.CreatedUnix
	}
	return nil
}

// AfterFind implements the GORM query hook.
func (s *ExternalReviewStatus) AfterFind(_ *gorm.DB) error {
	s.Created = time.Unix(s.CreatedUnix, 0).Local()
	s.Updated = time.Unix(s.UpdatedUnix, 0).Local()
	return nil
}

var _ errx.NotFound = (*ErrExternalReviewRuleNotExist)(nil)

type ErrExternalReviewRuleNotExist struct {
	args map[string]any
}

func IsErrExternalReviewRuleNotExist(err error) bool {
	_, ok := err.(ErrExternalReviewRuleNotExist)
	return ok
}

func (err ErrExternalReviewRuleNotExist) Error() string {
	return fmt.Sprintf("external review rule does not exist: %v", err.args)
}

func (ErrExternalReviewRuleNotExist) NotFound() bool {
	return true
}

var _ errx.NotFound = (*ErrExternalReviewStatusNotExist)(nil)

type ErrExternalReviewStatusNotExist struct {
	args map[string]any
}

func IsErrExternalReviewStatusNotExist(err error) bool {
	_, ok := err.(ErrExternalReviewStatusNotExist)
	return ok
}

func (err ErrExternalReviewStatusNotExist) Error() string {
	return fmt.Sprintf("external review status does not exist: %v", err.args)
}

func (ErrExternalReviewStatusNotExist) NotFound() bool {
	return true
}

// ErrExternalReviewRequired is returned when merging a pull request whose head
// commit has not passed the review that its base branch requires.
type ErrExternalReviewRequired struct {
	Context  string
	CommitID string
}

func IsErrExternalReviewRequired(err error) bool {
	return errors.HasType(err, ErrExternalReviewRequired{})
}

func (err ErrExternalReviewRequired) Error() string {
	return fmt.Sprintf("commit %s has not passed the required external review %q", err.CommitID, err.Context)
}

// ExternalReviewCheck is the external review of the head commit of a pull
// request, by the rule of its base branch.
type ExternalReviewCheck struct {
	Rule *ExternalReviewRule
	// Status is nil when the review tool has reported no result for the
	// commit.
	Status *ExternalReviewStatus
}

// BlocksMerge returns true if the rule requires a passing review, and the
// commit has not passed it.
func (c *ExternalReviewCheck) BlocksMerge() bool {
	return c.Rule.Mode == ExternalReviewModeRequired &&
		(c.Status == nil || c.Status.State != ExternalReviewStatePassed)
}

// ExternalReviewsStore is the storage layer for external reviews of pull
// requests.
type ExternalReviewsStore struct {
	db *gorm.DB
}

func newExternalReviewsStore(db *gorm.DB) *ExternalReviewsStore {
	return &ExternalReviewsStore{db: db}
}

// GetRule returns the rule of the branch with given name in the repository. It
// returns ErrExternalReviewRuleNotExist when the branch has none.
func (s *ExternalReviewsStore) GetRule(ctx context.Context, repoID int64, branch string) (*ExternalReviewRule, error) {
	rule := new(ExternalReviewRule)
	err := s.db.WithContext(ctx).Where("repo_id = ? AND branch = ?", repoID, branch).First(rule).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrExternalReviewRuleNotExist{args: map[string]any{"repoID": repoID, "branch": branch}}
		}
		return nil, err
	}
	return rule, nil
}

// ListRules returns the rules of the repository, ordered by branch name.
func (s *ExternalReviewsStore) ListRules(ctx context.Context, repoID int64) ([]*ExternalReviewRule, error) {
	rules := make([]*ExternalReviewRule, 0, 2)
	return rules, s.db.WithContext(ctx).Where("repo_id = ?", repoID).Order("branch ASC").Find(&rules).Error
}

// SaveRule creates or replaces the rule of the branch with given name in the
// repository.
func (s *ExternalReviewsStore) SaveRule(ctx context.Context, repoID int64, branch string, mode ExternalReviewMode, reviewContext string) error {
	if !mode.IsValid() {
		return errors.Newf("invalid external review mode %q", mode)
	} else if !IsValidExternalReviewContext(reviewContext) {
		return errors.Newf("invalid external review context %q", reviewContext)
	}

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rule := new(ExternalReviewRule)
		err := tx.Where("repo_id = ? AND branch = ?", repoID, branch).First(rule).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return tx.Create(&ExternalReviewRule{
				RepoID:  repoID,
				Branch:  branch,
				Mode:    mode,
				Context: reviewContext,
			}).Error
		} else if err != nil {
			return errors.Wrap(err, "get rule")
		}
		return tx.Model(rule).Updates(map[string]any{
			"mode":         mode,
			"context":      reviewContext,
			"updated_unix": tx.NowFunc().Unix(),
		}).Error
	})
}

// DeleteRule deletes the rule of the branch with given name in the repository,
// if any.
func (s *ExternalReviewsStore) DeleteRule(ctx context.Context, repoID int64, branch string) error {
	return s.db.WithContext(ctx).Where("repo_id = ? AND branch = ?", repoID, branch).Delete(new(ExternalReviewRule)).Error
}

// GetStatus returns the result that the review tool with given context
// reported for the commit of the repository. It returns
// ErrExternalReviewStatusNotExist when there is none.
func (s *ExternalReviewsStore) GetStatus(ctx context.Context, repoID int64, commitID, reviewContext string) (*ExternalReviewStatus, error) {
	status := new(ExternalReviewStatus)
	err := s.db.WithContext(ctx).
		Where("repo_id = ? AND commit_id = ? AND context = ?", repoID, commitID, reviewContext).
		First(status).
		Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrExternalReviewStatusNotExist{args: map[string]any{"repoID": repoID, "commitID": commitID, "context": reviewContext}}
		}
		return nil, err
	}
	return status, nil
}

// ListStatuses returns the results that review tools reported for the commit
// of the repository, ordered by context.
func (s *ExternalReviewsStore) ListStatuses(ctx context.Context, repoID int64, commitID string) ([]*ExternalReviewStatus, error) {
	statuses := make([]*ExternalReviewStatus, 0, 1)
	return statuses, s.db.WithContext(ctx).
		Where("repo_id = ? AND commit_id = ?", repoID, commitID).
		Order("context ASC").
		Find(&statuses).
		Error
}

// SetStatusOptions is the result of an external review for a commit.
type SetStatusOptions struct {
	CommitID    string
	Context     string
	State       ExternalReviewState
	TargetURL   string
	Description string
	CreatorID   int64
}

// SetStatus creates or replaces the result that the review tool reported for
// the commit of the repository.
func (s *ExternalReviewsStore) SetStatus(ctx context.Context, repoID int64, opts SetStatusOptions) (*ExternalReviewStatus, error) {
	switch {
	case !opts.State.IsValid():
		return nil, errors.Newf("invalid external review state %q", opts.State)
	case !IsValidExternalReviewContext(opts.Context):
		return nil, errors.Newf("invalid external review context %q", opts.Context)
	case !IsValidExternalReviewTargetURL(opts.TargetURL):
		return nil, errors.Newf("invalid external review target URL %q", opts.TargetURL)
	case len(opts.Description) > MaxExternalReviewDescriptionLength:
		return nil, errors.New("external review description is too long")
	}

	status := new(ExternalReviewStatus)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Where("repo_id = ? AND commit_id = ? AND context = ?", repoID, opts.CommitID, opts.Context).First(status).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			status = &ExternalReviewStatus{
				RepoID:      repoID,
				CommitID:    opts.CommitID,
				Context:     opts.Context,
				State:       opts.State,
				TargetURL:   opts.TargetURL,
				Description: opts.Description,
				CreatorID:   opts.CreatorID,
			}
			return tx.Create(status).Error
		} else if err != nil {
			return errors.Wrap(err, "get status")
		}

		status.State = opts.State
		status.TargetURL = opts.TargetURL
		status.Description = opts.Description
		status.CreatorID = opts.CreatorID
		status.UpdatedUnix = tx.NowFunc().Unix()
		return tx.Model(status).
			Select("state", "target_url", "description", "creator_id", "updated_unix").
			Updates(status).
			Error
	})
	if err != nil {
		return nil, err
	}
	_ = status.AfterFind(nil)
	return status, nil
}

// Check returns the external review of the commit by the rule of the branch
// with given name in the repository. It returns nil when the branch has no
// rule.
func (s *ExternalReviewsStore) Check(ctx context.Context, repoID int64, branch, commitID string) (*ExternalReviewCheck, error) {
	rule, err := s.GetRule(ctx, repoID, branch)
	if err != nil {
		if IsErrExternalReviewRuleNotExist(err) {
			return nil, nil
		}
		return nil, errors.Wrap(err, "get rule")
	}

	check := &ExternalReviewCheck{Rule: rule}
	check.Status, err = s.GetStatus(ctx, repoID, commitID, rule.Context)
	if err != nil && !IsErrExternalReviewStatusNotExist(err) {
		return nil, errors.Wrap(err, "get status")
	}
	return check, nil
}

// ExternalReviewStatusURL returns the API URL that review tools report their
// results for the commit of the repository to.
func ExternalReviewStatusURL(repo *Repository, commitID string) string {
	return conf.Server.ExternalURL + "api/v1/repos/" + repo.FullName() + "/reviews/" + commitID
}

// RequestReview asks the review tool that the rule of the base branch of the
// pull request names to review the head commit, which the base repository
// must already have as "refs/pull/<index>/head". The commit is marked pending
// unless the tool has reported a result for it already. It does nothing when
// the base branch has no rule.
//
// The pull request must have its issue, the repository of the issue, and its
// base repository loaded.
func (s *ExternalReviewsStore) RequestReview(ctx context.Context, logger *logx.Logger, pr *PullRequest, doer *User) error {
	rule, err := s.GetRule(ctx, pr.BaseRepoID, pr.BaseBranch)
	if err != nil {
		if IsErrExternalReviewRuleNotExist(err) {
			return nil
		}
		return errors.Wrap(err, "get rule")
	}

	baseGitRepo, err := git.Open(pr.BaseRepo.RepoPath())
	if err != nil {
		return errors.Wrap(err, "open base repository")
	}
	commitID, err := baseGitRepo.RevParse(ctx, fmt.Sprintf("refs/pull/%d/head", pr.Index))
	if err != nil {
		return errors.Wrap(err, "get head commit")
	}

	_, err = s.GetStatus(ctx, pr.BaseRepoID, commitID, rule.Context)
	if IsErrExternalReviewStatusNotExist(err) {
		_, err = s.SetStatus(ctx, pr.BaseRepoID, SetStatusOptions{
			CommitID:  commitID,
			Context:   rule.Context,
			State:     ExternalReviewStatePending,
			CreatorID: doer.ID,
		})
		if err != nil {
			return errors.Wrap(err, "set pending status")
		}
	} else if err != nil {
		return errors.Wrap(err, "get status")
	}

	return newWebhooksStore(s.db).Prepare(ctx, logger, pr.Issue.Repo, HookEventTypeExternalReview, &apiv1types.WebhookExternalReviewPayload{
		Action:      apiv1types.WebhookExternalReviewRequested,
		Context:     rule.Context,
		Mode:        string(rule.Mode),
		SHA:         commitID,
		StatusURL:   ExternalReviewStatusURL(pr.BaseRepo, commitID),
		Index:       pr.Index,
		PullRequest: pr.APIFormat(logger),
		Repository:  pr.Issue.Repo.APIFormatLegacy(logger, nil),
		Sender:      doer.APIFormat(),
	})
}
