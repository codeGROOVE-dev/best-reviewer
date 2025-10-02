package reviewer

import (
	"context"
	"log/slog"

	"github.com/codeGROOVE-dev/best-reviewer/pkg/types"
)

// findDirectoryAuthor finds the most recent author in the affected directories.
func (f *Finder) findDirectoryAuthor(ctx context.Context, pr *types.PullRequest, files []string) string {
	dirs := directories(files)

	for _, dir := range dirs {
		if author := f.findRecentAuthorInDirectory(ctx, pr.Owner, pr.Repository, dir); author != "" {
			if author == pr.Author {
				slog.Info("Filtered (is PR author)", "author", author)
				continue
			}
			if f.isValidReviewer(ctx, pr, author) {
				return author
			}
		}
	}

	return ""
}

// findProjectAuthor finds the most recent author in the project.
func (f *Finder) findProjectAuthor(ctx context.Context, pr *types.PullRequest) string {
	authors := f.findRecentAuthorsInProject(ctx, pr.Owner, pr.Repository, topReviewersLimit)

	for _, author := range authors {
		if author == pr.Author {
			slog.Info("Filtered (is PR author)", "author", author)
			continue
		}
		if f.isValidReviewer(ctx, pr, author) {
			return author
		}
	}

	return ""
}

// findDirectoryReviewer finds the most active reviewer in the affected directories.
func (f *Finder) findDirectoryReviewer(ctx context.Context, pr *types.PullRequest, files []string, excludeAuthor string) string {
	dirs := directories(files)

	for _, dir := range dirs {
		if reviewer := f.findActiveReviewerInDirectory(ctx, pr.Owner, pr.Repository, dir); reviewer != "" {
			if reviewer == pr.Author {
				slog.Info("Filtered (is PR author)", "reviewer", reviewer)
				continue
			}
			if reviewer == excludeAuthor {
				slog.Info("Filtered (is excluded author)", "reviewer", reviewer)
				continue
			}
			if f.isValidReviewer(ctx, pr, reviewer) {
				return reviewer
			}
		}
	}

	return ""
}

// findProjectReviewer finds the most active reviewer in the project.
func (f *Finder) findProjectReviewer(ctx context.Context, pr *types.PullRequest, excludeAuthor string) string {
	reviewers := f.findActiveReviewersInProject(ctx, pr.Owner, pr.Repository, topReviewersLimit)

	for _, reviewer := range reviewers {
		if reviewer == pr.Author {
			slog.Info("Filtered (is PR author)", "reviewer", reviewer)
			continue
		}
		if reviewer == excludeAuthor {
			slog.Info("Filtered (is excluded author)", "reviewer", reviewer)
			continue
		}
		if f.isValidReviewer(ctx, pr, reviewer) {
			return reviewer
		}
	}

	return ""
}

// findRecentAuthorInDirectory finds the most recent commit author in a directory.
func (f *Finder) findRecentAuthorInDirectory(ctx context.Context, owner, repo, directory string) string {
	prs, err := f.recentPRsInDirectory(ctx, owner, repo, directory)
	if err != nil {
		return ""
	}

	fc := make(frequencyCounter)
	for _, pr := range prs {
		if pr.Author != "" {
			fc[pr.Author]++
		}
	}
	return fc.best()
}

// findActiveReviewerInDirectory finds the most active reviewer in a directory.
func (f *Finder) findActiveReviewerInDirectory(ctx context.Context, owner, repo, directory string) string {
	prs, err := f.recentPRsInDirectory(ctx, owner, repo, directory)
	if err != nil {
		return ""
	}

	fc := make(frequencyCounter)
	for _, pr := range prs {
		for _, r := range pr.Reviewers {
			if r != "" {
				fc[r]++
			}
		}
	}
	return fc.best()
}

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
		for _, r := range pr.Reviewers {
			if r != "" {
				fc[r]++
			}
		}
	}
	return fc.top(limit)
}
