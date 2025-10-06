package reviewer

import (
	"context"
)

// findRecentAuthorsInProject finds recent commit authors in the project.
func (f *Finder) findRecentAuthorsInProject(ctx context.Context, owner, repo string, limit int) []string {
	prs, err := f.recentPRsInProject(ctx, owner, repo)
	if err != nil {
		return nil
	}

	fc := make(frequencyCounter)
	for _, pr := range prs {
		if pr.Author != "" {
			fc[pr.Author]++
		}
	}
	return fc.top(limit)
}

// findActiveReviewersInProject finds active reviewers in the project.
func (f *Finder) findActiveReviewersInProject(ctx context.Context, owner, repo string, limit int) []string {
	prs, err := f.recentPRsInProject(ctx, owner, repo)
	if err != nil {
		return nil
	}

	fc := make(frequencyCounter)
	for _, pr := range prs {
		// Add weight for who merged the PR (strong signal of active maintainer)
		if pr.MergedBy != "" {
			fc[pr.MergedBy] += 2 // Double weight for mergers
		}
		for _, r := range pr.Reviewers {
			if r != "" {
				fc[r]++
			}
		}
	}
	return fc.top(limit)
}
