package tui

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/cmoro-deusto/llamaman/internal/llamaapi"
)

// capturedRecord is a minimal slog.Record projection. We keep only the
// level and message; the propsFetchedMsg handler attaches structured
// attrs but tests don't currently assert on them.
type capturedRecord struct {
	Level   slog.Level
	Message string
}

// capturingHandler is a slog.Handler that appends every record to a
// shared slice. Safe for concurrent use because the handler may be
// called from a tea.Cmd goroutine.
type capturingHandler struct {
	mu      *sync.Mutex
	records *[]capturedRecord
}

func (h *capturingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *capturingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	*h.records = append(*h.records, capturedRecord{Level: r.Level, Message: r.Message})
	return nil
}
func (h *capturingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *capturingHandler) WithGroup(string) slog.Handler      { return h }

// captureSlog redirects slog.Default() to an in-memory handler for the
// duration of a test, restoring the prior default in t.Cleanup.
// Returns the slice the handler is appending into; tests assert on it
// directly.
func captureSlog(t *testing.T) *[]capturedRecord {
	t.Helper()
	var (
		mu      sync.Mutex
		records []capturedRecord
	)
	handler := &capturingHandler{mu: &mu, records: &records}
	prev := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &records
}

// assertLogged fails the test unless at least one captured record
// matches both level and message exactly.
func assertLogged(t *testing.T, recs *[]capturedRecord, level slog.Level, msg string) {
	t.Helper()
	for _, r := range *recs {
		if r.Level == level && r.Message == msg {
			return
		}
	}
	t.Errorf("missing log record level=%v msg=%q; have: %+v", level, msg, *recs)
}

// assertNotLogged fails the test if any captured record matches both
// level and message — used to verify silent-on-cancel behavior.
func assertNotLogged(t *testing.T, recs *[]capturedRecord, level slog.Level, msg string) {
	t.Helper()
	for _, r := range *recs {
		if r.Level == level && r.Message == msg {
			t.Errorf("unexpected log record level=%v msg=%q", level, msg)
			return
		}
	}
}

// fakeFetcher is the test-side Fetcher. Behavior is parameterized so a
// single struct serves the "happy fetch", "first attempt fails / retry
// succeeds", and "always fails" cases. Phase 3 extends it with
// /metrics + /slots responders so RunMode tests can exercise the
// live-data path without standing up an httptest server per test.
type fakeFetcher struct {
	mu       sync.Mutex
	calls    int
	props    *llamaapi.Props
	err      error
	delay    time.Duration // optional, simulate slow server
	errFirst error         // returned on call 1, then fall through to props/err

	// Live-poll responders (zero values = ErrMetricsNotEnabled / empty
	// slot list / no error). Each call increments its own counter so
	// tests can verify the polling cadence.
	metrics       *llamaapi.Metrics
	metricsErr    error
	metricsCalls  int
	slots         *llamaapi.Slots
	slotsErr      error
	slotsCalls    int
	metricsScript []*llamaapi.Metrics // when non-nil, serve consecutive entries
}

func (f *fakeFetcher) FetchProps(ctx context.Context) (*llamaapi.Props, error) {
	f.mu.Lock()
	f.calls++
	n := f.calls
	f.mu.Unlock()
	if f.delay > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(f.delay):
		}
	}
	if n == 1 && f.errFirst != nil {
		return nil, f.errFirst
	}
	return f.props, f.err
}

func (f *fakeFetcher) FetchMetrics(_ context.Context) (*llamaapi.Metrics, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	idx := f.metricsCalls
	f.metricsCalls++
	if len(f.metricsScript) > 0 {
		if idx >= len(f.metricsScript) {
			idx = len(f.metricsScript) - 1
		}
		return f.metricsScript[idx], nil
	}
	return f.metrics, f.metricsErr
}

func (f *fakeFetcher) FetchSlots(_ context.Context) (*llamaapi.Slots, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.slotsCalls++
	return f.slots, f.slotsErr
}

func (f *fakeFetcher) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func propsWithNCtx(n int) *llamaapi.Props {
	p := &llamaapi.Props{}
	p.DefaultGenerationSettings.NCtx = n
	return p
}

func TestFetchPropsCmdHappy(t *testing.T) {
	fake := &fakeFetcher{props: propsWithNCtx(4096)}
	msg := fetchPropsCmd(context.Background(), fake)()
	got, ok := msg.(propsFetchedMsg)
	if !ok {
		t.Fatalf("expected propsFetchedMsg, got %T (%v)", msg, msg)
	}
	if got.err != nil {
		t.Errorf("err = %v, want nil", got.err)
	}
	if got.nctx != 4096 {
		t.Errorf("nctx = %d, want 4096", got.nctx)
	}
	if c := fake.callCount(); c != 1 {
		t.Errorf("FetchProps call count = %d, want 1 (no retry on success)", c)
	}
}

func TestFetchPropsCmdRetryAfterTransient(t *testing.T) {
	fake := &fakeFetcher{
		errFirst: errors.New("connection refused"),
		props:    propsWithNCtx(8192),
	}
	start := time.Now()
	msg := fetchPropsCmd(context.Background(), fake)()
	elapsed := time.Since(start)
	got := msg.(propsFetchedMsg)
	if got.err != nil {
		t.Errorf("err = %v, want nil after retry", got.err)
	}
	if got.nctx != 8192 {
		t.Errorf("nctx = %d, want 8192", got.nctx)
	}
	if c := fake.callCount(); c != 2 {
		t.Errorf("FetchProps call count = %d, want 2 (one retry)", c)
	}
	// Sanity: the retry sleep is fetchPropsRetryDelay (250ms). Allow
	// generous slack — CI can be slow — but the retry must have
	// actually waited.
	if elapsed < fetchPropsRetryDelay {
		t.Errorf("elapsed = %v, expected at least the retry delay (%v)", elapsed, fetchPropsRetryDelay)
	}
}

func TestFetchPropsCmdBothAttemptsFail(t *testing.T) {
	fake := &fakeFetcher{err: errors.New("connection refused")}
	msg := fetchPropsCmd(context.Background(), fake)()
	got := msg.(propsFetchedMsg)
	if got.err == nil {
		t.Fatal("expected err on permanent failure, got nil")
	}
	if c := fake.callCount(); c != 2 {
		t.Errorf("FetchProps call count = %d, want 2 (one retry)", c)
	}
}

// ---- Phase 3: live-poll Cmd tests ----

func TestFetchMetricsCmdHappy(t *testing.T) {
	fake := &fakeFetcher{metrics: &llamaapi.Metrics{TokensPredictedTotal: 42}}
	msg := fetchMetricsCmd(context.Background(), fake)()
	got, ok := msg.(metricsFetchedMsg)
	if !ok {
		t.Fatalf("expected metricsFetchedMsg, got %T", msg)
	}
	if got.err != nil || got.m == nil || got.m.TokensPredictedTotal != 42 {
		t.Errorf("unexpected msg: %+v", got)
	}
}

func TestFetchMetricsCmdSurfacesError(t *testing.T) {
	fake := &fakeFetcher{metricsErr: llamaapi.ErrMetricsNotEnabled}
	msg := fetchMetricsCmd(context.Background(), fake)()
	got := msg.(metricsFetchedMsg)
	if !errors.Is(got.err, llamaapi.ErrMetricsNotEnabled) {
		t.Errorf("err = %v, want ErrMetricsNotEnabled", got.err)
	}
}

func TestFetchSlotsCmdHappy(t *testing.T) {
	fake := &fakeFetcher{slots: &llamaapi.Slots{Total: 4, BusyCount: 2}}
	msg := fetchSlotsCmd(context.Background(), fake)()
	got, ok := msg.(slotsFetchedMsg)
	if !ok {
		t.Fatalf("expected slotsFetchedMsg, got %T", msg)
	}
	if got.err != nil || got.s.BusyCount != 2 || got.s.Total != 4 {
		t.Errorf("unexpected msg: %+v", got)
	}
}

func TestFetchPropsCmdContextCancelledDuringRetry(t *testing.T) {
	// First attempt fails immediately; cancel before the retry sleep
	// elapses. The cmd must return promptly with ctx.Err().
	fake := &fakeFetcher{errFirst: errors.New("connection refused"), props: propsWithNCtx(1)}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan tea.Msg, 1)
	go func() { done <- fetchPropsCmd(ctx, fake)() }()

	// Give the cmd a moment to enter the select on time.After, then
	// cancel before fetchPropsRetryDelay elapses.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case msg := <-done:
		got := msg.(propsFetchedMsg)
		if !errors.Is(got.err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", got.err)
		}
	case <-time.After(fetchPropsRetryDelay + 200*time.Millisecond):
		t.Fatal("fetchPropsCmd did not return after cancel; cancellation is broken")
	}
}
