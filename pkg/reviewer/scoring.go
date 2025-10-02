package reviewer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/codeGROOVE-dev/best-reviewer/pkg/types"
)

// SimplifiedScorer provides deterministic scoring for reviewer candidates.
type SimplifiedScorer struct {
	finder *Finder
}

// ReviewerScore represents a scored reviewer candidate.
type ReviewerScore struct {
	Factors  map[string]float64
	Username string
	Score    float64
}

// Contributor represents a repository contributor.
type Contributor struct {
	LastActivity time.Time
	Login        string
	Commits      int
}

// scoreContributors scores a list of contributors for their suitability as reviewers.
func (s *SimplifiedScorer) scoreContributors(ctx context.Context, pr *types.PullRequest, contributors []Contributor) []ReviewerScore {
	var scores []ReviewerScore

	for _, contributor := range contributors {
		if contributor.Login == pr.Author {
			continue
		}

		score := s.scoreReviewer(ctx, pr, contributor)
		if score.Score > 0 {
			scores = append(scores, score)
		}
	}

	// Sort by score (highest first)
	sort.Slice(scores, func(i, j int) bool {
		return scores[i].Score > scores[j].Score
	})

	// Log top candidates for transparency
	for i, score := range scores {
		if i >= topCandidatesToLog {
			break
		}
		slog.Info("Candidate scored", "rank", i+1, "username", score.Username, "score", score.Score,
			"file_overlap", score.Factors["file_overlap"], "recency", score.Factors["recency"], "expertise", score.Factors["expertise"])
	}

	return scores
}

// scoreReviewer calculates a deterministic score for a potential reviewer.
func (s *SimplifiedScorer) scoreReviewer(ctx context.Context, pr *types.PullRequest, contributor Contributor) ReviewerScore {
	score := ReviewerScore{
		Username: contributor.Login,
		Factors:  make(map[string]float64),
	}

	// Factor 1: File overlap (how many of the changed files they've touched)
	fileOverlap := s.calculateFileOverlap(ctx, pr, contributor)
	score.Factors["file_overlap"] = fileOverlap * fileOverlapWeight

	// Factor 2: Recency (when they last contributed)
	recency := s.calculateRecencyScore(contributor.LastActivity)
	score.Factors["recency"] = recency * recencyWeight

	// Factor 3: Domain expertise (previous reviews in same area)
	expertise := s.calculateDomainExpertise(ctx, pr, contributor)
	score.Factors["expertise"] = expertise * expertiseWeight

	// Calculate total score
	score.Score = score.Factors["file_overlap"] +
		score.Factors["recency"] +
		score.Factors["expertise"]

	return score
}

// calculateFileOverlap calculates how much the contributor has worked on the changed files.
func (s *SimplifiedScorer) calculateFileOverlap(ctx context.Context, pr *types.PullRequest, contributor Contributor) float64 {
	if len(pr.ChangedFiles) == 0 {
		return 0
	}

	overlap := 0.0
	totalChanges := 0

	// Weight files by significance
	fileWeights := s.fileWeights(pr.ChangedFiles)

	for _, file := range pr.ChangedFiles {
		weight := fileWeights[file.Filename]
		totalChanges += file.Additions + file.Deletions

		// Check if contributor has touched this file recently
		if s.hasContributorTouchedFile(ctx, pr.Owner, pr.Repository, contributor.Login, file.Filename) {
			overlap += weight * float64(file.Additions+file.Deletions)
		}
	}

	if totalChanges == 0 {
		return 0
	}

	// Normalize to 0-1 range
	normalized := overlap / float64(totalChanges)
	if normalized < 1.0 {
		return normalized
	}
	return 1.0
}

// calculateRecencyScore calculates a score based on how recently the contributor was active.
func (s *SimplifiedScorer) calculateRecencyScore(lastActivity time.Time) float64 {
	daysSince := time.Since(lastActivity).Hours() / 24

	// More aggressive decay for inactive users
	switch {
	case daysSince <= 1:
		return 1.0 // Active today/yesterday
	case daysSince <= 3:
		return recentActivityScore // Very recent
	case daysSince <= recentDaysThreshold:
		return weekActivityScore // Past week
	case daysSince <= biweeklyDaysThreshold:
		return biweeklyActivityScore // Past two weeks
	case daysSince <= monthlyDaysThreshold:
		return monthlyActivityScore // Past month
	case daysSince <= bimonthlyDaysThreshold:
		return bimonthlyActivityScore // Past two months
	case daysSince <= quarterlyDaysThreshold:
		return quarterlyActivityScore // Past three months
	default:
		return 0.0 // No score for very old activity
	}
}

// calculateDomainExpertise calculates expertise based on previous reviews in the same domain.
func (s *SimplifiedScorer) calculateDomainExpertise(ctx context.Context, pr *types.PullRequest, contributor Contributor) float64 {
	// Get directories from changed files
	dirs := s.finder.uniqueDirectories(pr.ChangedFiles)
	if len(dirs) == 0 {
		return 0
	}

	primaryDir := dirs[0] // Most specific directory

	// Check cache for domain expertise
	cacheKey := makeCacheKey("domain-expertise", pr.Owner, pr.Repository, contributor.Login, primaryDir)
	if cached, found := s.finder.cache.Get(cacheKey); found {
		if score, ok := cached.(float64); ok {
			return score
		}
	}

	// Calculate expertise score
	expertiseScore := s.calculateDirectoryExpertise(ctx, pr.Owner, pr.Repository, contributor.Login, primaryDir)

	// Cache the result
	s.finder.cache.SetWithTTL(cacheKey, expertiseScore, directoryOwnersCacheTTL)

	return expertiseScore
}

// calculateDirectoryExpertise calculates how much expertise a user has in a directory.
func (s *SimplifiedScorer) calculateDirectoryExpertise(ctx context.Context, owner, repo, user, directory string) float64 {
	// This would query for the user's review history in this directory
	// For now, returning a simplified score
	return defaultExpertiseScore
}

// fileWeights calculates significance weights for changed files.
func (s *SimplifiedScorer) fileWeights(files []types.ChangedFile) map[string]float64 {
	weights := make(map[string]float64)

	for _, file := range files {
		weight := 1.0

		// Boost production code over test code
		if strings.HasSuffix(file.Filename, ".go") && !strings.HasSuffix(file.Filename, "_test.go") {
			weight *= prodCodeMultiplier
		}

		// Boost critical files
		if s.isCriticalFile(file.Filename) {
			weight *= criticalFileMultiplier
		}

		// Boost refactoring (more deletions than additions)
		if file.Deletions > file.Additions {
			weight *= refactoringMultiplier
		}

		weights[file.Filename] = weight
	}

	return weights
}

// isCriticalFile determines if a file is critical based on patterns.
func (s *SimplifiedScorer) isCriticalFile(filename string) bool {
	criticalPatterns := []string{
		"main.go",
		"handler",
		"server",
		"auth",
		"security",
		"payment",
		"database",
		"migration",
	}

	lower := strings.ToLower(filename)
	for _, pattern := range criticalPatterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}

	return false
}

// hasContributorTouchedFile checks if a contributor has recently touched a file.
func (s *SimplifiedScorer) hasContributorTouchedFile(ctx context.Context, owner, repo, user, file string) bool {
	// Check cache first
	cacheKey := makeCacheKey("user-file-touch", owner, repo, user, file)
	if cached, found := s.finder.cache.Get(cacheKey); found {
		if touched, ok := cached.(bool); ok {
			return touched
		}
	}

	// This would check commit history for the file
	// For now, returning a simplified check
	touched := false // Would be determined by API call

	// Cache the result
	s.finder.cache.SetWithTTL(cacheKey, touched, fileHistoryCacheTTL)

	return touched
}

// topContributors fetches the top contributors for a repository.
func (f *Finder) topContributors(ctx context.Context, owner, repo string) []Contributor {
	// Check cache first
	cacheKey := makeCacheKey("top-contributors", owner, repo)
	if cached, found := f.cache.Get(cacheKey); found {
		if contributors, ok := cached.([]Contributor); ok {
			return contributors
		}
	}

	slog.Info("  [API] Fetching top contributors for %s/%s", owner, repo)

	// First, get all user activity in a single batch
	userActivities := f.fetchRepoUserActivity(ctx, owner, repo)

	// Fetch from GitHub API
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/contributors?per_page=30", owner, repo)
	resp, err := f.client.MakeRequest(ctx, httpMethodGet, url, nil)
	if err != nil {
		slog.Warn("Failed to fetch contributors", "error", err)
		return nil
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Warn("Failed to close response body", "error", err)
		}
	}()

	var apiContributors []struct {
		Login         string `json:"login"`
		Contributions int    `json:"contributions"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&apiContributors); err != nil {
		slog.Warn("Failed to decode contributors", "error", err)
		return nil
	}

	// Convert to our format using pre-fetched activity data
	contributors := make([]Contributor, 0, len(apiContributors))
	for _, ac := range apiContributors {
		// Get last activity from pre-fetched data
		var lastActivity time.Time
		if activity, exists := userActivities[ac.Login]; exists {
			lastActivity = activity.LastActivity
			daysSince := int(time.Since(lastActivity).Hours() / 24)
			slog.Debug("User last active", "username", ac.Login, "days_ago", daysSince, "source", activity.Source)
		} else {
			// No recent activity found
			lastActivity = time.Now().Add(-365 * 24 * time.Hour) // Default to 1 year ago
			slog.Debug("No recent activity found for user", "username", ac.Login)
		}

		// Filter out users who haven't been active in over 90 days
		daysSince := time.Since(lastActivity).Hours() / 24
		if daysSince > quarterlyDaysThreshold {
			slog.Debug("Skipping inactive user", "username", ac.Login, "inactive_days", int(daysSince))
			continue
		}

		contributors = append(contributors, Contributor{
			Login:        ac.Login,
			Commits:      ac.Contributions,
			LastActivity: lastActivity,
		})
	}

	// Cache the result with shorter TTL for catching returning contributors
	f.cache.SetWithTTL(cacheKey, contributors, repoContributorsCacheTTL)

	return contributors
}

// uniqueDirectories extracts unique directories from changed files, sorted by specificity.
func (f *Finder) uniqueDirectories(files []types.ChangedFile) []string {
	dirMap := make(map[string]bool)
	for _, file := range files {
		parts := strings.Split(file.Filename, "/")
		if len(parts) > 1 {
			// Get the directory path
			dir := strings.Join(parts[:len(parts)-1], "/")
			dirMap[dir] = true
		}
	}

	dirs := make([]string, 0, len(dirMap))
	for dir := range dirMap {
		dirs = append(dirs, dir)
	}

	// Sort by depth (deeper first - more specific)
	sort.Slice(dirs, func(i, j int) bool {
		depthI := strings.Count(dirs[i], "/")
		depthJ := strings.Count(dirs[j], "/")
		if depthI != depthJ {
			return depthI > depthJ
		}
		return dirs[i] < dirs[j]
	})

	return dirs
}
