// Package reviewer provides intelligent reviewer selection for pull requests.
package reviewer

import "time"

// Configuration constants.
const (
	cacheTTL           = 24 * time.Hour // Default cache TTL for in-memory cache
	topCandidatesToLog = 5              // Number of top candidates to log
	maxContextScore    = 100            // Maximum context score for candidates
)
