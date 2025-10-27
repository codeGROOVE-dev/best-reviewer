package reviewer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/codeGROOVE-dev/best-reviewer/pkg/cache"
	"github.com/codeGROOVE-dev/best-reviewer/pkg/github"
	"github.com/codeGROOVE-dev/best-reviewer/pkg/types"
)

// Finder finds and selects reviewers for pull requests.
type Finder struct {
	client       *github.Client
	cache        *cache.Cache
	prCountCache time.Duration
}

// Config holds configuration for the reviewer finder.
type Config struct {
	PRCountCache time.Duration // Cache duration for PR counts
}

// New creates a new Finder with the given GitHub client and configuration.
func New(client *github.Client, cfg Config) *Finder {
	return &Finder{
		client:       client,
		cache:        cache.New(cacheTTL),
		prCountCache: cfg.PRCountCache,
	}
}

// Find finds the best reviewers for a pull request.
// Returns a list of reviewer candidates sorted by relevance.
func (f *Finder) Find(ctx context.Context, pr *types.PullRequest) ([]types.ReviewerCandidate, error) {
	if pr == nil {
		return nil, errors.New("pr cannot be nil")
	}

	slog.Info("Finding reviewers for PR", "pr", pr.Number, "owner", pr.Owner, "repo", pr.Repository)

	// Check if project has only 0-2 members with write access for early short-circuit
	smallTeamMembers, totalMembers, err := f.checkSmallTeamProject(ctx, pr)
	if err != nil {
		slog.Warn("Failed to check small team project (continuing)", "error", err)
	} else if totalMembers >= 0 && totalMembers <= 2 {
		// Short-circuit for small teams (0-2 valid members excluding PR author)
		switch len(smallTeamMembers) {
		case 0:
			slog.Info("Project has no valid reviewers (single-person project or PR author is only member)")
			return nil, nil
		case 1:
			slog.Info("Project has single member, assigning to them", "member", smallTeamMembers[0])
		default:
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

// checkSmallTeamProject checks if the project has only 0-2 members with write access.
// Returns (valid members, total count, error).
// Valid members excludes the PR author and bots. Total count is the number of valid members.
// Returns count=-1 if there are more than 2 valid members (no short-circuit needed).
func (f *Finder) checkSmallTeamProject(ctx context.Context, pr *types.PullRequest) (members []string, count int, err error) {
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

	collaborators, err := f.client.Collaborators(ctx, pr.Owner, pr.Repository)
	if err != nil {
		return nil, -1, fmt.Errorf("failed to fetch collaborators: %w", err)
	}

	var validMembers []string
	for _, member := range collaborators {
		if member == pr.Author {
			continue
		}
		if f.client.IsUserBot(ctx, member) {
			continue
		}
		validMembers = append(validMembers, member)
	}

	slog.InfoContext(ctx, "Found project members", "total", len(collaborators), "valid", len(validMembers), "pr_author", pr.Author)

	count = len(validMembers)
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
