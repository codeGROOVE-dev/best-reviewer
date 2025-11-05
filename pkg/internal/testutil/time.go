package testutil

import (
	"time"

	"github.com/codeGROOVE-dev/best-reviewer/pkg/github"
)

// MockTimeProvider implements github.TimeProvider for testing.
type MockTimeProvider struct {
	CurrentTime time.Time
	AfterChan   chan time.Time
	TickerChan  chan time.Time
}

// NewMockTimeProvider creates a new MockTimeProvider with the given current time.
func NewMockTimeProvider(now time.Time) *MockTimeProvider {
	return &MockTimeProvider{
		CurrentTime: now,
		AfterChan:   make(chan time.Time, 1),
		TickerChan:  make(chan time.Time, 100),
	}
}

// Now returns the configured current time.
func (m *MockTimeProvider) Now() time.Time {
	return m.CurrentTime
}

// After returns a channel that will receive the current time after the duration.
func (m *MockTimeProvider) After(_ time.Duration) <-chan time.Time {
	return m.AfterChan
}

// NewTicker creates a mock ticker.
func (m *MockTimeProvider) NewTicker(_ time.Duration) github.Ticker {
	return &MockTicker{c: m.TickerChan}
}

// Advance advances the mock time by the given duration.
func (m *MockTimeProvider) Advance(d time.Duration) {
	m.CurrentTime = m.CurrentTime.Add(d)
}

// MockTicker implements github.Ticker for testing.
type MockTicker struct {
	c       chan time.Time
	stopped bool
}

// C returns the ticker channel.
func (m *MockTicker) C() <-chan time.Time {
	return m.c
}

// Stop stops the ticker.
func (m *MockTicker) Stop() {
	m.stopped = true
}

// Tick sends a tick on the channel.
func (m *MockTicker) Tick(t time.Time) {
	if !m.stopped {
		m.c <- t
	}
}

// RealTimeProvider implements github.TimeProvider using real time.
type RealTimeProvider struct{}

// Now returns the current time.
func (*RealTimeProvider) Now() time.Time {
	return time.Now()
}

// After returns a channel that will receive the current time after the duration.
func (*RealTimeProvider) After(d time.Duration) <-chan time.Time {
	return time.After(d)
}

// NewTicker creates a real ticker.
func (*RealTimeProvider) NewTicker(d time.Duration) github.Ticker {
	return &RealTicker{Ticker: time.NewTicker(d)}
}

// RealTicker wraps time.Ticker to implement github.Ticker.
type RealTicker struct {
	*time.Ticker
}

// C returns the ticker channel.
func (r *RealTicker) C() <-chan time.Time {
	return r.Ticker.C
}
