// Package main implements a GitHub App bot that automatically assigns reviewers across all installed organizations.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/codeGROOVE-dev/best-reviewer/pkg/github"
	"github.com/codeGROOVE-dev/best-reviewer/pkg/reviewer"
	"github.com/codeGROOVE-dev/best-reviewer/pkg/types"
	"github.com/codeGROOVE-dev/prx/pkg/prx"
)

var (
	// GitHub App authentication flags.
	appID      = flag.String("app-id", "", "GitHub App ID for authentication")
	appKeyPath = flag.String("app-key-path", "", "Path to GitHub App private key file")

	// Behavior flags.
	loopDelay   = flag.Duration("loop-delay", 5*time.Minute, "Loop delay between polling cycles (default: 5m)")
	dryRun      = flag.Bool("dry-run", false, "Run in dry-run mode (no actual reviewer assignments)")
	minOpenTime = flag.Duration("min-age", 0, "Minimum time since last activity for PR assignment")
	maxOpenTime = flag.Duration("max-age", 10*365*24*time.Hour, "Maximum time since last activity for PR assignment")

	prCountCache = flag.Duration("pr-count-cache", 6*time.Hour, "Cache duration for PR count queries")
)

// prxClientWrapper wraps prx.Client to satisfy the interface expected by github.Client.
type prxClientWrapper struct {
	client *prx.Client
}

// PullRequestWithReferenceTime wraps the prx.Client.PullRequestWithReferenceTime method to return any.
func (w *prxClientWrapper) PullRequestWithReferenceTime(ctx context.Context, owner, repo string, prNumber int, referenceTime time.Time) (any, error) {
	return w.client.PullRequestWithReferenceTime(ctx, owner, repo, prNumber, referenceTime)
}

// MetricsCollector tracks metrics for the health endpoint.
type MetricsCollector struct {
	uniqueOrgs        map[string]bool
	uniquePRsSeen     map[string]bool
	uniquePRsModified map[string]bool
	lastRun           time.Time
	mu                sync.RWMutex
	totalRuns         int64
	pollingMu         sync.Mutex
	isPolling         bool
}

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [options]\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "GitHub App bot that automatically assigns reviewers to PRs across all installed organizations.\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nEnvironment Variables:\n")
		fmt.Fprintf(os.Stderr, "  GITHUB_APP_ID               - GitHub App ID\n")
		fmt.Fprintf(os.Stderr, "  GITHUB_APP_KEY              - Secret name in Google Secret Manager for private key\n")
		fmt.Fprintf(os.Stderr, "  GITHUB_APP_KEY_PATH         - Path to GitHub App private key file\n")
		fmt.Fprintf(os.Stderr, "  PORT                        - HTTP server port (default: 8080)\n")
	}
	flag.Parse()

	// Set up structured logging
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	// Resolve credentials
	effectiveAppID := *appID
	effectiveAppKey := *appKeyPath
	if effectiveAppID == "" {
		effectiveAppID = os.Getenv("GITHUB_APP_ID")
	}
	if effectiveAppKey == "" {
		effectiveAppKey = os.Getenv("GITHUB_APP_KEY_PATH")
	}

	// Validate credentials
	if effectiveAppID == "" {
		slog.Error("GitHub App ID is required")
		slog.Info("Set via --app-id flag or GITHUB_APP_ID environment variable")
		os.Exit(1)
	}
	// Note: GITHUB_APP_KEY will be checked via gsm.Secret in auth.go
	if effectiveAppKey == "" {
		slog.Info("No GITHUB_APP_KEY_PATH provided, will attempt to use GITHUB_APP_KEY from Google Secret Manager")
	}

	ctx := context.Background()

	// Create GitHub client with app authentication
	cfg := github.Config{
		UseAppAuth:  true,
		AppID:       effectiveAppID,
		AppKeyPath:  effectiveAppKey,
		HTTPTimeout: 30 * time.Second,
		CacheTTL:    24 * time.Hour,
	}
	client, err := github.New(ctx, cfg)
	if err != nil {
		slog.Error("Failed to create GitHub client", "error", err)
		os.Exit(1)
	}

	// Get token for prx client
	token, err := client.Token(ctx)
	if err != nil {
		slog.Error("Failed to get GitHub token for prx client", "error", err)
		os.Exit(1)
	}

	// Create prx client for enhanced PR data (includes CI status)
	prxClient := prx.NewClient(token, prx.WithLogger(logger))

	// Wrap prx client to satisfy interface
	client.SetPrxClient(&prxClientWrapper{client: prxClient})

	// Create reviewer finder
	finderCfg := reviewer.Config{
		PRCountCache: *prCountCache,
	}
	finder := reviewer.New(client, finderCfg)

	bot := &Bot{
		client:            client,
		finder:            finder,
		sprinklerMonitors: make(map[string]*sprinklerMonitor),
		dryRun:            *dryRun,
		minOpenTime:       *minOpenTime,
		maxOpenTime:       *maxOpenTime,
	}

	slog.Info("Starting in server mode", "loop_delay", *loopDelay)
	bot.runServeMode(ctx, *loopDelay)
}

// Bot manages reviewer assignment across all installed organizations.
type Bot struct {
	client            *github.Client
	finder            *reviewer.Finder
	metrics           *MetricsCollector
	sprinklerMonitors map[string]*sprinklerMonitor // One monitor per org
	dryRun            bool
	minOpenTime       time.Duration
	maxOpenTime       time.Duration
}

// processAllOrgs processes all organizations where the GitHub app is installed.
func (b *Bot) processAllOrgs(ctx context.Context) error {
	orgs, err := b.client.ListAppInstallations(ctx)
	if err != nil {
		return fmt.Errorf("failed to list app installations: %w", err)
	}

	if len(orgs) == 0 {
		slog.Info("No organization installations found")
		return nil
	}

	slog.Info("Processing organizations", "count", len(orgs))

	var totalProcessed, totalAssigned, totalSkipped int

	for i, orgName := range orgs {
		slog.Info("Processing organization", "org", orgName, "progress", fmt.Sprintf("%d/%d", i+1, len(orgs)))

		b.client.SetCurrentOrg(orgName)

		processed, assigned, skipped := b.processOrg(ctx, orgName)
		totalProcessed += processed
		totalAssigned += assigned
		totalSkipped += skipped

		if b.metrics != nil {
			b.metrics.RecordOrg(orgName)
		}

		b.client.SetCurrentOrg("")
	}

	slog.Info("Completed all organizations",
		"total_prs", totalProcessed,
		"assigned", totalAssigned,
		"skipped", totalSkipped,
		"orgs", len(orgs))

	return nil
}

// processSinglePR processes a single PR by owner, repo, and number (used by sprinkler).
func (b *Bot) processSinglePR(ctx context.Context, owner, repo string, prNumber int) error {
	// Fetch the PR
	pr, err := b.client.PullRequest(ctx, owner, repo, prNumber)
	if err != nil {
		return fmt.Errorf("failed to fetch PR: %w", err)
	}

	// Record metrics
	if b.metrics != nil {
		b.metrics.RecordPRSeen(owner, repo, prNumber)
	}

	// Process the PR
	wasAssigned := b.processPR(ctx, pr)
	if wasAssigned && b.metrics != nil {
		b.metrics.RecordPRModified(owner, repo, prNumber)
	}

	return nil
}

// processOrg processes all PRs for a single organization.
func (b *Bot) processOrg(ctx context.Context, org string) (processed, assigned, skipped int) {
	// Get all open PRs across all repos in the org using search API
	prs, err := b.client.OpenPullRequestsForOrg(ctx, org)
	if err != nil {
		slog.Warn("Failed to get PRs for org", "org", org, "error", err)
		return 0, 0, 0
	}

	for _, pr := range prs {
		processed++
		if b.metrics != nil {
			b.metrics.RecordPRSeen(org, pr.Repository, pr.Number)
		}

		wasAssigned := b.processPR(ctx, pr)
		if wasAssigned {
			assigned++
			if b.metrics != nil {
				b.metrics.RecordPRModified(org, pr.Repository, pr.Number)
			}
		} else {
			skipped++
		}
	}

	return processed, assigned, skipped
}

// processPR processes a single PR and assigns reviewers if appropriate.
func (b *Bot) processPR(ctx context.Context, pr *types.PullRequest) bool {
	// Skip draft PRs
	if pr.Draft {
		slog.Debug("Skipping draft PR", "pr", pr.Number, "repo", pr.Repository)
		return false
	}

	// Skip if PR already has reviewers
	if len(pr.Reviewers) > 0 {
		slog.Debug("Skipping PR with existing reviewers", "pr", pr.Number, "repo", pr.Repository)
		return false
	}

	// Check CI/test status and apply delays
	if !b.isPRReadyForReview(pr) {
		return false
	}

	// Check PR age constraints
	lastActivity := pr.LastCommit
	if pr.LastReview.After(lastActivity) {
		lastActivity = pr.LastReview
	}
	timeSinceActivity := time.Since(lastActivity)
	if timeSinceActivity < b.minOpenTime || timeSinceActivity > b.maxOpenTime {
		slog.Debug("Skipping PR outside time window", "pr", pr.Number, "repo", pr.Repository)
		return false
	}

	// Find reviewers
	candidates, err := b.finder.Find(ctx, pr)
	if err != nil {
		slog.Warn("Failed to find reviewers", "pr", pr.Number, "repo", pr.Repository, "error", err)
		return false
	}

	if len(candidates) == 0 {
		slog.Debug("No suitable reviewers found", "pr", pr.Number, "repo", pr.Repository)
		return false
	}

	// Assign top 2 reviewers only
	maxReviewers := 2
	if len(candidates) < maxReviewers {
		maxReviewers = len(candidates)
	}
	reviewers := make([]string, 0, maxReviewers)
	for i := 0; i < maxReviewers; i++ {
		reviewers = append(reviewers, candidates[i].Username)
	}

	if b.dryRun {
		slog.Info("Would assign reviewers (dry-run)",
			"pr", pr.Number,
			"repo", pr.Repository,
			"reviewers", reviewers)
		return true
	}

	if err := b.client.AddReviewers(ctx, pr.Owner, pr.Repository, pr.Number, reviewers); err != nil {
		slog.Error("Failed to assign reviewers",
			"pr", pr.Number,
			"repo", pr.Repository,
			"error", err)
		return false
	}

	slog.Info("Assigned reviewers",
		"pr", pr.Number,
		"repo", pr.Repository,
		"reviewers", reviewers)
	return true
}

// isPRReadyForReview checks if a PR is ready for reviewer assignment based on CI/test status.
// Returns false if tests are pending (wait 20 min) or failing (wait 90 min).
// Also enforces a minimum 2 minute wait since last update.
func (*Bot) isPRReadyForReview(pr *types.PullRequest) bool {
	timeSinceUpdate := time.Since(pr.UpdatedAt)

	// Always wait at least 2 minutes since last update before assigning reviewers
	const minWaitTime = 2 * time.Minute
	if timeSinceUpdate < minWaitTime {
		slog.Debug("Skipping PR - waiting for minimum time since last update",
			"pr", pr.Number,
			"repo", pr.Repository,
			"time_since_update", timeSinceUpdate.Round(time.Second),
			"wait_remaining", (minWaitTime - timeSinceUpdate).Round(time.Second))
		return false
	}

	switch pr.TestState {
	case "failing":
		// Wait 90 minutes after last update if tests are failing
		if timeSinceUpdate < 90*time.Minute {
			slog.Debug("Skipping PR with failing tests - waiting for fixes",
				"pr", pr.Number,
				"repo", pr.Repository,
				"test_state", pr.TestState,
				"time_since_update", timeSinceUpdate.Round(time.Minute),
				"wait_remaining", (90*time.Minute - timeSinceUpdate).Round(time.Minute))
			return false
		}
		slog.Info("Assigning reviewers to PR with failing tests after 90 minute grace period",
			"pr", pr.Number,
			"repo", pr.Repository,
			"test_state", pr.TestState,
			"time_since_update", timeSinceUpdate.Round(time.Minute))

	case "pending", "queued", "running":
		// Wait 20 minutes after last update if tests are pending
		if timeSinceUpdate < 20*time.Minute {
			slog.Debug("Skipping PR with pending tests - waiting for completion",
				"pr", pr.Number,
				"repo", pr.Repository,
				"test_state", pr.TestState,
				"time_since_update", timeSinceUpdate.Round(time.Minute),
				"wait_remaining", (20*time.Minute - timeSinceUpdate).Round(time.Minute))
			return false
		}
		slog.Info("Assigning reviewers to PR with pending tests after 20 minute grace period",
			"pr", pr.Number,
			"repo", pr.Repository,
			"test_state", pr.TestState,
			"time_since_update", timeSinceUpdate.Round(time.Minute))

	case "passing", "":
		// No delay for passing or unknown test states
		slog.Debug("PR has passing or no CI checks",
			"pr", pr.Number,
			"repo", pr.Repository,
			"test_state", pr.TestState)
	default:
		// Unknown test state - proceed with review assignment
		slog.Debug("PR has unknown test state",
			"pr", pr.Number,
			"repo", pr.Repository,
			"test_state", pr.TestState)
	}

	return true
}

// runServeMode runs the bot in server mode with periodic execution.
func (b *Bot) runServeMode(ctx context.Context, loopDelay time.Duration) {
	b.metrics = NewMetricsCollector()

	// Start health server in background
	go b.startHealthServer(ctx)

	time.Sleep(100 * time.Millisecond)
	slog.Info("Service started in server mode", "loop_delay", loopDelay)

	// Initialize and start one sprinkler monitor per org
	orgs, err := b.client.ListAppInstallations(ctx)
	if err != nil {
		slog.Warn("Failed to list organizations for sprinkler", "error", err)
	} else {
		for _, org := range orgs {
			// Create and start sprinkler for this org
			// Pass a token provider function that gets fresh tokens
			monitor := newSprinklerMonitor(b, org)
			if err := monitor.start(ctx); err != nil {
				slog.Error("Failed to start sprinkler for org", "org", org, "error", err)
				continue
			}
			b.sprinklerMonitors[org] = monitor
			slog.Info("Started sprinkler monitor", "org", org)
		}

		// Stop all monitors on shutdown
		defer func() {
			for org, monitor := range b.sprinklerMonitors {
				slog.Info("Stopping sprinkler monitor", "org", org)
				monitor.stop()
			}
		}()
	}

	// Run immediately, then loop
	for {
		select {
		case <-ctx.Done():
			slog.Info("Context cancelled, shutting down")
			return
		default:
			slog.Info("Starting reviewer assignment run")
			startTime := time.Now()

			if err := b.processAllOrgs(ctx); err != nil {
				slog.Error("Failed to process app installations", "error", err)
			}

			// Check for new/removed orgs and update sprinkler monitors
			b.updateSprinklerMonitors(ctx)

			b.metrics.RecordRunComplete()
			duration := time.Since(startTime)
			slog.Info("Run completed", "duration", duration, "sleep_duration", loopDelay)

			// Sleep with context cancellation
			timer := time.NewTimer(loopDelay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
				// Continue to next iteration
			}
		}
	}
}

// updateSprinklerMonitors checks for new/removed orgs and updates sprinkler monitors accordingly.
func (b *Bot) updateSprinklerMonitors(ctx context.Context) {
	orgs, err := b.client.ListAppInstallations(ctx)
	if err != nil {
		slog.Warn("Failed to list organizations for sprinkler update", "error", err)
		return
	}

	// Build set of current orgs
	currentOrgs := make(map[string]bool)
	for _, org := range orgs {
		currentOrgs[org] = true
	}

	// Stop monitors for removed orgs
	for org, monitor := range b.sprinklerMonitors {
		if !currentOrgs[org] {
			slog.Info("Stopping sprinkler for removed org", "org", org)
			monitor.stop()
			delete(b.sprinklerMonitors, org)
		}
	}

	// Start monitors for new orgs
	for _, org := range orgs {
		if _, exists := b.sprinklerMonitors[org]; exists {
			continue // Already monitoring
		}

		// Create and start sprinkler for this org
		monitor := newSprinklerMonitor(b, org)
		if err := monitor.start(ctx); err != nil {
			slog.Error("Failed to start sprinkler for new org", "org", org, "error", err)
			continue
		}

		b.sprinklerMonitors[org] = monitor
		slog.Info("Started sprinkler monitor for new org", "org", org)
	}
}

// startHealthServer starts the HTTP server for health checks.
func (b *Bot) startHealthServer(ctx context.Context) {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	http.HandleFunc("/_-_/health", func(w http.ResponseWriter, _ *http.Request) {
		stats := b.metrics.Stats()

		status := "ok"
		statusCode := http.StatusOK

		if stats.TotalRuns > 0 && time.Since(stats.LastRun) > 15*time.Minute {
			status = "stale"
			statusCode = http.StatusServiceUnavailable
		}

		response := fmt.Sprintf("%s - %d organizations, %d PRs seen, %d PRs modified (last: %s, runs: %d)\n",
			status, stats.Orgs, stats.PRsSeen, stats.PRsModified,
			stats.LastRun.Format(time.RFC3339), stats.TotalRuns)

		w.WriteHeader(statusCode)
		if _, err := w.Write([]byte(response)); err != nil {
			slog.Warn("Failed to write response", "error", err)
		}
	})

	http.HandleFunc("/_-_/poll", func(w http.ResponseWriter, _ *http.Request) {
		if !b.metrics.pollingMu.TryLock() {
			w.WriteHeader(http.StatusConflict)
			if _, err := w.Write([]byte("Polling already in progress\n")); err != nil {
				slog.Warn("Failed to write response", "error", err)
			}
			return
		}

		b.metrics.isPolling = true

		// Start background polling with a detached context since HTTP request will complete
		// Use context.WithoutCancel to inherit values but allow goroutine to outlive handler
		go func() {
			pollCtx := context.WithoutCancel(ctx)
			defer func() {
				b.metrics.isPolling = false
				b.metrics.pollingMu.Unlock()
			}()

			slog.Info("Manual poll triggered")
			startTime := time.Now()

			if err := b.processAllOrgs(pollCtx); err != nil {
				slog.Error("Manual poll failed", "error", err)
			} else {
				b.metrics.RecordRunComplete()
				slog.Info("Manual poll completed", "duration", time.Since(startTime))
			}
		}()

		w.WriteHeader(http.StatusAccepted)
		if _, err := w.Write([]byte("Poll triggered\n")); err != nil {
			slog.Warn("Failed to write response", "error", err)
		}
	})

	http.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("Best Reviewer Bot\n/_-_/health - Health status\n/_-_/poll - Trigger manual poll\n")); err != nil {
			slog.Warn("Failed to write response", "error", err)
		}
	})

	slog.Info("Starting health server", "port", port)
	server := &http.Server{
		Addr:         ":" + port,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("Health server failed", "error", err)
	}
}

// NewMetricsCollector creates a new metrics collector.
func NewMetricsCollector() *MetricsCollector {
	return &MetricsCollector{
		uniqueOrgs:        make(map[string]bool),
		uniquePRsSeen:     make(map[string]bool),
		uniquePRsModified: make(map[string]bool),
	}
}

// RecordOrg records an organization being processed.
func (m *MetricsCollector) RecordOrg(org string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.uniqueOrgs[org] = true
}

// RecordPRSeen records a PR that was seen.
func (m *MetricsCollector) RecordPRSeen(owner, repo string, prNumber int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := fmt.Sprintf("%s/%s#%d", owner, repo, prNumber)
	m.uniquePRsSeen[key] = true
}

// RecordPRModified records a PR that was modified.
func (m *MetricsCollector) RecordPRModified(owner, repo string, prNumber int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := fmt.Sprintf("%s/%s#%d", owner, repo, prNumber)
	m.uniquePRsModified[key] = true
}

// RecordRunComplete records that a run has completed.
func (m *MetricsCollector) RecordRunComplete() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastRun = time.Now()
	atomic.AddInt64(&m.totalRuns, 1)
}

// Stats represents collected metrics.
type Stats struct {
	LastRun     time.Time
	TotalRuns   int64
	Orgs        int
	PRsSeen     int
	PRsModified int
}

// Stats returns the current statistics.
func (m *MetricsCollector) Stats() Stats {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return Stats{
		Orgs:        len(m.uniqueOrgs),
		PRsSeen:     len(m.uniquePRsSeen),
		PRsModified: len(m.uniquePRsModified),
		LastRun:     m.lastRun,
		TotalRuns:   atomic.LoadInt64(&m.totalRuns),
	}
}
