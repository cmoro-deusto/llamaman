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
	// Router-mode endpoints. FetchModels returns the model list of
	// GET /models; FetchHealth the loaded-model ids of GET /health.
	// FetchSlotsFor / FetchMetricsFor return one model's stats via
	// /slots?model=<id> and /metrics?model=<id> (the router requires
	// the model name on these endpoints).
	FetchModels(ctx context.Context) (*llamaapi.Models, error)
	FetchHealth(ctx context.Context) (*llamaapi.Health, error)
	FetchSlotsFor(ctx context.Context, model string) (*llamaapi.Slots, error)
	FetchMetricsFor(ctx context.Context, model string) (*llamaapi.Metrics, error)
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

// fetchPropsRetryDelay is the gap between /props polling attempts.
// Used only as a safety fallback — the primary readiness signal is the
// log marker ("listening on") which fires the /props fetch at the right
// time. If the marker is missed (log truncation, weird server build),
// the poll loop catches it.
const fetchPropsRetryDelay = 5 * time.Second

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

// modelsFetchedMsg / healthFetchedMsg carry router-mode endpoint
// results (GET /models and GET /health) back into the Update loop.
type modelsFetchedMsg struct {
	m   *llamaapi.Models
	err error
}

type healthFetchedMsg struct {
	h   *llamaapi.Health
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

// fetchModelsCmd is a single one-shot GET /models (router mode).
func fetchModelsCmd(ctx context.Context, fetcher Fetcher) tea.Cmd {
	return func() tea.Msg {
		m, err := fetcher.FetchModels(ctx)
		return modelsFetchedMsg{m: m, err: err}
	}
}

// fetchHealthCmd is a single one-shot GET /health (router mode).
func fetchHealthCmd(ctx context.Context, fetcher Fetcher) tea.Cmd {
	return func() tea.Msg {
		h, err := fetcher.FetchHealth(ctx)
		return healthFetchedMsg{h: h, err: err}
	}
}

// routerSlotsMsg carries one model's /slots?model=<id> result.
type routerSlotsMsg struct {
	model string
	s     *llamaapi.Slots
	err   error
}

// fetchRouterSlotsCmd is a single one-shot per-model GET /slots
// (router mode; the router requires the model name on /slots).
func fetchRouterSlotsCmd(ctx context.Context, fetcher Fetcher, model string) tea.Cmd {
	return func() tea.Msg {
		s, err := fetcher.FetchSlotsFor(ctx, model)
		return routerSlotsMsg{model: model, s: s, err: err}
	}
}

// routerMetricsMsg carries one model's /metrics?model=<id> result.
type routerMetricsMsg struct {
	model string
	m     *llamaapi.Metrics
	err   error
}

// fetchRouterMetricsCmd is a single one-shot per-model GET /metrics
// (router mode). ErrMetricsNotEnabled (router started without
// --metrics) is handled by the RunMode handler, which stops polling.
func fetchRouterMetricsCmd(ctx context.Context, fetcher Fetcher, model string) tea.Cmd {
	return func() tea.Msg {
		m, err := fetcher.FetchMetricsFor(ctx, model)
		return routerMetricsMsg{model: model, m: m, err: err}
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

// fetchPropsCmd returns a tea.Cmd that polls GET /props in a loop
// until the server responds successfully or ctx is cancelled.
//
// Primary readiness path: the log marker ("listening on") detected in
// the logChunkMsg handler fires fetchPropsCmd once the HTTP server is
// up. That attempt succeeds immediately.
//
// Fallback path: if the marker is missed (log truncation, unusual
// server build), the poll loop retries every fetchPropsRetryDelay
// (5s) until success. Model loading (GGUF into memory, HF download)
// can take minutes; the 5s interval avoids hammering a port nothing
// is listening on.
//
// ctx cancellation (kill/detach) aborts the loop immediately.
// fetcher must be non-nil.
func fetchPropsCmd(ctx context.Context, fetcher Fetcher) tea.Cmd {
	return func() tea.Msg {
		for {
			p, err := fetcher.FetchProps(ctx)
			if err == nil {
				return propsFetchedMsg{nctx: p.DefaultGenerationSettings.NCtx}
			}
			// Server not ready yet (model still loading, HTTP listener
			// not up). Wait and retry. Sleep with cancellation so a
			// kill-during-fetch returns promptly.
			select {
			case <-ctx.Done():
				return propsFetchedMsg{err: ctx.Err()}
			case <-time.After(fetchPropsRetryDelay):
			}
		}
	}
}
