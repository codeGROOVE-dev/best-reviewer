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

	slog.Info("🔍 Finding reviewers for PR #%d in %s/%s", pr.Number, pr.Owner, pr.Repository)

	// Use optimized method
	candidates := f.findReviewersOptimized(ctx, pr)
	if len(candidates) > 0 {
		slog.Info("  ✅ Found %d candidates via optimized search", len(candidates))
		return candidates, nil
	}

	// Final fallback to original method
	slog.Info("  ⚠️  Using fallback method")
	candidates = f.findReviewersFallback(ctx, pr)
	if len(candidates) > 0 {
		slog.Info("  ✅ Found %d candidates via fallback", len(candidates))
	} else {
		slog.Info("  ❌ No suitable reviewers found")
	}

	return candidates, nil
}

// findReviewersFallback is the original reviewer finding logic as a fallback.
func (f *Finder) findReviewersFallback(ctx context.Context, pr *types.PullRequest) []types.ReviewerCandidate {
	files := f.changedFiles(pr)

	// Pre-fetch all PR file patches
	patchCache, err := f.fetchAllPRFiles(ctx, pr.Owner, pr.Repository, pr.Number)
	if err != nil {
		slog.Info("[WARN] Failed to fetch PR file patches: %v (continuing without patches)", err)
		patchCache = make(map[string]string) // Empty cache as fallback
	}

	// Find expert author (code ownership context)
	author, authorMethod := f.findExpertAuthor(ctx, pr, files, patchCache)

	// Find expert reviewer (review activity context)
	reviewer, reviewerMethod := f.findExpertReviewer(ctx, pr, files, patchCache, author)

	// Build final candidate list
	var candidates []types.ReviewerCandidate

	if author != "" && author != pr.Author {
		candidates = append(candidates, types.ReviewerCandidate{
			Username:        author,
			SelectionMethod: authorMethod,
			ContextScore:    maxContextScore,
		})
	}

	if reviewer != "" && reviewer != pr.Author && reviewer != author {
		candidates = append(candidates, types.ReviewerCandidate{
			Username:        reviewer,
			SelectionMethod: reviewerMethod,
			ContextScore:    maxContextScore / 2,
		})
	}

	return candidates
}

// findExpertAuthor finds the most relevant code author for the changes.
func (f *Finder) findExpertAuthor(
	ctx context.Context, pr *types.PullRequest, files []string, patchCache map[string]string,
) (author string, method string) {
	// Check assignees first
	if assignee := f.findAssigneeExpert(ctx, pr); assignee != "" {
		return assignee, SelectionAssignee
	}

	// Check line overlap
	if author := f.findOverlappingAuthor(ctx, pr, files, patchCache); author != "" {
		return author, SelectionAuthorOverlap
	}

	// Check directory authors
	if author := f.findDirectoryAuthor(ctx, pr, files); author != "" {
		return author, SelectionAuthorDirectory
	}

	// Check project authors
	if author := f.findProjectAuthor(ctx, pr); author != "" {
		return author, SelectionAuthorProject
	}

	return "", ""
}

// findAssigneeExpert checks if any PR assignees can be expert authors.
func (f *Finder) findAssigneeExpert(ctx context.Context, pr *types.PullRequest) string {
	for _, assignee := range pr.Assignees {
		if assignee == pr.Author {
			slog.Info("    Filtered (is PR author): %s", assignee)
			continue
		}
		if f.isValidReviewer(ctx, pr, assignee) {
			return assignee
		}
	}
	return ""
}

// isValidReviewer checks if a user is a valid reviewer.
func (f *Finder) isValidReviewer(ctx context.Context, pr *types.PullRequest, username string) bool {
	// Check if user is a bot
	if f.client.IsUserBot(ctx, username) {
		slog.Info("    Filtered (is bot): %s", username)
		return false
	}

	// Check write access
	hasAccess := f.client.HasWriteAccess(ctx, pr.Owner, pr.Repository, username)
	if !hasAccess {
		slog.Info("    Filtered (no write access): %s", username)
		return false
	}

	// Check PR count for workload balancing across the organization
	// This is a best-effort check - if it fails, we continue with the candidate
	prCount, err := f.client.OpenPRCount(ctx, pr.Owner, username, f.prCountCache)
	if err != nil {
		slog.Info("    ⚠️  Warning: could not check PR count for %s in org %s: %v (continuing without PR count filter)", username, pr.Owner, err)
		// Continue without filtering - better to have a reviewer than none at all
	} else if prCount > f.maxPRs {
		slog.Info("    Filtered (too many open PRs %d > %d in org %s): %s", prCount, f.maxPRs, pr.Owner, username)
		return false
	}

	return true
}

// findExpertReviewer finds the most active reviewer for the changes.
func (f *Finder) findExpertReviewer(
	ctx context.Context, pr *types.PullRequest, files []string, patchCache map[string]string, excludeAuthor string,
) (reviewer string, method string) {
	// Check line overlap
	if reviewer := f.findOverlappingReviewer(ctx, pr, files, patchCache, excludeAuthor); reviewer != "" {
		return reviewer, SelectionReviewerOverlap
	}

	// Check directory reviewers
	if reviewer := f.findDirectoryReviewer(ctx, pr, files, excludeAuthor); reviewer != "" {
		return reviewer, SelectionReviewerDirectory
	}

	// Check project reviewers
	if reviewer := f.findProjectReviewer(ctx, pr, excludeAuthor); reviewer != "" {
		return reviewer, SelectionReviewerProject
	}

	return "", ""
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

	slog.Info("  [API] Fetching all file patches for PR #%d", prNumber)

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
	slog.Info("  ✅ Fetched and cached %d file patches", len(patches))

	return patches, nil
}
