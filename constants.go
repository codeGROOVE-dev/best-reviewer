package main

import "time"

// Selection methods for reviewer choice tracking.
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

// Configuration constants.
const (
	httpTimeout       = 120 // seconds
	maxRetries        = 3
	retryDelay        = 2                   // seconds
	nearbyLines       = 3                   // lines within this distance count as "nearby"
	maxFilesToAnalyze = 3                   // Focus on 3 files with largest delta to reduce API calls
	maxHistoricalPRs  = 2                   // Limit to 2 PRs per file to reduce API calls
	maxRecentPRs      = 50                  // Increased to get better candidate pool
	cacheTTL          = 24 * time.Hour      // Default cache TTL for most items
	prCacheTTL        = 20 * 24 * time.Hour // Cache PRs for 20 days (use updated_at to invalidate)
	searchCacheTTL    = 15 * time.Minute    // Cache search results for 15 minutes

	// Specific cache TTLs for different data types
	userTypeCacheTTL         = 30 * 24 * time.Hour // User type never changes
	repoContributorsCacheTTL = 4 * time.Hour       // 4 hours - catch people returning from vacation
	directoryOwnersCacheTTL  = 3 * 24 * time.Hour  // Directory ownership changes slowly
	recentPRsCacheTTL        = 1 * time.Hour       // Recent PRs for active repos
	fileHistoryCacheTTL      = 3 * 24 * time.Hour  // File history changes slowly

	// API and pagination limits.
	perPageLimit = 100 // GitHub API per_page limit

	// Analysis parameters.
	topReviewersLimit = 10 // Number of top reviewers to find
	progressBarWidth  = 40 // Width of progress bar in characters

	// Overlap scoring parameters.
	overlapDecayDays  = 30.0 // Days for recency weight decay
	nearbyMatchWeight = 0.5  // Weight for nearby line matches

	// Recency window parameters (in days).
	recencyWindow1 = 4  // First window: 4 days
	recencyWindow2 = 8  // Second window: 8 days
	recencyWindow3 = 16 // Third window: 16 days
	recencyWindow4 = 32 // Fourth window: 32 days
	selectionRolls = 10 // Number of random rolls for weighted selection

	// Scoring parameters.
	topCandidatesToLog = 5   // Number of top candidates to log
	maxContextScore    = 100 // Maximum context score for candidates

	// Scoring weights (must sum to 100)
	fileOverlapWeight = 40.0 // Weight for file overlap score
	recencyWeight     = 35.0 // Weight for recency score
	expertiseWeight   = 25.0 // Weight for domain expertise score

	// File significance multipliers
	prodCodeMultiplier     = 1.5 // Production code vs test code
	criticalFileMultiplier = 1.3 // Main.go, handlers, etc.
	refactoringMultiplier  = 1.2 // More deletions than additions

	// Retry parameters handled by external library

	// PR URL parsing.
	minURLParts = 4 // Minimum parts in PR URL

	// GraphQL constants.
	graphQLNodes = "nodes" // Common GraphQL field name
)
