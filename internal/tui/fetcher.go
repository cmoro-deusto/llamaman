package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/cmoro-deusto/llamaman/internal/hwinfo"
	"github.com/cmoro-deusto/llamaman/internal/llamaapi"
)

// Fetcher is the subset of llamaapi.Client that RunMode needs. The
// interface lives here (next to its consumer) so RunMode tests can
// inject fakes without depending on internal/llamaapi at all and the
// HTTP transport stays decoupled from the TUI layer.
type Fetcher interface {
	FetchProps(ctx context.Context) (*llamaapi.Props, error)
	FetchMetrics(ctx context.Context) (*llamaapi.Metrics, error)
	FetchSlots(ctx context.Context) (*llamaapi.Slots, error)
}

// propsFetchedMsg carries the result of a one-shot /props fetch back
// into the Bubble Tea Update loop. Exactly one of nctx / err is
// meaningful; ctx-cancelled fetches surface as err == context.Canceled
// and the handler suppresses logging for that case so a detach during
// startup doesn't leave a noisy WARN line behind.
type propsFetchedMsg struct {
	nctx int
	err  error
}

// fetchPropsRetryDelay is the gap between the initial fetch and the one
// retry. ~250ms covers the realistic transient: server just hit the
// ready marker but the HTTP listener wiring isn't fully up yet.
const fetchPropsRetryDelay = 250 * time.Millisecond

// livePollInterval drives the run-mode header's live-data updates
// (server panel + hardware panel). 1s matches the existing uptime
// tick so the user sees a single coherent refresh cadence.
const livePollInterval = time.Second

// metricsFetchedMsg / slotsFetchedMsg / livePollTickMsg drive the
// live-data flow. The tick fires once per livePollInterval; on each
// tick RunMode dispatches FetchMetrics + FetchSlots + a hardware
// snapshot in parallel and waits for the results to land as their
// respective tea.Msg types.
type metricsFetchedMsg struct {
	m   *llamaapi.Metrics
	err error
}

type slotsFetchedMsg struct {
	s   *llamaapi.Slots
	err error
}

// hwSnapshotMsg carries the result of a periodic hwinfo.Snapshot.
// Defined here next to the other live-poll msgs so RunMode wires them
// through the same Update branch.
type hwSnapshotMsg struct {
	devices []hwinfo.Device
}

type livePollTickMsg time.Time

// hwTickMsg drives the hardware-panel poll cadence. Decoupled from
// livePollTickMsg so the Hardware panel can start populating
// immediately at RunMode birth — it doesn't need the server to be
// ready (gopsutil/NVML have no dependency on llama-server).
type hwTickMsg time.Time

// fetchMetricsCmd is a single one-shot fetch. Returns a metricsFetchedMsg.
func fetchMetricsCmd(ctx context.Context, fetcher Fetcher) tea.Cmd {
	return func() tea.Msg {
		m, err := fetcher.FetchMetrics(ctx)
		return metricsFetchedMsg{m: m, err: err}
	}
}

// fetchSlotsCmd is a single one-shot fetch. Returns a slotsFetchedMsg.
func fetchSlotsCmd(ctx context.Context, fetcher Fetcher) tea.Cmd {
	return func() tea.Msg {
		s, err := fetcher.FetchSlots(ctx)
		return slotsFetchedMsg{s: s, err: err}
	}
}

// hwSnapshotCmd performs a synchronous hwinfo.Snapshot in a tea.Cmd
// goroutine and emits the result. NVML/gopsutil reads finish in
// milliseconds on a typical desktop, but we wrap them in a Cmd
// anyway so a slow sysfs read can't block the Bubble Tea event loop.
func hwSnapshotCmd() tea.Cmd {
	return func() tea.Msg {
		devs, _ := hwinfo.Snapshot()
		return hwSnapshotMsg{devices: devs}
	}
}

// tickLivePoll fires once per livePollInterval. The next tick is
// scheduled by RunMode after each tick lands, so a kill-during-poll
// (which cancels the fetch context) also stops the cadence.
func tickLivePoll() tea.Cmd {
	return tea.Tick(livePollInterval, func(t time.Time) tea.Msg {
		return livePollTickMsg(t)
	})
}

// tickHwPoll is the hardware panel's independent ticker — fires at
// the same livePollInterval as the server poll but on its own
// schedule so it can start before (and continue independently of)
// the server-readiness gate.
func tickHwPoll() tea.Cmd {
	return tea.Tick(livePollInterval, func(t time.Time) tea.Msg {
		return hwTickMsg(t)
	})
}

// fetchPropsCmd returns a tea.Cmd that GETs /props once, retries once
// after fetchPropsRetryDelay on transient failure, and emits a
// propsFetchedMsg with the result. ctx cancellation aborts both
// attempts and any in-flight HTTP request. fetcher must be non-nil.
func fetchPropsCmd(ctx context.Context, fetcher Fetcher) tea.Cmd {
	return func() tea.Msg {
		p, err := fetcher.FetchProps(ctx)
		if err == nil {
			return propsFetchedMsg{nctx: p.DefaultGenerationSettings.NCtx}
		}
		// One retry. Sleep with cancellation so a kill-during-fetch
		// returns promptly.
		select {
		case <-ctx.Done():
			return propsFetchedMsg{err: ctx.Err()}
		case <-time.After(fetchPropsRetryDelay):
		}
		p, err = fetcher.FetchProps(ctx)
		if err == nil {
			return propsFetchedMsg{nctx: p.DefaultGenerationSettings.NCtx}
		}
		return propsFetchedMsg{err: err}
	}
}
