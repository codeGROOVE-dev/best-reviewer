// Package types contains shared data structures used across the reviewer system.
//
//nolint:revive // "types" is a standard Go package name for shared data structures
package types

import "time"

// PullRequest represents a GitHub pull request.
type PullRequest struct {
	CreatedAt    time.Time
	UpdatedAt    time.Time
	LastCommit   time.Time
	LastReview   time.Time
	Title        string
	State        string
	Author       string
	Repository   string
	Owner        string
	TestState    string // "passing", "failing", "pending", "queued", "running", or ""
	ChangedFiles []ChangedFile
	Assignees    []string
	Reviewers    []string
	Number       int
	Draft        bool
}

// ChangedFile represents a file changed in a pull request.
type ChangedFile struct {
	Filename  string
	Status    string // "added", "modified", "removed", "renamed"
	Patch     string
	Additions int
	Deletions int
}

// ReviewerCandidate represents a potential reviewer with scoring metadata.
type ReviewerCandidate struct {
	LastActivity      time.Time
	Username          string
	SelectionMethod   string
	AuthorAssociation string
	ContextScore      int
	ActivityScore     int
}

// PRInfo holds basic PR information for historical analysis.
type PRInfo struct {
	MergedAt  time.Time
	Author    string
	MergedBy  string
	Reviewers []string
	Number    int
	Merged    bool
	LineCount int // Number of lines this PR touched (for blame-based weighting)
}

// UserActivity tracks a user's last activity in a repository.
type UserActivity struct {
	LastActivity time.Time
	Username     string
	Source       string // "commit", "pr_author", "pr_reviewer"
}
