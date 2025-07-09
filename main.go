package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"
)

var (
	// Target flags (mutually exclusive)
	prURL   = flag.String("pr", "", "Pull request URL (e.g., https://github.com/owner/repo/pull/123 or owner/repo#123)")
	project = flag.String("project", "", "GitHub project to monitor (e.g., owner/repo)")
	org     = flag.String("org", "", "GitHub organization to monitor")

	// Behavior flags
	poll        = flag.Duration("poll", 0, "Polling interval (e.g., 1h, 30m). If not set, runs once")
	dryRun      = flag.Bool("dry-run", false, "Run in dry-run mode (no actual approvals)")
	minOpenTime = flag.Duration("min-age", 1*time.Hour, "Minimum time PR since last commit or review for PR assignment")
	maxOpenTime = flag.Duration("max-age", 180*24*time.Hour, "Maximum time PR since last commit or review for PR assignment")
)

func main() {
	flag.Parse()

	if err := validateFlags(); err != nil {
		log.Fatalf("Invalid flags: %v", err)
	}

	ctx := context.Background()
	client, err := newGitHubClient()
	if err != nil {
		log.Fatalf("Failed to create GitHub client: %v", err)
	}

	reviewer := &ReviewerFinder{
		client:      client,
		dryRun:      *dryRun,
		minOpenTime: *minOpenTime,
		maxOpenTime: *maxOpenTime,
	}

	if *poll > 0 {
		log.Printf("Starting polling mode with interval: %v", *poll)
		reviewer.startPolling(ctx, *poll)
	} else {
		if err := reviewer.findAndAssignReviewers(ctx); err != nil {
			log.Fatalf("Failed to find and assign reviewers: %v", err)
		}
	}
}

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
		return fmt.Errorf("exactly one of -pr, -project, or -org must be specified")
	}

	return nil
}

func usage() {
	fmt.Fprintf(os.Stderr, "Usage: %s [flags]\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "\nTarget flags (mutually exclusive):\n")
	fmt.Fprintf(os.Stderr, "  -pr string\n")
	fmt.Fprintf(os.Stderr, "    \tPull request URL (e.g., https://github.com/owner/repo/pull/123 or owner/repo#123)\n")
	fmt.Fprintf(os.Stderr, "  -project string\n")
	fmt.Fprintf(os.Stderr, "    \tGitHub project to monitor (e.g., owner/repo)\n")
	fmt.Fprintf(os.Stderr, "  -org string\n")
	fmt.Fprintf(os.Stderr, "    \tGitHub organization to monitor\n")
	fmt.Fprintf(os.Stderr, "\nBehavior flags:\n")
	flag.PrintDefaults()
}

func init() {
	flag.Usage = usage
}