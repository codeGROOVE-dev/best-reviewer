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
	app     = flag.Bool("app", false, "Monitor all organizations where this GitHub app is installed (deprecated, use --app-id and --app-key)")

	// GitHub App authentication flags.
	appID  = flag.String("app-id", "", "GitHub App ID for authentication")
	appKey = flag.String("app-key", "", "Path to GitHub App private key file")

	// Behavior flags.
	poll         = flag.Duration("poll", 0, "Polling interval (e.g., 1h, 30m). If not set, runs once")
	serve        = flag.Bool("serve", false, "Run in server mode with health endpoint")
	loopDelay    = flag.Duration("loop-delay", 5*time.Minute, "Loop delay in serve mode (default: 5m)")
	dryRun       = flag.Bool("dry-run", false, "Run in dry-run mode (no actual approvals)")
	minOpenTime  = flag.Duration("min-age", 1*time.Hour, "Minimum time since last activity for PR assignment")
	maxOpenTime  = flag.Duration("max-age", 180*24*time.Hour, "Maximum time since last activity for PR assignment")
	maxPRs       = flag.Int("max-prs", 9, "Maximum number of non-stale open PRs a candidate can have before being filtered out")
	prCountCache = flag.Duration("pr-count-cache", prCountCacheTTL, "Cache duration for PR count queries (e.g., 6h, 12h)")
)

func main() {
	setupUsage()
	flag.Parse()

	// Check environment variables if flags are empty
	effectiveAppID := *appID
	effectiveAppKey := *appKey
	if effectiveAppID == "" {
		effectiveAppID = os.Getenv("GITHUB_APP_ID")
	}
	if effectiveAppKey == "" {
		effectiveAppKey = os.Getenv("GITHUB_APP_KEY")
		if effectiveAppKey == "" {
			effectiveAppKey = os.Getenv("GITHUB_APP_PRIVATE_KEY_PATH")
		}
	}

	if err := validateFlags(effectiveAppID, effectiveAppKey); err != nil {
		log.Fatalf("Invalid flags: %v", err)
	}

	ctx := context.Background()
	// Use app authentication if either new flags or old flag is set
	useAppAuth := *app || (effectiveAppID != "" && effectiveAppKey != "")
	client, err := newGitHubClient(ctx, useAppAuth, effectiveAppID, effectiveAppKey)
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

	// Handle different execution modes
	switch {
	case *serve:
		if !useAppAuth {
			log.Fatal("Serve mode requires GitHub App authentication (--app-id and --app-key)")
		}
		log.Printf("Starting serve mode with loop delay: %v", *loopDelay)
		finder.runServeMode(ctx, *loopDelay)
	case *poll > 0:
		log.Printf("Starting polling mode with interval: %v", *poll)
		if useAppAuth {
			finder.startAppPolling(ctx, *poll)
		} else {
			finder.startPolling(ctx, *poll)
		}
	default:
		if useAppAuth {
			if err := finder.findAndAssignReviewersForApp(ctx); err != nil {
				log.Fatalf("Failed to find and assign reviewers for app installations: %v", err)
			}
		} else {
			if err := finder.findAndAssignReviewers(ctx); err != nil {
				log.Fatalf("Failed to find and assign reviewers: %v", err)
			}
		}
	}
}

// validateFlags ensures exactly one target flag is set.
func validateFlags(effectiveAppID, effectiveAppKey string) error {
	// Serve mode requires app auth
	if *serve {
		if effectiveAppID == "" || effectiveAppKey == "" {
			return errors.New("--serve mode requires --app-id and --app-key")
		}
		return nil
	}

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
	if *app || (effectiveAppID != "" && effectiveAppKey != "") {
		targetFlags++
	}

	if targetFlags != 1 {
		return errors.New("exactly one of -pr, -project, -org, --app-id/--app-key, or --serve must be specified")
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
		fmt.Fprint(os.Stderr, "  -app\n")
		fmt.Fprint(os.Stderr, "    \tMonitor all organizations where this GitHub app is installed\n")
		fmt.Fprint(os.Stderr, "\nBehavior flags:\n")
		flag.PrintDefaults()
	}
}
