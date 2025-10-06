// Package main implements a CLI tool for finding the best reviewers for a GitHub pull request.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/codeGROOVE-dev/best-reviewer/pkg/github"
	"github.com/codeGROOVE-dev/best-reviewer/pkg/reviewer"
)

var verbose = flag.Bool("v", false, "Verbose output with detailed diagnostics")

const prCountCache = 6 * time.Hour

func defaultCacheDir() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return dir + "/best-reviewer"
}

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s <PR_URL> [options]\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Analyzes a GitHub pull request and recommends the top 5 reviewers.\n\n")
		fmt.Fprintf(os.Stderr, "Arguments:\n")
		fmt.Fprintf(os.Stderr, "  PR_URL    Pull request URL (e.g., https://github.com/owner/repo/pull/123 or owner/repo#123)\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  %s https://github.com/owner/repo/pull/123\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s owner/repo#123\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s owner/repo#123 -v\n", os.Args[0])
	}
	flag.Parse()

	if flag.NArg() < 1 {
		flag.Usage()
		os.Exit(1)
	}

	prURL := flag.Arg(0)

	// Set up structured logging
	logLevel := slog.LevelInfo
	if *verbose {
		logLevel = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	}))
	slog.SetDefault(logger)

	ctx := context.Background()

	// Parse PR URL
	owner, repo, prNumber, err := parsePRURL(prURL)
	if err != nil {
		slog.Error("Invalid PR URL", "error", err)
		os.Exit(1)
	}

	// Get GitHub token from gh CLI
	token, err := getGitHubToken(ctx)
	if err != nil {
		slog.Error("Failed to get GitHub token", "error", err)
		slog.Info("Make sure you have the gh CLI installed and authenticated (run: gh auth login)")
		os.Exit(1)
	}

	// Create GitHub client
	cfg := github.Config{
		UseAppAuth:  false,
		Token:       token,
		HTTPTimeout: 30 * time.Second,
		CacheTTL:    24 * time.Hour,
		CacheDir:    defaultCacheDir(),
	}
	client, err := github.New(ctx, cfg)
	if err != nil {
		slog.Error("Failed to create GitHub client", "error", err)
		os.Exit(1)
	}

	// Create reviewer finder
	finderCfg := reviewer.Config{
		PRCountCache: prCountCache,
	}
	finder := reviewer.New(client, finderCfg)

	// Fetch PR details
	slog.Info("Fetching PR details", "owner", owner, "repo", repo, "number", prNumber)
	pr, err := client.PullRequest(ctx, owner, repo, prNumber)
	if err != nil {
		slog.Error("Failed to fetch PR", "error", err)
		os.Exit(1)
	}

	// Print PR information
	fmt.Printf("\n📋 Pull Request: %s/%s#%d\n", owner, repo, prNumber)
	fmt.Printf("   Title: %s\n", pr.Title)
	fmt.Printf("   Author: %s\n", pr.Author)
	fmt.Printf("   State: %s\n", pr.State)
	if pr.Draft {
		fmt.Printf("   Draft: yes\n")
	}
	fmt.Printf("   Changed files: %d\n", len(pr.ChangedFiles))
	if len(pr.Reviewers) > 0 {
		fmt.Printf("   Current reviewers: %s\n", strings.Join(pr.Reviewers, ", "))
	}
	fmt.Println()

	// Get all project collaborators for context
	collaborators, err := client.Collaborators(ctx, owner, repo)
	if err != nil {
		slog.Warn("Failed to fetch collaborators", "error", err)
		// Continue without collaborator list
	}

	// Find reviewers
	slog.Info("Finding best reviewers")
	candidates, err := finder.Find(ctx, pr)
	if err != nil {
		slog.Error("Failed to find reviewers", "error", err)
		os.Exit(1)
	}

	// Display collaborators if available
	if len(collaborators) > 0 {
		// Filter out bots from display
		validCollaborators := make([]string, 0, len(collaborators))
		for _, collab := range collaborators {
			if !client.IsUserBot(ctx, collab) {
				validCollaborators = append(validCollaborators, collab)
			}
		}

		if len(validCollaborators) > 0 {
			fmt.Printf("👥 Project Collaborators (%d):\n   ", len(validCollaborators))
			fmt.Println(strings.Join(validCollaborators, ", "))
			fmt.Println()
		}
	}

	// Display results
	if len(candidates) == 0 {
		fmt.Println("❌ No suitable reviewers found")
		os.Exit(0)
	}

	fmt.Printf("🏆 Top %d Reviewer Recommendations (in descending order):\n\n", min(5, len(candidates)))
	for i, candidate := range candidates[:min(5, len(candidates))] {
		fmt.Printf("%d. @%s\n", i+1, candidate.Username)
		fmt.Printf("   Selection Method: %s\n", candidate.SelectionMethod)
		fmt.Printf("   Context Score: %d\n", candidate.ContextScore)
		if candidate.ActivityScore > 0 {
			fmt.Printf("   Activity Score: %d\n", candidate.ActivityScore)
		}
		if !candidate.LastActivity.IsZero() {
			fmt.Printf("   Last Activity: %s\n", candidate.LastActivity.Format(time.RFC3339))
		}
		if *verbose && candidate.AuthorAssociation != "" {
			fmt.Printf("   Association: %s\n", candidate.AuthorAssociation)
		}
		fmt.Println()
	}

	fmt.Printf("✅ Found %d total candidates\n", len(candidates))
}

// parsePRURL parses a PR URL or shorthand into owner, repo, and PR number.
func parsePRURL(url string) (owner, repo string, prNumber int, err error) {
	// Handle shorthand: owner/repo#123
	if strings.Contains(url, "#") && !strings.Contains(url, "://") {
		parts := strings.Split(url, "#")
		if len(parts) != 2 {
			return "", "", 0, errors.New("invalid PR shorthand format (expected owner/repo#number)")
		}
		repoPath := strings.Split(parts[0], "/")
		if len(repoPath) != 2 {
			return "", "", 0, errors.New("invalid repository path (expected owner/repo)")
		}
		_, err := fmt.Sscanf(parts[1], "%d", &prNumber)
		if err != nil {
			return "", "", 0, fmt.Errorf("invalid PR number: %w", err)
		}
		return repoPath[0], repoPath[1], prNumber, nil
	}

	// Handle full URL: https://github.com/owner/repo/pull/123
	if strings.HasPrefix(url, "https://github.com/") || strings.HasPrefix(url, "http://github.com/") {
		url = strings.TrimPrefix(url, "https://github.com/")
		url = strings.TrimPrefix(url, "http://github.com/")
		parts := strings.Split(url, "/")
		if len(parts) < 4 || parts[2] != "pull" {
			return "", "", 0, errors.New("invalid GitHub PR URL format")
		}
		_, err := fmt.Sscanf(parts[3], "%d", &prNumber)
		if err != nil {
			return "", "", 0, fmt.Errorf("invalid PR number: %w", err)
		}
		return parts[0], parts[1], prNumber, nil
	}

	return "", "", 0, errors.New("invalid PR URL format (use: https://github.com/owner/repo/pull/123 or owner/repo#123)")
}

// getGitHubToken retrieves the GitHub token from gh CLI.
func getGitHubToken(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "gh", "auth", "token")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get GitHub token: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
