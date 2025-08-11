package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// MetricsCollector tracks metrics for the health endpoint.
type MetricsCollector struct {
	mu                sync.RWMutex
	uniqueOrgs        map[string]bool
	uniquePRsSeen     map[string]bool
	uniquePRsModified map[string]bool
	lastRun           time.Time
	totalRuns         int64
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
	Orgs        int
	PRsSeen     int
	PRsModified int
	LastRun     time.Time
	TotalRuns   int64
}

// GetStats returns the current statistics.
func (m *MetricsCollector) GetStats() Stats {
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

// startHealthServer starts the HTTP server for health checks.
func startHealthServer(metrics *MetricsCollector) {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	http.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		stats := metrics.GetStats()

		status := "ok"
		statusCode := http.StatusOK

		// If we haven't had a successful run in 15 minutes, report as unhealthy
		if stats.TotalRuns > 0 && time.Since(stats.LastRun) > 15*time.Minute {
			status = "stale"
			statusCode = http.StatusServiceUnavailable
		}

		response := fmt.Sprintf("%s - %d organizations served, %d PRs seen, %d PRs modified (last run: %s, total runs: %d)\n",
			status, stats.Orgs, stats.PRsSeen, stats.PRsModified, stats.LastRun.Format(time.RFC3339), stats.TotalRuns)

		w.WriteHeader(statusCode)
		if _, err := w.Write([]byte(response)); err != nil {
			log.Printf("[ERROR] Failed to write health response: %v", err)
		}
	})

	http.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("Better Reviewers Service\nHealth endpoint: /healthz\n")); err != nil {
			log.Printf("[ERROR] Failed to write response: %v", err)
		}
	})

	log.Printf("[SERVER] Starting health server on port %s", port)
	server := &http.Server{
		Addr:         ":" + port,
		ReadTimeout:  serverReadTimeout * time.Second,
		WriteTimeout: serverReadTimeout * time.Second,
		IdleTimeout:  serverIdleTimeout * time.Second,
	}

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("[ERROR] Failed to start health server: %v", err)
	}
}

// runServeMode runs the application in serve mode with periodic execution.
func (rf *ReviewerFinder) runServeMode(ctx context.Context, loopDelay time.Duration) {
	metrics := NewMetricsCollector()
	rf.metrics = metrics

	// Start health server in background
	go startHealthServer(metrics)

	// Give the server a moment to start
	time.Sleep(100 * time.Millisecond)
	log.Printf("[SERVER] Service started in serve mode with loop delay: %v", loopDelay)

	// Run immediately, then loop
	for {
		select {
		case <-ctx.Done():
			log.Print("[SERVER] Context cancelled, shutting down")
			return
		default:
			log.Print("[SERVER] Starting reviewer assignment run")
			startTime := time.Now()

			if err := rf.findAndAssignReviewersForApp(ctx); err != nil {
				log.Printf("[ERROR] Failed to process app installations: %v", err)
			}

			metrics.RecordRunComplete()
			duration := time.Since(startTime)
			log.Printf("[SERVER] Run completed in %v, sleeping for %v", duration, loopDelay)

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
