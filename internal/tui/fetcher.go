package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/cmoro-deusto/llamaman/internal/llamaapi"
)

// Fetcher is the subset of llamaapi.Client that RunMode needs. The
// interface lives here (next to its consumer) so RunMode tests can
// inject fakes without depending on internal/llamaapi at all and the
// HTTP transport stays decoupled from the TUI layer.
type Fetcher interface {
	FetchProps(ctx context.Context) (*llamaapi.Props, error)
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
