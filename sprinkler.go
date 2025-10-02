// Package main - sprinkler.go contains real-time event monitoring via WebSocket.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
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

// sprinklerMonitor manages WebSocket event subscriptions for all monitored orgs.
type sprinklerMonitor struct {
	finder          *ReviewerFinder
	client          *client.Client
	cancel          context.CancelFunc
	eventChan       chan string          // Channel for PR URLs that need processing
	lastEventMap    map[string]time.Time // Track last event per URL to dedupe
	lastConnectedAt time.Time            // Last successful connection time
	token           string
	orgs            []string
	mu              sync.RWMutex
	isRunning       bool
	isConnected     bool // Track WebSocket connection status
}

// newSprinklerMonitor creates a new sprinkler monitor for real-time PR events.
func newSprinklerMonitor(finder *ReviewerFinder, token string) *sprinklerMonitor {
	_, cancel := context.WithCancel(context.Background())
	return &sprinklerMonitor{
		finder:       finder,
		token:        token,
		orgs:         make([]string, 0),
		cancel:       cancel,
		eventChan:    make(chan string, eventChannelSize),
		lastEventMap: make(map[string]time.Time),
	}
}

// updateOrgs sets the list of organizations to monitor.
func (sm *sprinklerMonitor) updateOrgs(orgs []string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if len(orgs) == 0 {
		log.Print("[SPRINKLER] No organizations provided")
		return
	}

	log.Printf("[SPRINKLER] Setting organizations: %v (%d orgs)", orgs, len(orgs))
	sm.orgs = make([]string, len(orgs))
	copy(sm.orgs, orgs)
}

// start begins monitoring for PR events across all orgs.
func (sm *sprinklerMonitor) start(ctx context.Context) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.isRunning {
		log.Print("[SPRINKLER] Monitor already running")
		return nil // Already running
	}

	if len(sm.orgs) == 0 {
		log.Print("[SPRINKLER] No organizations to monitor, skipping start")
		return nil
	}

	log.Printf("[SPRINKLER] Starting event monitor for %d organizations", len(sm.orgs))

	config := client.Config{
		ServerURL:      "wss://" + client.DefaultServerAddress + "/ws",
		Token:          sm.token,
		Organization:   "*", // Monitor all orgs
		EventTypes:     []string{"pull_request"},
		UserEventsOnly: false,
		Verbose:        false,
		NoReconnect:    false,
		OnConnect: func() {
			sm.mu.Lock()
			sm.isConnected = true
			sm.lastConnectedAt = time.Now()
			sm.mu.Unlock()
			log.Print("[SPRINKLER] WebSocket connected")
		},
		OnDisconnect: func(err error) {
			sm.mu.Lock()
			sm.isConnected = false
			sm.mu.Unlock()
			if err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("[SPRINKLER] WebSocket disconnected: %v", err)
			}
		},
		OnEvent: func(event client.Event) {
			sm.handleEvent(event)
		},
	}

	wsClient, err := client.New(config)
	if err != nil {
		log.Printf("[ERROR] Failed to create WebSocket client: %v", err)
		return fmt.Errorf("create sprinkler client: %w", err)
	}

	sm.client = wsClient
	sm.isRunning = true

	log.Print("[SPRINKLER] Starting event processor goroutine")
	// Start event processor
	go sm.processEvents(ctx)

	log.Print("[SPRINKLER] Starting WebSocket client goroutine")
	// Start WebSocket client with error recovery
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[ERROR] WebSocket goroutine panic: %v", r)
				sm.mu.Lock()
				sm.isRunning = false
				sm.mu.Unlock()
			}
		}()

		startTime := time.Now()
		if err := wsClient.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("[ERROR] WebSocket client error (uptime: %v): %v",
				time.Since(startTime).Round(time.Second), err)
			sm.mu.Lock()
			sm.isRunning = false
			sm.mu.Unlock()
		} else {
			log.Printf("[SPRINKLER] WebSocket client stopped gracefully (uptime: %v)",
				time.Since(startTime).Round(time.Second))
		}
	}()

	log.Print("[SPRINKLER] Event monitor started successfully")
	return nil
}

// handleEvent processes incoming PR events.
func (sm *sprinklerMonitor) handleEvent(event client.Event) {
	// Filter by event type
	if event.Type != "pull_request" {
		return
	}

	if event.URL == "" {
		log.Print("[SPRINKLER] Received PR event with empty URL")
		return
	}

	// Extract org from URL (format: https://github.com/org/repo/pull/123)
	parts := strings.Split(event.URL, "/")
	const minParts = 5
	if len(parts) < minParts || parts[2] != "github.com" {
		log.Printf("[SPRINKLER] Failed to extract org from URL: %s", event.URL)
		return
	}
	org := parts[3]

	// Check if this org is in our monitored list
	sm.mu.RLock()
	monitored := false
	for _, o := range sm.orgs {
		if o == org {
			monitored = true
			break
		}
	}
	sm.mu.RUnlock()

	if !monitored {
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

	log.Printf("[SPRINKLER] PR event received: %s (org: %s)", event.URL, org)

	// Send to event channel for processing (non-blocking)
	select {
	case sm.eventChan <- event.URL:
	default:
		log.Printf("[SPRINKLER] Event channel full, dropping event: %s", event.URL)
	}
}

// processEvents handles PR events by checking and processing them.
func (sm *sprinklerMonitor) processEvents(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[ERROR] Event processor panic: %v", r)
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

	// Extract repo and number for logging
	repo, number := parseRepoAndNumberFromURL(prURL)
	if repo == "" || number == 0 {
		log.Printf("[SPRINKLER] Failed to parse PR URL: %s", prURL)
		return
	}

	log.Printf("[SPRINKLER] Processing PR event: %s #%d", repo, number)

	// Process the PR with retry logic
	err := retry.Do(func() error {
		pr, err := sm.finder.prFromURL(ctx, prURL)
		if err != nil {
			return fmt.Errorf("failed to get PR: %w", err)
		}

		// Process the PR for reviewer assignment
		_, err = sm.finder.processPR(ctx, pr)
		return err
	},
		retry.Attempts(sprinklerMaxRetries),
		retry.DelayType(retry.CombineDelay(retry.BackOffDelay, retry.RandomDelay)),
		retry.MaxDelay(sprinklerMaxDelay),
		retry.OnRetry(func(n uint, err error) {
			log.Printf("[SPRINKLER] Retrying PR processing (attempt %d): %s #%d - %v",
				n+1, repo, number, err)
		}),
		retry.Context(ctx),
	)
	if err != nil {
		log.Printf("[SPRINKLER] Failed to process PR after retries: %s #%d (elapsed: %v) - %v",
			repo, number, time.Since(startTime).Round(time.Millisecond), err)
		return
	}

	log.Printf("[SPRINKLER] Successfully processed PR: %s #%d (elapsed: %v)",
		repo, number, time.Since(startTime).Round(time.Millisecond))
}

// stop stops the sprinkler monitor.
func (sm *sprinklerMonitor) stop() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if !sm.isRunning {
		return
	}

	log.Print("[SPRINKLER] Stopping event monitor")
	sm.cancel()
	sm.isRunning = false
}

// parseRepoAndNumberFromURL extracts repo and PR number from URL.
func parseRepoAndNumberFromURL(url string) (repo string, number int) {
	// URL format: https://github.com/org/repo/pull/123
	const minParts = 7
	parts := strings.Split(url, "/")
	if len(parts) < minParts || parts[2] != "github.com" {
		return "", 0
	}

	repo = fmt.Sprintf("%s/%s", parts[3], parts[4])

	var n int
	_, err := fmt.Sscanf(parts[6], "%d", &n)
	if err != nil {
		return "", 0
	}

	return repo, n
}
