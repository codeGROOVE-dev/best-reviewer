// Package main implements a tool for automatically finding and assigning
// reviewers to GitHub pull requests based on code ownership and activity patterns.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"time"
)

var (
	// Target flags (mutually exclusive).
	prURL   = flag.String("pr", "", "Pull request URL (e.g., https://github.com/owner/repo/pull/123 or owner/repo#123)")
	project = flag.String("project", "", "GitHub project to monitor (e.g., owner/repo)")
	org     = flag.String("org", "", "GitHub organization to monitor")

	// Behavior flags.
	poll         = flag.Duration("poll", 0, "Polling interval (e.g., 1h, 30m). If not set, runs once")
	dryRun       = flag.Bool("dry-run", false, "Run in dry-run mode (no actual approvals)")
	minOpenTime  = flag.Duration("min-age", 1*time.Hour, "Minimum time since last activity for PR assignment")
	maxOpenTime  = flag.Duration("max-age", 180*24*time.Hour, "Maximum time since last activity for PR assignment")
	maxPRs       = flag.Int("max-prs", 9, "Maximum number of non-stale open PRs a candidate can have before being filtered out")
	prCountCache = flag.Duration("pr-count-cache", prCountCacheTTL, "Cache duration for PR count queries (e.g., 6h, 12h)")
)

func main() {
	setupUsage()
	flag.Parse()

	if err := validateFlags(); err != nil {
		log.Fatalf("Invalid flags: %v", err)
	}

	ctx := context.Background()
	client, err := newGitHubClient(ctx)
	if err != nil {
		log.Fatalf("Failed to create GitHub client: %v", err)
	}

	finder := &ReviewerFinder{
		client:       client,
		dryRun:       *dryRun,
		minOpenTime:  *minOpenTime,
		maxOpenTime:  *maxOpenTime,
		maxPRs:       *maxPRs,
		prCountCache: *prCountCache,
		output:       &outputFormatter{verbose: true},
	}

	if *poll > 0 {
		log.Printf("Starting polling mode with interval: %v", *poll)
		finder.startPolling(ctx, *poll)
	} else {
		if err := finder.findAndAssignReviewers(ctx); err != nil {
			log.Fatalf("Failed to find and assign reviewers: %v", err)
		}
	}
}

// validateFlags ensures exactly one target flag is set.
func validateFlags() error {
	targetFlags := 0
	if *prURL != "" {
		targetFlags++
	}
	if *project != "" {
		targetFlags++
	}
	if *org != "" {
		targetFlags++
	}

	if targetFlags != 1 {
		return errors.New("exactly one of -pr, -project, or -org must be specified")
	}

	return nil
}

func setupUsage() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [flags]\n", os.Args[0])
		fmt.Fprint(os.Stderr, "\nTarget flags (mutually exclusive):\n")
		fmt.Fprint(os.Stderr, "  -pr string\n")
		fmt.Fprint(os.Stderr, "    \tPull request URL (e.g., https://github.com/owner/repo/pull/123 or owner/repo#123)\n")
		fmt.Fprint(os.Stderr, "  -project string\n")
		fmt.Fprint(os.Stderr, "    \tGitHub project to monitor (e.g., owner/repo)\n")
		fmt.Fprint(os.Stderr, "  -org string\n")
		fmt.Fprint(os.Stderr, "    \tGitHub organization to monitor\n")
		fmt.Fprint(os.Stderr, "\nBehavior flags:\n")
		flag.PrintDefaults()
	}
}
