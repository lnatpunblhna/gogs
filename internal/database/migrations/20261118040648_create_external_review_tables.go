package migrations

import (
	"gorm.io/gorm"
)

// createExternalReviewTables creates the tables of the rules that enable
// external reviews for target branches of pull requests, and of the results
// that review tools report for commits.
func createExternalReviewTables(db *gorm.DB) error {
	type externalReviewRule struct {
		ID          int64  `gorm:"primaryKey"`
		RepoID      int64  `gorm:"uniqueIndex:external_review_rule_repo_branch_unique;not null"`
		Branch      string `gorm:"size:255;uniqueIndex:external_review_rule_repo_branch_unique;not null"`
		Mode        string `gorm:"size:16;not null"`
		Context     string `gorm:"size:255;not null"`
		CreatedUnix int64
		UpdatedUnix int64
	}
	type externalReviewStatus struct {
		ID          int64  `gorm:"primaryKey"`
		RepoID      int64  `gorm:"uniqueIndex:external_review_status_repo_commit_context_unique;not null"`
		CommitID    string `gorm:"size:64;uniqueIndex:external_review_status_repo_commit_context_unique;not null"`
		Context     string `gorm:"size:255;uniqueIndex:external_review_status_repo_commit_context_unique;not null"`
		State       string `gorm:"size:16;not null"`
		TargetURL   string `gorm:"type:TEXT"`
		Description string `gorm:"size:255"`
		CreatorID   int64
		CreatedUnix int64
		UpdatedUnix int64
	}

	return db.Transaction(func(tx *gorm.DB) error {
		for _, table := range []any{&externalReviewRule{}, &externalReviewStatus{}} {
			if tx.Migrator().HasTable(table) {
				continue
			}
			if err := tx.Migrator().CreateTable(table); err != nil {
				return err
			}
		}
		return nil
	})
}
