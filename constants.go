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
	httpTimeout       = 30                  // seconds - reduced for security
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
	prCountFailureCacheTTL   = 10 * time.Minute   // Cache failures to avoid repeated API calls.
	prStaleDaysThreshold     = 90                 // PRs older than this are considered stale.
	maxTokenLength           = 100                // Maximum expected length for GitHub tokens.
	minTokenLength           = 40                 // Minimum expected length for GitHub tokens.
	classicTokenLength       = 40                 // Length of classic GitHub tokens.
	maxAppID                 = 999999999          // Maximum valid GitHub App ID.
	filePermSecure           = 0o077              // Mask for checking secure file permissions.
	maxGraphQLVarLength      = 1000               // Maximum length for GraphQL variable strings.
	maxGraphQLVarNum         = 1000000            // Maximum numeric value for GraphQL variables.
	maxURLLength             = 500                // Maximum URL length to validate.
	maxPRNumber              = 999999             // Maximum PR number to validate.
	maxGitHubNameLength      = 100                // Maximum length for GitHub owner/repo names.

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

	// Retry parameters for exponential backoff with jitter.
	maxRetryAttempts  = 25              // Maximum retry attempts for API calls.
	initialRetryDelay = 1 * time.Second // Initial delay for retry attempts.
	maxRetryDelay     = 2 * time.Minute // Maximum delay cap (2 minutes as per requirement).

	// PR URL parsing.
	minURLParts = 4 // Minimum parts in PR URL

	// GraphQL constants.
	graphQLNodes = "nodes" // Common GraphQL field name

	// HTTP constants.
	httpMethodGet     = "GET" // HTTP GET method
	serverReadTimeout = 10    // seconds - server read timeout
	serverIdleTimeout = 60    // seconds - server idle timeout

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
)
