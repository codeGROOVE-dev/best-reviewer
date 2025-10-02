// Package reviewer provides intelligent reviewer selection for pull requests.
package reviewer

import "time"

// Selection methods for reviewer choice tracking.
const (
	SelectionAssignee          = "assignee-expert"
	SelectionAuthorOverlap     = "author-overlap"
	SelectionAuthorDirectory   = "author-directory"
	SelectionAuthorProject     = "author-project"
	SelectionReviewerCommenter = "reviewer-commenter"
	SelectionReviewerOverlap   = "reviewer-overlap"
	SelectionReviewerDirectory = "reviewer-directory"
	SelectionReviewerProject   = "reviewer-project"
	SelectionTopContributor    = "top-contributor"
	SelectionCodeOwner         = "codeowner"
	SelectionFileAuthor        = "file-author"
	SelectionFileReviewer      = "file-reviewer"
	SelectionFullAnalysis      = "full-analysis"
)

// Configuration constants.
const (
	nearbyLines       = 3                   // lines within this distance count as "nearby"
	maxFilesToAnalyze = 3                   // Focus on 3 files with largest delta to reduce API calls
	maxHistoricalPRs  = 2                   // Limit to 2 PRs per file to reduce API calls
	cacheTTL          = 24 * time.Hour      // Default cache TTL for most items
	prCacheTTL        = 20 * 24 * time.Hour // Cache PRs for 20 days (use updated_at to invalidate)

	// Specific cache TTLs for different data types.
	repoContributorsCacheTTL = 4 * time.Hour      // 4 hours - catch people returning from vacation
	directoryOwnersCacheTTL  = 3 * 24 * time.Hour // Directory ownership changes slowly
	fileHistoryCacheTTL      = 3 * 24 * time.Hour // File history changes slowly
	prCountCacheTTL          = 6 * time.Hour      // PR count for workload balancing (default).

	// API and pagination limits.
	perPageLimit = 100 // GitHub API per_page limit

	// Analysis parameters.
	topReviewersLimit = 10 // Number of top reviewers to find

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

	// Scoring weights (must sum to 100).
	fileOverlapWeight = 40.0 // Weight for file overlap score
	recencyWeight     = 35.0 // Weight for recency score
	expertiseWeight   = 25.0 // Weight for domain expertise score

	// File significance multipliers.
	prodCodeMultiplier     = 1.5 // Production code vs test code
	criticalFileMultiplier = 1.3 // Main.go, handlers, etc.
	refactoringMultiplier  = 1.2 // More deletions than additions

	// HTTP constants.
	httpMethodGet = "GET" // HTTP GET method

	// Path constants.
	pathSeparator = "/"

	// Scoring constants.
	recentActivityScore      = 0.9  // Score for very recent activity (< 3 days)
	weekActivityScore        = 0.7  // Score for weekly activity (< 7 days)
	biweeklyActivityScore    = 0.5  // Score for biweekly activity (< 14 days)
	monthlyActivityScore     = 0.25 // Score for monthly activity (< 30 days)
	bimonthlyActivityScore   = 0.1  // Score for bimonthly activity (< 60 days)
	quarterlyActivityScore   = 0.05 // Score for quarterly activity (< 90 days)
	defaultExpertiseScore    = 0.5  // Default expertise score
	reviewerWeightMultiplier = 0.5  // Weight multiplier for reviewers vs authors

	// Overlap scoring constants.
	contextMatchWeight  = 0.7 // Weight for context matches in overlap scoring
	minOverlapThreshold = 5.0 // Minimum overlap score threshold

	// Analysis limits.
	maxRecentCommits      = 10 // Maximum recent commits to analyze
	maxDirectoryReviewers = 5  // Maximum directory reviewers to return

	// Batch processing sizes.
	defaultBatchSize = 20 // Default batch size for processing
	smallBatchSize   = 10 // Small batch size fallback
	minBatchSize     = 5  // Minimum batch size

	// Selection method scoring.
	overlapAuthorScore     = 30 // Score for author overlap
	overlapReviewerScore   = 25 // Score for reviewer overlap
	fileAuthorScore        = 15 // Score for file author
	fileReviewerScore      = 12 // Score for file reviewer
	directoryAuthorScore   = 7  // Score for directory author
	directoryReviewerScore = 5  // Score for directory reviewer

	// Time-based constants.
	recentDaysThreshold    = 7  // Days threshold for recent activity
	biweeklyDaysThreshold  = 14 // Days threshold for biweekly activity
	monthlyDaysThreshold   = 30 // Days threshold for monthly activity
	bimonthlyDaysThreshold = 60 // Days threshold for bimonthly activity
	quarterlyDaysThreshold = 90 // Days threshold for quarterly activity

	// GraphQL constants.
	graphQLNodes = "nodes" // Common GraphQL field name
)
