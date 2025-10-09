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
	eventChannelSize       = 100              // Buffer size for event channel
	eventDedupWindow       = 5 * time.Second  // Time window for deduplicating events
	eventMapMaxSize        = 1000             // Maximum entries in event dedup map
	eventMapCleanupAge     = 1 * time.Hour    // Age threshold for cleaning up old entries
	sprinklerMaxRetries    = 3                // Max retries for PR processing
	sprinklerMaxDelay      = 10 * time.Second // Max delay between retries
	connectionHealthCheck  = 2 * time.Minute  // Check connection health every 2 minutes
	connectionStaleTimeout = 5 * time.Minute  // Reconnect if no connection for 5 minutes
	maxReconnectAttempts   = 5                // Max reconnection attempts before giving up
	reconnectBackoff       = 30 * time.Second // Initial backoff between reconnection attempts
)

// sprinklerMonitor manages WebSocket event subscriptions for a single org.
type sprinklerMonitor struct {
	bot               *Bot
	client            *client.Client
	eventChan         chan string          // Channel for PR URLs that need processing
	lastEventMap      map[string]time.Time // Track last event per URL to dedupe
	lastConnectedAt   time.Time            // Last successful connection time
	lastEventAt       time.Time            // Last event received time (for health monitoring)
	org               string               // Organization this monitor is for
	reconnectAttempts int                  // Current reconnection attempt count
	stopChan          chan struct{}        // Channel to signal monitor should stop
	mu                sync.RWMutex
	isRunning         bool
	isConnected       bool // Track WebSocket connection status
	isStopped         bool // Track if monitor was explicitly stopped
}

// newSprinklerMonitor creates a new sprinkler monitor for a specific org.
func newSprinklerMonitor(bot *Bot, org string) *sprinklerMonitor {
	return &sprinklerMonitor{
		bot:          bot,
		org:          org,
		eventChan:    make(chan string, eventChannelSize),
		lastEventMap: make(map[string]time.Time),
		stopChan:     make(chan struct{}),
	}
}

// start begins monitoring for PR events for this org.
func (sm *sprinklerMonitor) start(ctx context.Context) error {
	sm.mu.Lock()
	if sm.isRunning {
		sm.mu.Unlock()
		slog.Info("Monitor already running", "component", "sprinkler", "org", sm.org)
		return nil
	}
	sm.isRunning = true
	sm.isStopped = false
	sm.mu.Unlock()

	slog.Info("Starting event monitor for org", "component", "sprinkler", "org", sm.org)

	// Start event processor
	go sm.processEvents(ctx)

	// Start connection manager with auto-reconnect
	go sm.manageConnection(ctx)

	// Start health monitor
	go sm.monitorHealth(ctx)

	slog.Info("Event monitor started successfully", "component", "sprinkler", "org", sm.org)
	return nil
}

// manageConnection manages the WebSocket connection with automatic reconnection.
func (sm *sprinklerMonitor) manageConnection(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("Connection manager panic", "component", "sprinkler", "org", sm.org, "panic", r)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			slog.Info("Context cancelled, stopping connection manager", "component", "sprinkler", "org", sm.org)
			return
		case <-sm.stopChan:
			slog.Info("Stop signal received, stopping connection manager", "component", "sprinkler", "org", sm.org)
			return
		default:
			sm.mu.RLock()
			stopped := sm.isStopped
			sm.mu.RUnlock()

			if stopped {
				return
			}

			if err := sm.connectWebSocket(ctx); err != nil {
				sm.mu.Lock()
				sm.reconnectAttempts++
				attempts := sm.reconnectAttempts
				sm.mu.Unlock()

				if attempts >= maxReconnectAttempts {
					slog.Error("Max reconnection attempts reached, giving up", "component", "sprinkler", "org", sm.org, "attempts", attempts)
					return
				}

				backoff := reconnectBackoff * time.Duration(attempts)
				if backoff > 5*time.Minute {
					backoff = 5 * time.Minute
				}

				slog.Warn("WebSocket connection failed, will retry",
					"component", "sprinkler",
					"org", sm.org,
					"attempt", attempts,
					"backoff", backoff,
					"error", err)

				select {
				case <-ctx.Done():
					return
				case <-sm.stopChan:
					return
				case <-time.After(backoff):
					continue
				}
			}

			// Connection succeeded, reset reconnect attempts
			sm.mu.Lock()
			sm.reconnectAttempts = 0
			sm.mu.Unlock()

			// Wait a bit before attempting to reconnect after a disconnect
			select {
			case <-ctx.Done():
				return
			case <-sm.stopChan:
				return
			case <-time.After(5 * time.Second):
			}
		}
	}
}

// connectWebSocket establishes a WebSocket connection.
func (sm *sprinklerMonitor) connectWebSocket(ctx context.Context) error {
	// Get fresh token - the client will automatically refresh if needed
	sm.bot.client.SetCurrentOrg(sm.org)
	token, err := sm.bot.client.Token(ctx)
	sm.bot.client.SetCurrentOrg("")
	if err != nil {
		return fmt.Errorf("failed to get token: %w", err)
	}

	config := client.Config{
		ServerURL:      "wss://" + client.DefaultServerAddress + "/ws",
		Token:          token,
		Organization:   sm.org,
		EventTypes:     []string{"pull_request"},
		UserEventsOnly: false,
		Verbose:        false,
		NoReconnect:    false,
		OnConnect: func() {
			sm.mu.Lock()
			sm.isConnected = true
			sm.lastConnectedAt = time.Now()
			sm.mu.Unlock()
			slog.Info("WebSocket connected", "component", "sprinkler", "org", sm.org)
		},
		OnDisconnect: func(err error) {
			sm.mu.Lock()
			wasConnected := sm.isConnected
			sm.isConnected = false
			sm.mu.Unlock()
			if err != nil && !errors.Is(err, context.Canceled) && wasConnected {
				slog.Warn("WebSocket disconnected", "component", "sprinkler", "org", sm.org, "error", err)
			}
		},
		OnEvent: func(event client.Event) {
			sm.handleEvent(event)
		},
	}

	wsClient, err := client.New(config)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}

	sm.mu.Lock()
	sm.client = wsClient
	sm.mu.Unlock()

	slog.Info("Starting WebSocket client", "component", "sprinkler", "org", sm.org)
	startTime := time.Now()

	// Start the client (blocking call)
	if err := wsClient.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn("WebSocket client stopped with error",
			"component", "sprinkler",
			"org", sm.org,
			"uptime", time.Since(startTime).Round(time.Second),
			"error", err)
		return err
	}

	slog.Info("WebSocket client stopped", "component", "sprinkler", "org", sm.org, "uptime", time.Since(startTime).Round(time.Second))
	return nil
}

// monitorHealth monitors connection health and triggers reconnection if needed.
func (sm *sprinklerMonitor) monitorHealth(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("Health monitor panic", "component", "sprinkler", "org", sm.org, "panic", r)
		}
	}()

	ticker := time.NewTicker(connectionHealthCheck)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-sm.stopChan:
			return
		case <-ticker.C:
			sm.mu.RLock()
			isConnected := sm.isConnected
			lastConnected := sm.lastConnectedAt
			lastEvent := sm.lastEventAt
			stopped := sm.isStopped
			sm.mu.RUnlock()

			if stopped {
				return
			}

			now := time.Now()

			// Log health status
			if isConnected {
				timeSinceConnect := now.Sub(lastConnected)
				var timeSinceEvent time.Duration
				if !lastEvent.IsZero() {
					timeSinceEvent = now.Sub(lastEvent)
				}
				slog.Info("Sprinkler health check - connected",
					"component", "sprinkler",
					"org", sm.org,
					"connected_for", timeSinceConnect.Round(time.Second),
					"time_since_last_event", timeSinceEvent.Round(time.Second))
			} else {
				// Not connected - check if we've been disconnected too long
				if !lastConnected.IsZero() {
					disconnectedFor := now.Sub(lastConnected)
					slog.Warn("Sprinkler health check - disconnected",
						"component", "sprinkler",
						"org", sm.org,
						"disconnected_for", disconnectedFor.Round(time.Second))

					// Force reconnection if disconnected for too long
					if disconnectedFor > connectionStaleTimeout {
						slog.Warn("Connection stale, forcing reconnection",
							"component", "sprinkler",
							"org", sm.org,
							"disconnected_for", disconnectedFor.Round(time.Second))

						// Stop current client if any
						sm.mu.RLock()
						wsClient := sm.client
						sm.mu.RUnlock()

						if wsClient != nil {
							wsClient.Stop()
						}
					}
				} else {
					slog.Info("Sprinkler health check - not yet connected",
						"component", "sprinkler",
						"org", sm.org)
				}
			}
		}
	}
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
	sm.lastEventAt = now // Track last event time for health monitoring

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
		slog.Error("Failed to process PR after retries",
			"component", "sprinkler",
			"owner", owner,
			"repo", repo,
			"pr", number,
			"elapsed", time.Since(startTime).Round(time.Millisecond),
			"error", err)
		return
	}

	slog.Info("Successfully processed PR",
		"component", "sprinkler",
		"owner", owner,
		"repo", repo,
		"pr", number,
		"elapsed", time.Since(startTime).Round(time.Millisecond))
}

// stop stops the sprinkler monitor.
func (sm *sprinklerMonitor) stop() {
	sm.mu.Lock()
	if !sm.isRunning {
		sm.mu.Unlock()
		return
	}

	slog.Info("Stopping event monitor", "component", "sprinkler", "org", sm.org)
	sm.isRunning = false
	sm.isStopped = true
	sm.mu.Unlock()

	// Signal all goroutines to stop
	close(sm.stopChan)

	// Close the client to stop the WebSocket connection
	sm.mu.RLock()
	wsClient := sm.client
	sm.mu.RUnlock()

	if wsClient != nil {
		wsClient.Stop()
	}

	slog.Info("Event monitor stopped", "component", "sprinkler", "org", sm.org)
}

// healthStatus returns the current health status of the monitor.
func (sm *sprinklerMonitor) healthStatus() map[string]any {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	status := map[string]any{
		"org":                sm.org,
		"is_running":         sm.isRunning,
		"is_connected":       sm.isConnected,
		"reconnect_attempts": sm.reconnectAttempts,
	}

	if !sm.lastConnectedAt.IsZero() {
		status["last_connected_at"] = sm.lastConnectedAt
		if sm.isConnected {
			status["connected_for"] = time.Since(sm.lastConnectedAt).Round(time.Second).String()
		} else {
			status["disconnected_for"] = time.Since(sm.lastConnectedAt).Round(time.Second).String()
		}
	}

	if !sm.lastEventAt.IsZero() {
		status["last_event_at"] = sm.lastEventAt
		status["time_since_last_event"] = time.Since(sm.lastEventAt).Round(time.Second).String()
	}

	return status
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
