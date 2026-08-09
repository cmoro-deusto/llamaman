package tui

// Typed-repo check + quant offer for the model form's HF field (DESIGN
// §16.6 / ROADMAP §3.8 step B): when the user confirms a *typed, bare*
// org/repo id, one async tree/main call (the §16.2 client) checks
// existence and fetches the quant list with real sizes; on success the
// shared quant chooser (§16.3 data, §16.4 UI shape) offers the quants
// and the picked quant becomes the :quant suffix. Failures are
// non-blocking (P3): distinct flashes for not-found vs gated vs
// network, and the typed id is still saved — llama-server surfaces any
// problem at launch.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"

	"github.com/cmoro-deusto/llamaman/internal/hf"
)

// hfCheckRequestedMsg asks ConfigMode to run the typed-repo check. It
// is emitted by pickerInput when the user confirms (enter) the HF
// field with a typed, valid, bare id (§16.6 trigger), and is caught
// before the form branch — the enter is swallowed, so the form stays
// on the HF field until the check resolves.
type hfCheckRequestedMsg struct {
	id string // trimmed org/repo id, bare (no :quant suffix)
}

// hfCheckDoneMsg carries the async check result back to ConfigMode.
// gen ties the result to the check that produced it — a stale done msg
// from a canceled earlier check must never resolve a *later* one. err
// is context.Canceled when the user aborted with esc (no flash, no
// chooser — the field keeps its value).
type hfCheckDoneMsg struct {
	id     string
	gen    int
	opts   []hf.QuantOption
	mmproj bool
	err    error
}

// hfCheckRunner runs the one-round-trip repo check. The production
// adapter hfCheckClient composes the §16.2 client's Tree with the
// §16.3 quant grouping; tests inject a stub via SetHFCheckRunner. A
// nil runner disables the check (P3 — the form advances exactly as
// before).
type hfCheckRunner interface {
	CheckHF(ctx context.Context, repo string) ([]hf.QuantOption, bool, error)
}

// hfCheckClient is the production runner: one Tree(repo, "main") call
// yields existence, quants + sizes, and mmproj presence together —
// ROADMAP §3.8's "existence + quant list + sizes + LFS sha256 in a
// single round-trip". No internal/hf changes are needed: the pieces it
// composes are all exported.
type hfCheckClient struct{ c *hf.Client }

func (a hfCheckClient) CheckHF(ctx context.Context, repo string) ([]hf.QuantOption, bool, error) {
	files, err := a.c.Tree(ctx, repo, "main")
	if err != nil {
		// A cancellation must surface as such: the §16.2 client wraps
		// the transport error (including a canceled request) into an
		// hf.Error without Unwrap/Is, so errors.Is(ctx.Canceled) would
		// never match — the esc-cancel path depends on this.
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		return nil, false, err
	}
	return hf.Quants(files), hf.HasMMProj(files), nil
}

// hfCheckCmd runs the check in tea's goroutine (the same cmd-returns-a-
// msg shape as fetchPropsCmd) and reports back via hfCheckDoneMsg. The
// request is bounded by the §16.2 client's requestTimeout; esc cancels
// the ctx (P10). gen ties the result to its check so a stale done msg
// is dropped (handleHFCheckDone's guard).
func hfCheckCmd(ctx context.Context, runner hfCheckRunner, repo string, gen int) tea.Cmd {
	return func() tea.Msg {
		opts, mmproj, err := runner.CheckHF(ctx, repo)
		return hfCheckDoneMsg{id: repo, gen: gen, opts: opts, mmproj: mmproj, err: err}
	}
}

// hfCheckState is the in-flight repo check. While it is set, ConfigMode
// shields the form and renders the checking overlay; esc cancels. gen
// matches the hfCheckDoneMsg that resolves this check.
type hfCheckState struct {
	repo   string
	gen    int
	cancel context.CancelFunc
}

// checkingOverlay renders the static "checking org/repo…" popup.
// Static on purpose (no spinner): the check is one bounded HTTP call
// and static text keeps snapshot tests deterministic.
func checkingOverlay(theme Theme, repo string) string {
	box := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(theme.Accent).
		Padding(1, 2)
	return box.Render(strings.Join([]string{
		lipgloss.NewStyle().Bold(true).Render("checking " + repo + "…"),
		lipgloss.NewStyle().Foreground(theme.Subtle).Render("esc: cancel"),
	}, "\n"))
}

// formWidthFor sizes overlay forms to the window so long repo ids stay
// visible while typing (shared by the Storage manager's download
// action and the config editor's quant chooser — both hosts agree).
func formWidthFor(width int) int {
	return max(60, min(width-12, 160))
}

// quantKeepBare is the value of the quant chooser's trailing
// "keep <repo> (no quant)" row. It is a distinct non-empty sentinel
// (rather than "") on purpose: huh.Select anchors its initial cursor
// on the option whose value matches the bound value, and the hosts
// bind an empty string before opening — an empty keep-bare value would
// pre-select the keep-bare row and hide the quants.
const quantKeepBare = "\x01keep-bare"

// quantChooserForm builds the shared quant chooser (DESIGN §16.3 data,
// §16.4 UI shape): one row per quant — Tag — human size, with a
// (cached) marker for quants already on disk — plus an optional
// trailing "keep <repo> (no quant)" row (keepBare) whose value is the
// quantKeepBare sentinel, meaning "save the bare id". note is an extra
// informational Description line (e.g. the mmproj note); the
// Description is repo when empty. The picked value is written into
// value; the host applies theme/width by chaining With* after the call.
func quantChooserForm(repo string, opts []hf.QuantOption, cached map[string]bool, note string, value *string, keepBare bool) *huh.Form {
	choices := make([]huh.Option[string], 0, len(opts)+1)
	for _, q := range opts {
		label := fmt.Sprintf("%s — %s", q.Tag, hf.HumanSize(q.Size))
		if cached[q.Tag] {
			label += " (cached)"
		}
		choices = append(choices, huh.NewOption(label, q.Tag))
	}
	if keepBare {
		choices = append(choices, huh.NewOption("keep "+repo+" (no quant)", quantKeepBare))
	}
	desc := repo
	if note != "" {
		desc += "\n" + note
	}
	return huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title("quantization").
			Description(desc).
			Options(choices...).
			Value(value),
	))
}

// hfCheckFlash maps a check failure to its distinct non-blocking flash
// (ROADMAP §3.8: not-found vs gated vs network).
func hfCheckFlash(repo string, err error) string {
	var he *hf.Error
	switch {
	case hf.IsNotFound(err):
		return repo + ": not found on Hugging Face"
	case hf.IsGated(err):
		return repo + ": gated — requires HF_TOKEN"
	case errors.As(err, &he) && he.Kind == hf.ErrHTTP:
		return fmt.Sprintf("%s: HTTP %d", repo, he.Status)
	default:
		return repo + ": could not reach Hugging Face"
	}
}

// bareRepo reports whether an HF id carries no :quant suffix. The id
// is assumed to have passed hfFormValidator, so a ":" can only be the
// quant separator.
func bareRepo(id string) bool { return !strings.Contains(id, ":") }
