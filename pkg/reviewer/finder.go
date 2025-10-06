package reviewer

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/codeGROOVE-dev/best-reviewer/pkg/cache"
	"github.com/codeGROOVE-dev/best-reviewer/pkg/github"
	"github.com/codeGROOVE-dev/best-reviewer/pkg/types"
)

// Finder finds and selects reviewers for pull requests.
type Finder struct {
	client       *github.Client
	cache        *cache.Cache
	maxPRs       int
	prCountCache time.Duration
}

// Config holds configuration for the reviewer finder.
type Config struct {
	MaxPRs       int           // Maximum open PRs per reviewer
	PRCountCache time.Duration // Cache duration for PR counts
}

// New creates a new Finder with the given GitHub client and configuration.
func New(client *github.Client, cfg Config) *Finder {
	return &Finder{
		client:       client,
		cache:        cache.New(cacheTTL),
		maxPRs:       cfg.MaxPRs,
		prCountCache: cfg.PRCountCache,
	}
}

// Find finds the best reviewers for a pull request.
// Returns a list of reviewer candidates sorted by relevance.
func (f *Finder) Find(ctx context.Context, pr *types.PullRequest) ([]types.ReviewerCandidate, error) {
	if pr == nil {
		return nil, fmt.Errorf("pr cannot be nil")
	}

	slog.Info("Finding reviewers for PR", "pr", pr.Number, "owner", pr.Owner, "repo", pr.Repository)

	// Check if project has only 0-2 members with write access for early short-circuit
	smallTeamMembers, totalMembers, err := f.checkSmallTeamProject(ctx, pr)
	if err != nil {
		slog.Warn("Failed to check small team project (continuing)", "error", err)
	} else if totalMembers >= 0 && totalMembers <= 2 {
		// Short-circuit for small teams (0-2 valid members excluding PR author)
		if len(smallTeamMembers) == 0 {
			slog.Info("Project has no valid reviewers (single-person project or PR author is only member)")
			return nil, nil
		} else if len(smallTeamMembers) == 1 {
			slog.Info("Project has single member, assigning to them", "member", smallTeamMembers[0])
		} else {
			slog.Info("Project has 2 members, assigning both", "members", smallTeamMembers)
		}
		candidates := make([]types.ReviewerCandidate, len(smallTeamMembers))
		for i, member := range smallTeamMembers {
			candidates[i] = types.ReviewerCandidate{
				Username:        member,
				SelectionMethod: "small-team",
				ContextScore:    maxContextScore,
			}
		}
		return candidates, nil
	}

	// Find reviewers using scoring algorithm
	candidates := f.findReviewersOptimized(ctx, pr)
	slog.Info("Reviewer search complete", "count", len(candidates))
	return candidates, nil
}

// isValidReviewer checks if a user is a valid reviewer (only hard filters).
func (f *Finder) isValidReviewer(ctx context.Context, pr *types.PullRequest, username string) bool {
	// Check if user is a bot
	if f.client.IsUserBot(ctx, username) {
		slog.Info("Filtered (is bot)", "username", username)
		return false
	}

	// Check write access - this is the only hard filter since they can't approve without it
	hasAccess := f.client.HasWriteAccess(ctx, pr.Owner, pr.Repository, username)
	if !hasAccess {
		slog.Info("Filtered (no write access)", "username", username)
		return false
	}

	return true
}

// calculateWorkloadPenalty returns a score penalty based on PR count (not a hard filter).
func (f *Finder) calculateWorkloadPenalty(ctx context.Context, pr *types.PullRequest, username string) int {
	prCount, err := f.client.OpenPRCount(ctx, pr.Owner, username, f.prCountCache)
	if err != nil {
		slog.Debug("Could not check PR count, no penalty applied", "username", username, "org", pr.Owner, "error", err)
		return 0 // No penalty if we can't check
	}

	if prCount == 0 {
		return 0 // No penalty for no open PRs
	}

	// Apply progressive penalty: 1 point per PR, so it slightly deprioritizes busy reviewers
	// but never makes them ineligible. This is a soft signal, not a hard filter.
	penalty := prCount
	slog.Debug("Workload penalty applied", "username", username, "pr_count", prCount, "penalty", penalty)
	return penalty
}

// changedFiles returns the list of changed files, limiting to the most changed.
func (f *Finder) changedFiles(pr *types.PullRequest) []string {
	// Sort by total changes (additions + deletions)
	type fileChange struct {
		name    string
		changes int
	}

	fileChanges := make([]fileChange, 0, len(pr.ChangedFiles))
	for _, file := range pr.ChangedFiles {
		fileChanges = append(fileChanges, fileChange{
			name:    file.Filename,
			changes: file.Additions + file.Deletions,
		})
	}

	// Sort by changes (descending)
	sort.Slice(fileChanges, func(i, j int) bool {
		return fileChanges[i].changes > fileChanges[j].changes
	})

	// Extract filenames, limit to maxFilesToAnalyze
	files := make([]string, 0, len(fileChanges))
	for i, fc := range fileChanges {
		if i >= maxFilesToAnalyze {
			break
		}
		files = append(files, fc.name)
	}

	return files
}

// fetchAllPRFiles fetches all file patches for a PR.
func (f *Finder) fetchAllPRFiles(ctx context.Context, owner, repo string, prNumber int) (map[string]string, error) {
	// Check cache first
	cacheKey := makeCacheKey("pr-files", owner, repo, prNumber)
	if cached, found := f.cache.Get(cacheKey); found {
		if patches, ok := cached.(map[string]string); ok {
			slog.Info("  📦 Using cached PR file patches")
			return patches, nil
		}
	}

	slog.Info("Fetching all file patches for PR", "pr", prNumber)

	// Fetch changed files
	files, err := f.client.ChangedFiles(ctx, owner, repo, prNumber)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch changed files: %w", err)
	}

	// Build patch cache from changed files (patches are already included)
	patches := make(map[string]string)
	for _, file := range files {
		if file.Patch != "" {
			patches[file.Filename] = file.Patch
		}
	}

	// Cache the result
	f.cache.SetWithTTL(cacheKey, patches, fileHistoryCacheTTL)
	slog.Info("Fetched and cached file patches", "count", len(patches))

	return patches, nil
}

// checkSmallTeamProject checks if the project has only 0-2 members with write access.
// Returns (valid members, total count, error).
// Valid members excludes the PR author and bots. Total count is the number of valid members.
// Returns count=-1 if there are more than 2 valid members (no short-circuit needed).
func (f *Finder) checkSmallTeamProject(ctx context.Context, pr *types.PullRequest) ([]string, int, error) {
	type cachedResult struct {
		Members []string
		Count   int
	}

	cacheKey := makeCacheKey("small-team", pr.Owner, pr.Repository)
	if cached, found := f.cache.Get(cacheKey); found {
		if result, ok := cached.(cachedResult); ok {
			slog.DebugContext(ctx, "Small team check cached", "count", result.Count)
			return result.Members, result.Count, nil
		}
	}

	slog.InfoContext(ctx, "Checking for small team project", "owner", pr.Owner, "repo", pr.Repository)

	members, err := f.client.Collaborators(ctx, pr.Owner, pr.Repository)
	if err != nil {
		return nil, -1, fmt.Errorf("failed to fetch collaborators: %w", err)
	}

	var validMembers []string
	for _, member := range members {
		if member == pr.Author {
			continue
		}
		if f.client.IsUserBot(ctx, member) {
			continue
		}
		validMembers = append(validMembers, member)
	}

	slog.InfoContext(ctx, "Found project members", "total", len(members), "valid", len(validMembers), "pr_author", pr.Author)

	count := len(validMembers)
	var result []string

	// Only cache small teams (0-2 members) for short-circuit optimization
	if count <= 2 {
		result = validMembers
		if count > 0 {
			slog.InfoContext(ctx, "Small team project detected", "member_count", count)
		}
	} else {
		// More than 2 members - no short-circuit
		count = -1
	}

	f.cache.SetWithTTL(cacheKey, cachedResult{Members: result, Count: count}, 6*time.Hour)

	return result, count, nil
}
