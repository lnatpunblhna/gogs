package types

import "time"

// ExternalReviewStatus is the result of an external review tool for a commit.
type ExternalReviewStatus struct {
	SHA     string `json:"sha"`
	Context string `json:"context"`
	// State is one of "pending", "passed" and "failed".
	State       string    `json:"state"`
	TargetURL   string    `json:"target_url"`
	Description string    `json:"description"`
	Creator     *User     `json:"creator"`
	Created     time.Time `json:"created_at"`
	Updated     time.Time `json:"updated_at"`
}
