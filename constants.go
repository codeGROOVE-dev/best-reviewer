package main

import "time"

// Selection methods for reviewer choice tracking
const (
	selectionAssignee          = "assignee-expert"
	selectionAuthorOverlap     = "author-overlap"
	selectionAuthorDirectory   = "author-directory" 
	selectionAuthorProject     = "author-project"
	selectionReviewerCommenter = "reviewer-commenter"
	selectionReviewerOverlap   = "reviewer-overlap"
	selectionReviewerDirectory = "reviewer-directory"
	selectionReviewerProject   = "reviewer-project"
)

// Configuration constants
const (
	httpTimeout       = 120 // seconds
	maxRetries        = 3
	retryDelay        = 2 // seconds
	nearbyLines       = 3 // lines within this distance count as "nearby"
	maxFilesToAnalyze = 10
	maxHistoricalPRs  = 50 // With caching, we can afford more lookups
	maxRecentPRs      = 20 // Reasonable limit for recent PRs
	cacheTTL          = 15 * time.Minute // Cache results for 15 minutes
)