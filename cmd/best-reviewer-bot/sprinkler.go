package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/codeGROOVE-dev/retry"
	"github.com/codeGROOVE-dev/sprinkler/pkg/client"
)

const (
	eventChannelSize    = 100              // Buffer size for event channel
	eventDedupWindow    = 5 * time.Second  // Time window for deduplicating events
	eventMapMaxSize     = 1000             // Maximum entries in event dedup map
	eventMapCleanupAge  = 1 * time.Hour    // Age threshold for cleaning up old entries
	sprinklerMaxRetries = 3                // Max retries for PR processing
	sprinklerMaxDelay   = 10 * time.Second // Max delay between retries
)

// sprinklerMonitor manages WebSocket event subscriptions for a single org.
type sprinklerMonitor struct {
	bot             *Bot
	client          *client.Client
	cancel          context.CancelFunc
	eventChan       chan string          // Channel for PR URLs that need processing
	lastEventMap    map[string]time.Time // Track last event per URL to dedupe
	lastConnectedAt time.Time            // Last successful connection time
	token           string               // Installation token for this org
	org             string               // Organization this monitor is for
	mu              sync.RWMutex
	isRunning       bool
	isConnected     bool // Track WebSocket connection status
}

// newSprinklerMonitor creates a new sprinkler monitor for a specific org.
func newSprinklerMonitor(bot *Bot, token, org string) *sprinklerMonitor {
	_, cancel := context.WithCancel(context.Background())
	return &sprinklerMonitor{
		bot:          bot,
		token:        token,
		org:          org,
		cancel:       cancel,
		eventChan:    make(chan string, eventChannelSize),
		lastEventMap: make(map[string]time.Time),
	}
}

// start begins monitoring for PR events for this org.
func (sm *sprinklerMonitor) start(ctx context.Context) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.isRunning {
		slog.Info("Monitor already running", "component", "sprinkler", "org", sm.org)
		return nil
	}

	slog.Info("Starting event monitor for org", "component", "sprinkler", "org", sm.org)

	config := client.Config{
		ServerURL:      "wss://" + client.DefaultServerAddress + "/ws",
		Token:          sm.token,
		Organization:   sm.org, // Monitor only this org
		EventTypes:     []string{"pull_request"},
		UserEventsOnly: false,
		Verbose:        false,
		NoReconnect:    false,
		OnConnect: func() {
			sm.mu.Lock()
			sm.isConnected = true
			sm.lastConnectedAt = time.Now()
			sm.mu.Unlock()
			slog.Info("WebSocket connected", "component", "sprinkler")
		},
		OnDisconnect: func(err error) {
			sm.mu.Lock()
			sm.isConnected = false
			sm.mu.Unlock()
			if err != nil && !errors.Is(err, context.Canceled) {
				slog.Warn("WebSocket disconnected", "component", "sprinkler", "error", err)
			}
		},
		OnEvent: func(event client.Event) {
			sm.handleEvent(event)
		},
	}

	wsClient, err := client.New(config)
	if err != nil {
		slog.Error("Failed to create WebSocket client", "component", "sprinkler", "error", err)
		return fmt.Errorf("create sprinkler client: %w", err)
	}

	sm.client = wsClient
	sm.isRunning = true

	slog.Info("Starting event processor goroutine", "component", "sprinkler")
	// Start event processor
	go sm.processEvents(ctx)

	slog.Info("Starting WebSocket client goroutine", "component", "sprinkler")
	// Start WebSocket client with error recovery
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("WebSocket goroutine panic", "component", "sprinkler", "panic", r)
				sm.mu.Lock()
				sm.isRunning = false
				sm.mu.Unlock()
			}
		}()

		startTime := time.Now()
		if err := wsClient.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("WebSocket client error", "component", "sprinkler", "uptime", time.Since(startTime).Round(time.Second), "error", err)
			sm.mu.Lock()
			sm.isRunning = false
			sm.mu.Unlock()
		} else {
			slog.Info("WebSocket client stopped gracefully", "component", "sprinkler", "uptime", time.Since(startTime).Round(time.Second))
		}
	}()

	slog.Info("Event monitor started successfully", "component", "sprinkler")
	return nil
}

// handleEvent processes incoming PR events.
func (sm *sprinklerMonitor) handleEvent(event client.Event) {
	// Filter by event type
	if event.Type != "pull_request" {
		return
	}

	if event.URL == "" {
		slog.Warn("Received PR event with empty URL", "component", "sprinkler")
		return
	}

	// Extract org from URL (format: https://github.com/org/repo/pull/123)
	parts := strings.Split(event.URL, "/")
	const minParts = 5
	if len(parts) < minParts || parts[2] != "github.com" {
		slog.Warn("Failed to extract org from URL", "component", "sprinkler", "url", event.URL, "org", sm.org)
		return
	}
	org := parts[3]

	// Verify this event is for our org (should always match due to sprinkler config)
	if org != sm.org {
		slog.Debug("Ignoring event for different org", "component", "sprinkler", "event_org", org, "monitor_org", sm.org)
		return
	}

	// Dedupe events - only process if we haven't seen this URL recently
	sm.mu.Lock()
	lastSeen, exists := sm.lastEventMap[event.URL]
	now := time.Now()
	if exists && now.Sub(lastSeen) < eventDedupWindow {
		sm.mu.Unlock()
		return
	}
	sm.lastEventMap[event.URL] = now

	// Clean up old entries to prevent memory leak
	if len(sm.lastEventMap) > eventMapMaxSize {
		cutoff := now.Add(-eventMapCleanupAge)
		for url, timestamp := range sm.lastEventMap {
			if timestamp.Before(cutoff) {
				delete(sm.lastEventMap, url)
			}
		}
	}
	sm.mu.Unlock()

	slog.Info("PR event received", "component", "sprinkler", "url", event.URL, "org", sm.org)

	// Send to event channel for processing (non-blocking)
	select {
	case sm.eventChan <- event.URL:
	default:
		slog.Warn("Event channel full, dropping event", "component", "sprinkler", "url", event.URL)
	}
}

// processEvents handles PR events by checking and processing them.
func (sm *sprinklerMonitor) processEvents(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("Event processor panic", "component", "sprinkler", "panic", r)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case prURL := <-sm.eventChan:
			sm.processEvent(ctx, prURL)
		}
	}
}

// processEvent processes a single PR event.
func (sm *sprinklerMonitor) processEvent(ctx context.Context, prURL string) {
	startTime := time.Now()

	// Parse PR URL to extract owner, repo, and number
	owner, repo, number, err := parsePRURL(prURL)
	if err != nil {
		slog.Warn("Failed to parse PR URL", "component", "sprinkler", "url", prURL, "error", err)
		return
	}

	slog.Info("Processing PR event", "component", "sprinkler", "owner", owner, "repo", repo, "pr", number)

	// Set the current org for the GitHub client
	sm.bot.client.SetCurrentOrg(owner)
	defer sm.bot.client.SetCurrentOrg("")

	// Process the PR with retry logic
	err = retry.Do(func() error {
		return sm.bot.processSinglePR(ctx, owner, repo, number)
	},
		retry.Attempts(sprinklerMaxRetries),
		retry.DelayType(retry.CombineDelay(retry.BackOffDelay, retry.RandomDelay)),
		retry.MaxDelay(sprinklerMaxDelay),
		retry.OnRetry(func(n uint, err error) {
			slog.Info("Retrying PR processing", "component", "sprinkler", "attempt", n+1, "owner", owner, "repo", repo, "pr", number, "error", err)
		}),
		retry.Context(ctx),
	)
	if err != nil {
		slog.Error("Failed to process PR after retries", "component", "sprinkler", "owner", owner, "repo", repo, "pr", number, "elapsed", time.Since(startTime).Round(time.Millisecond), "error", err)
		return
	}

	slog.Info("Successfully processed PR", "component", "sprinkler", "owner", owner, "repo", repo, "pr", number, "elapsed", time.Since(startTime).Round(time.Millisecond))
}

// stop stops the sprinkler monitor.
func (sm *sprinklerMonitor) stop() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if !sm.isRunning {
		return
	}

	slog.Info("Stopping event monitor", "component", "sprinkler")
	sm.cancel()
	sm.isRunning = false
}

// parsePRURL extracts owner, repo, and PR number from URL.
// URL format: https://github.com/owner/repo/pull/123
func parsePRURL(url string) (owner, repo string, number int, err error) {
	const minParts = 7
	parts := strings.Split(url, "/")
	if len(parts) < minParts || parts[2] != "github.com" {
		return "", "", 0, fmt.Errorf("invalid GitHub PR URL format: %s", url)
	}

	owner = parts[3]
	repo = parts[4]

	_, scanErr := fmt.Sscanf(parts[6], "%d", &number)
	if scanErr != nil {
		return "", "", 0, fmt.Errorf("invalid PR number in URL: %s", url)
	}

	return owner, repo, number, nil
}
