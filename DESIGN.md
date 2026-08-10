# llamaman — Design Document

This document captures the design decisions for `llamaman`, a TUI-based llama-server manager. It is the canonical reference for implementation.

The user-facing requirements are in `llamaman_specs.md`. Where this document and the spec disagree, this document wins (the spec contains a few inconsistencies and stale ideas that were resolved during design).

---

## 1. Overview

`llamaman` is a single-binary CLI utility for Linux that:

- Stores model definitions and llama-server launch presets in a JSON config file.
- Provides a modern TUI (Bubble Tea) for selecting, running, and configuring those models.
- Spawns `llama-server` as a managed child process, tails its output, and supports detach / reattach across `llamaman` invocations.
- Treats the config file as the single source of truth; edits are made through the TUI's configuration mode.

Repository: `github.com/cmoro-deusto/llamaman`

---

## 2. Stack & dependencies

| Concern | Choice |
|---|---|
| Language | Go |
| TUI framework | `github.com/charmbracelet/bubbletea` |
| Styling | `github.com/charmbracelet/lipgloss` |
| Widgets (list, viewport, textinput, …) | `github.com/charmbracelet/bubbles` |
| Forms (config mode) | `github.com/charmbracelet/huh` |
| CLI parser | `github.com/alecthomas/kong` |
| File watcher (log tailing) | `github.com/fsnotify/fsnotify` |
| Test helpers (TUI snapshots) | `github.com/charmbracelet/x/exp/teatest` |

No subcommand framework (Kong handles the flat CLI surface). No logger framework — `log/slog` to a file is sufficient.

---

## 3. Configuration

### 3.1 Location

- Default: `${XDG_CONFIG_HOME:-$HOME/.config}/llamaman/config.json`
- Override: `-c <path>` / `--config <path>`

### 3.2 Schema

```jsonc
{
  "version": 1,
  "globals": {
    "llama-server-bin": "/usr/local/bin/llama-server",
    "ip_address": "127.0.0.1",
    "port": 9080
  },
  "preferences": {          // optional; absent == defaults
    "theme": "auto",        // palette ID from the TUI table; "auto" is default
    "animations": true,      // default true; explicit false is honored
    "log-colors": true,      // default true; explicit false is honored (§15.3)
    "models-dir": ""        // llama.cpp HF cache root; "" = follow llama.cpp's chain (§16.1)
  },
  "models": [
    {
      "alias": "qwen3.6-27B",
      "location": "~/Code/ai/models/Qwen3.6-27B-Q4_K_XL.gguf",
      "presets": [...]
    },
    {
      "alias": "qwen-hf",
      "hf": "Qwen/Qwen3-32B-GGUF:Q4_K_M",
      "presets": [
        {
          "preset": "default",
          "description": "balanced settings",
          "params": {
            "ngl": 99,
            "ctx-size": 262144,
            "fa": "on",
            "temp": 0.6,
            "top-p": 0.95,
            "jinja": true,
            "no-mmproj": true
          }
        }
      ]
    }
  ]
}
```

- `version` is mandatory. Unknown versions → hard error (exit 2). No migration step exists today because only `version: 1` has ever shipped; when `version: 2` or higher is introduced, an automatic in-place migration runs at config-load time (see §12).
- `models` is a JSON array (the spec text says "object" — that's a typo).
- Each model has **exactly one** of `location` (a path to a local `.gguf` file, expanded with `~`/`$VAR` at load) or `hf` (a Hugging Face identifier in `org/repo[:quant]` form, passed verbatim to llama-server's `-hf`). Both empty or both filled → validation error.
- `presets` may be empty `[]`.
- `params` is an object; iteration order **must** be preserved (see §6.4).

### 3.3 Path expansion

Applied at config-load time to:
- `globals.llama-server-bin`
- every `models[].location`
- `preferences.models-dir` (Release 2, §16.1)

Expansions:
- Leading `~` → `$HOME` (via `os/user.Current()`)
- `$VAR` and `${VAR}` → environment lookup

Resolved values are used for all subsequent operations and error messages.

### 3.4 Save semantics

- Explicit save only (`s` keybinding in config mode).
- Atomic write: write `config.json.tmp` → `fsync` → rename `config.json` → `config.json.bak` → rename `config.json.tmp` → `config.json`.
- One rolling `.bak`.
- The `Esc` keybinding in config mode triggers a "Save / Discard / Cancel" prompt when there are unsaved changes.
- A `● modified` indicator appears in the status line when the in-memory config differs from disk.

### 3.5 Validation

- Per-field validation runs on form blur during editing (immediate feedback).
- Cross-field validation runs on save:
  - Alias uniqueness across models (case-sensitive).
  - Preset name uniqueness within a model (case-sensitive).
  - Each model has exactly one of `location` / `hf` set; both empty or both filled → error.
  - `hf` (when set) matches `^[\w.-]+/[\w.-]+(?::[\w.-]+)?$` (i.e., `org/repo[:quant]`) → error if not. No network reachability check; llama-server surfaces unreachable repos at launch.
- `models[].location` not existing on disk → warning, not blocking.
- `globals.llama-server-bin` not existing or not executable → warning, not blocking.
- Unknown param keys → warning, not blocking (consistent with §6.5).

---

## 4. CLI interface

### 4.1 Synopsis

```
llamaman [options] [<alias> [<preset>]]
llamaman import <file> [-c PATH]
llamaman export [<path>] [-c PATH]
```

### 4.2 Flags

| Short | Long | Action |
|---|---|---|
| `-h` | `--help` | Print help to stdout, exit 0. |
| | `--version` | Print `llamaman vX.Y.Z (commit, built date)` to stdout, exit 0. |
| `-l` | `--list` | List configured models, one per line: `<alias>\t(<source>)\t<n> presets[\t(running)]`. `<source>` is `local` or `hf`. Plain stdout, exit 0. |
| `-p` | `--presets` | With a following `<alias>`, print that model's presets to stdout. Exit 0. |
| `-c` | `--config` | Path to alternate config file. |
| | `--completion` | Takes `bash`, `zsh`, or `fish`; prints completion script to stdout, exit 0. |

Subcommands (dispatched before the positional dispatch table; see §13):

| Command | Action |
|---|---|
| `import <file>` | Merge a my-models.ini file into the config as models/presets (bootstrap the config if it does not exist). |
| `export [<path>]` | Serialize the config as a my-models.ini file (stdout when no path given). |

The help banner's first line: `llamaman vX.Y.Z llama-server manager` (per spec line 94).

### 4.3 Mode dispatch on launch

Order of evaluation:

1. `--help`, `--version`, `--completion`, `-l`, `-p` → plain stdout, no TUI, exit.
2. Read config (or trigger first-run if missing — §8).
3. Acquire `flock(2)` on `session.json` to determine if a session is already running.
4. Dispatch:

| Invocation | Session running? | Mode |
|---|---|---|
| `llamaman` (no positional args) | no | TUI main mode |
| `llamaman` (no positional args) | yes | TUI main mode (session reattach screen; attach with `a`/Enter) |
| `llamaman <alias>` | no, alias exists | TUI run mode (start fresh, default preset or only preset) |
| `llamaman <alias> <preset>` | no, both exist | TUI run mode (start fresh, named preset) |
| `llamaman <alias>` or `llamaman <alias> <preset>` | yes | TUI run mode (reattach, **arguments ignored**) |
| `llamaman <alias>` | no, alias missing | stderr error, exit 2 |
| `llamaman <alias> <preset>` | no, alias exists, preset missing | stderr error, exit 2 |

If two `llamaman` instances race to start a session, the loser sees `Another llamaman is already running` on stderr and exits 0.

A no-args launch with a session already running lands on **Main mode**, not run mode (owner decision): the session reattach screen (§15.2) makes the detached session visible, and `a`/Enter attach. `llamaman <alias>` with a running session still reattaches directly (arguments ignored).

### 4.4 Exit codes

| Code | Meaning |
|---|---|
| 0 | Clean exit |
| 1 | Generic / unexpected error |
| 2 | Config error (missing, malformed, schema, unknown alias/preset) |
| 3 | Execution prerequisites missing (binary or model file) |
| 4 | Port already in use |
| 130 | Interrupted (SIGINT) — conventional |

---

## 5. Process model & sessions

### 5.1 Spawning llama-server

- Command built per §6.
- Process launched via `os/exec` with a new session (`Setsid: true` in `SysProcAttr`) so it survives `llamaman`'s exit if the user detaches.
- `stdout` and `stderr` redirected to a single log file (see §5.3) opened with `O_CREATE|O_WRONLY|O_TRUNC` for fresh sessions, `O_APPEND` is never used by `llamaman` itself.

### 5.2 Session state

Path: `${XDG_RUNTIME_DIR:-/tmp/llamaman-$UID}/llamaman/session.json`

```jsonc
{
  "pid": 12345,
  "alias": "qwen3.6-27B",
  "preset": "default",
  "host": "127.0.0.1",
  "port": 9080,
  "started_at": "2026-05-01T13:11:42Z",
  "command": ["llama-server", "-m", "...", "--alias", "...", ...]
}
```

- A `flock(2)` on this file serializes session-start across `llamaman` instances.
- If the file exists but the PID is dead, treat as no session, silently clean up.
- Written by the process that spawns llama-server; read by reattaching processes.

### 5.3 Log file

Path: `${XDG_RUNTIME_DIR:-/tmp/llamaman-$UID}/llamaman/llama-server.log`

- Truncated when a fresh session starts.
- Preserved across detach/reattach.
- Removed when the user explicitly kills the server (quit → kill).
- Cleaned by the OS at reboot (since `XDG_RUNTIME_DIR` is wiped).
- Tailed via `fsnotify` (no polling).
- The TUI viewport reads the entire file on attach/reattach and follows new writes; the current implementation holds the whole file in memory (acceptable for realistic session sizes).

### 5.4 Session lifecycle

```
       fresh start                                         reattach
            │                                                  │
            ▼                                                  ▼
   write session.json ◄──────────  flock acquired ─────────► read session.json
   spawn llama-server                                          tail log file
   tail log file                                               (read-only)
            │
       ┌────┴────┐
       │         │
       ▼         ▼
   user q?  llama-server exits?
       │         │
   ┌───┴───┐    leave log + state for inspection
   │       │    user can q to clean up or r to restart
  kill   detach
   │       │
   SIGTERM, 5s grace, SIGKILL          (TUI exits, child orphaned)
   delete session.json + log           (state preserved)
```

### 5.5 Concurrency limits

- One running llama-server session at a time (single global port).
- Multiple `llamaman` instances may reattach concurrently (read-only on session state, both tail the same log file).
- Config edits are exclusive: an in-memory `flock` on `config.json` during configuration mode prevents two processes from writing simultaneously. Reads do not lock.

---

## 6. Parameter translation

### 6.1 Auto-added flags

In every spawn command, in this order:

```
<bin> {-m <location> | -hf <id>} --alias <alias> --host <host> <preset.params...> --port <port>
```

- The first slot is the model source: `-m <location>` for a local model, or `-hf <id>` for a Hugging Face identifier (chosen by which field is set on the model — exactly one, see §3.2).
- `--host` is **always** passed, even when it's `127.0.0.1` (explicit > implicit).
- If a preset's `params` contains a key that overlaps with an auto-added flag (`m`, `hf`, `alias`, `host`, `port`), the preset value wins and the corresponding auto-added entry is suppressed. This lets a preset redirect a model's source as the universal escape hatch.

### 6.2 Short vs long form inference

On `llamaman` startup (and whenever the binary's mtime changes), parse `llama-server --help` and build a map `{name → canonical_form}`. Cache it at `$XDG_CACHE_HOME/llamaman/flags-<bin-mtime>.json`.

If the binary is missing or its `--help` cannot be parsed, fall back to:
- Single-dash for keys in this hard-coded set: `m, n, c, t, s, b, h, p, ngl, ctk, ctv, fa, np, cb, hf, hff, hft, hfr, hfd, hfv`.
- Double-dash for everything else.

The `hf*` family is in the hard-coded set because `hf` is auto-emitted for HF-sourced models (§6.1) — its canonical form has to be correct even when the registry isn't available.

### 6.3 Boolean handling

- `"key": true` → emit `--key` (no value).
- `"key": false` → emit nothing.
- llama-server's `on`/`off`-valued flags (e.g. `--fa on`) are JSON strings (`"fa": "on"`), handled by string passthrough below.

### 6.4 Value passthrough

- Number → `--key 42` (two argv entries).
- String → `--key value` (two argv entries; the string is one argv element regardless of spaces, quotes, or JSON content — we exec directly with no shell, so no quoting is needed).
- Object or array → config error at load time.

### 6.5 Param order

JSON object key order is preserved via a custom `UnmarshalJSON` on the `Params` type (an ordered key/value slice under the hood). The order in the resulting argv matches the order in the source JSON, so users can group related flags for readability.

### 6.6 Validation

When a param key does not appear in the parsed `--help` flag set:
- Log a warning to the debug log.
- Display the warning in the run-mode top pane (line 3) and in the configuration-mode right pane.
- Do not block execution.

This keeps `llamaman` forward-compatible with new llama-server flags without requiring a release.

---

## 7. TUI modes

### 7.1 Global keybindings

| Key | Action |
|---|---|
| `?` | Toggle help overlay (lists current mode's keys) |
| `Esc` | Back / cancel modal |
| `Ctrl+C` | Same as `q` (with prompt in run mode) |
| `↑` / `↓` / `j` / `k` | Navigate lists (run mode is arrow-only — see §7.4) |
| `Enter` | Select / confirm |
| `/` | Filter (where applicable; the new-param picker also auto-enters filter mode on the first printable rune) |

Mouse: cell-motion mode. Wheel scrolls the focused viewport. Native click-drag selection requires Shift+drag (a known Bubble Tea trade-off).

Modal dialogs (quit prompt, kill confirm, restart confirm, help, config-mode forms) overlay the existing screen content using an ANSI-aware paste — the underlying view stays visible around the popup instead of being blanked.

### 7.2 Main mode

Centered window. Top: stylized "llamaman" wordmark (figlet-style, baked-in font, no runtime dependency). Below: version + tagline.

When at least one model is configured, a bordered single-line-per-row selection list is embedded directly in the landing screen between the version line and the shortcuts. The first model is pre-selected; the row is reverse-video. Each row shows a leading source tag (`local` / `hf` / `router`, in Muted — §15.2), the alias, an optional `(running)` marker, and a subtle preset-count summary. The highlighted model row with 2+ presets previews the preset names in its description, ellipsized to the row width (§15.2); the list box grows with wide terminals (cap 90 cols).

When **no** models are configured, the list is hidden and the screen reverts to its bare wordmark + shortcuts form so first-run users aren't confronted with an empty box.

| Key | Action |
|---|---|
| `↑` / `↓` | Move selection in the inline list (only when models exist) |
| `Enter` | Run the selected model. If the model has 0 or 1 presets it spawns directly; with 2+ presets the box pivots to a preset sub-list with the same Enter/Esc semantics |
| `Esc` | Back out of the preset sub-list to the model list |
| `c` | Configuration mode |
| `s` | Settings mode (theme, animations — edits `preferences`) |
| `t` / `Shift+t` | Cycle theme forward / backward (writes `preferences.theme`; `Shift+t` is not shown in the shortcut row) |
| `?` | Help overlay |
| `q` | Quit |
| `a` | Attach to running session (only shown when a session is running) |

If a session is running, Main shows a single reattach entry: `running <alias>/<preset> · listening on :<port>` (highlighted; §15.2). Enter/`a` attach; `q` quits llamaman leaving the server running.

Order: rows follow the configuration order (`models[]` in the JSON). No alphabetical sort — users who reorder via Shift+↑/↓ in configuration mode see the change reflected here.

A model alias with **zero** presets selected via Enter → run mode using only auto-added flags (`-m`, `--alias`, `--host`, `--port`).

There is no separate "selection mode" — model selection is the main mode.

### 7.4 Run mode

```
╭──────────────────────────────────────────────────────────────────────────────────╮
│ ▜  ▜                                                                             │
│ ▐  ▐  ▝▀▖ ...   Alias: alpha    Server: 8994     Context Size: 8192              │
│ ▐  ▐  ▞▀▌ ...   Preset: fast    Uptime: 00:01:30   [READY]                       │
│  ▘  ▘ ▝▀▘ ...                                                                    │
╰──────────────────────────────────────────────────────────────────────────────────╯
╭── llama-server ──────────────────────╮╭── Hardware ──────────────────────────────╮
│ Tokens ▁▂▃▅▇█▆▄▃ 80.0 /s  Busy 2/4   ││ [0] AMD Ryzen 9 7950X                    │
│ Prompt ▁▂▃▅▇█▆▄▃ 2331 /s  Queued 1   ││     Util  ▁▂▃▅▇█▆▄▃▂ 23.4%               │
│                                      ││     RAM   ▆▆▆ 41.0G / 64.0G ▆▆▆▆ 65.0%   │
│                                      ││     Power ▆▆▆ 32W / 125W ▆▆▆▆ 25.6%      │
│                                      ││     Temp  ▆▆▆ 68°C / 100°C ▆▆▆▆ 68.0%    │
│                                      ││ [0] NVIDIA GeForce RTX 4090              │
│                                      ││     Util  ▆▇████████▇▆▅ 89.0%            │
│                                      ││     VRAM  ▆▆▆ 21.1G / 24.0G ▆▆▆▆ 87.9%   │
│                                      ││     Power ▆▆▆ 320W / 450W ▆▆▆▆ 71.1%     │
│                                      ││     Temp  ▆▆▆ 72°C / 83°C ▆▆▆▆ 86.7% Fan65%│
╰──────────────────────────────────────╯╰──────────────────────────────────────────╯
╭─ output (tailing) ───────────────────────────────────────────────────────────────╮
│ main: HTTP server is listening, hostname: 127.0.0.1, port: 9080                  │
│ …                                                                                │
╰─ q: quit  k: kill  r: restart  c: copy  i: info  /: search  ?: help ─────────────╯
```

The header is a two-section block: a **top strip** carrying the
identity cells (alias, server version, ctx size, preset, uptime,
status badge) and a **live band** of two side-by-side panels
showing real-time data from the running server. Both sections are
fixed-shape — there is no graceful stacking and no word wrap;
content past the right edge truncates if the terminal is too narrow,
on the assumption that the user will widen the window.

**Layout state machine** (two states):

| Width | Layout |
|---|---|
| **≥ wordmarkMinWidth (90 cols)** | wordmark + 3 identity cells × 2 rows + live band |
| **< 90** | identity only (3 cells × 2 rows), no wordmark, no band |

The wide mode renders at full layout width regardless of terminal
size — the right edge of the live band will visually truncate
between roughly 90–115 cols where its content needs ~110 to fit
cleanly. That truncation is honest signal: widen the terminal.

**Wordmark**: the smblock-letterspaced llamaman wordmark
(31 cols × 4 rows) embedded from `internal/tui/wordmark.txt`.

**Identity cells**: `Alias`, `Server` (parsed `llama-server
--version`), `Context Size` (`/props.n_ctx` if available, else
preset value, else `n/a`), `Preset`, `Uptime`, status badge. The
badge is bracketed, bold, themed-foreground only — `[STARTING]` /
`[READY]` / `[EXITED]` / `[ERROR]` — no background fill, so it
works in both dark and light themes.

**Live band — `llama-server` panel** (SP3 shape): two content rows,
each with a 20-cell sparkline of the rate's last 30 seconds, the
current rate, and a secondary scalar.

```
Tokens <30s sparkline> 80.0 /s    Busy   2/4 slots
Prompt <30s sparkline> 2331 /s    Queued 1
```

Tokens/s and Prompt eval rates are sampled across two `/metrics`
ticks (Δtokens / Δseconds) and persisted: once the first non-zero
rate is observed, the trailing value latches and persists across
subsequent zero-delta ticks (so users see `80.0 /s` between
inference bursts instead of resetting to `—`). Before the first
non-zero rate, the trailing reads `—`. When `/metrics` is disabled
(preset doesn't set `metrics: true`), the rate reads `n/a`; busy
keeps working from `/slots`.

**Live band — `Hardware` panel** (T4 shape): five rows per device.

```
[N] <device name>
    Util  <30s sparkline>     XX.X%
    RAM   <bar with bytes overlay>     XX.X%
    Power <bar with W overlay>         XX.X%
    Temp  <bar with °C overlay>        XX.X%   [Fan XXrpm | Fan XX%]
```

CPU socket(s) come first (deduped by gopsutil `PhysicalID`), then
NVIDIA GPUs via NVML. Memory bar bytes overlay (M2): used/total
GiB centered inside the 20-cell bar; bar chars (`▆`, BC2) sit on
either side, color tells filled apart from empty. Power bar uses
CPU TDP from RAPL `constraint_0_max_power_uw` or NVML
`DeviceGetPowerManagementLimit`. Temp bar uses the throttle
ceiling (NVML SLOWDOWN threshold for GPU, gopsutil sensor
`Critical` for CPU). Fan slot is omitted entirely when not
available — never rendered as `n/a` (Bug 6 resolution).

**Color zones** (4-tier, threshold cuts in `internal/tui/zones.go`):

| Metric | Blue (idle) | Green (ok) | Yellow (warn) | Red (danger) |
|---|---|---|---|---|
| Util | 0–30% | 30–60% | 60–85% | >85% |
| RAM/VRAM | 0–30% | 30–70% | 70–90% | >90% |
| Power (% of max) | 0–30% | 30–70% | 70–90% | >90% |
| Temp (% of throttle) | 0–30% | 30–70% | 70–85% | >85% |

Color paints the bar fill chars + the trailing percentage value
(C1). Bytes-overlay text inside the bar stays neutral for legibility.
Sparklines are S1 (per-cell): each cell is colored by its own value's
zone, so a usage spike from blue → green → red shows mid-line.
Palette: dark-theme `StatusIdle = #7DC4E4`, light-theme `#3A7AAB`;
`StatusReady/Start/Err` reuse the run-mode status badge palette.

**Sparkline cadence**: each rate or device-util history is a 30-tick
ring buffer. Render compresses 30 samples into 20 visual cells via
integer-stride bucketing (alternating 1- and 2-sample buckets), so
older samples compress tighter than recent ones — newest live
transients on the right edge stay sharp.

**Polling**: a single 1s ticker (`livePollTickMsg`) drives the band.
Each tick fans out `/metrics` + `/slots` HTTP fetches plus a
`hwinfo.Snapshot()` call in parallel goroutines; results land as
their respective `tea.Msg` types. The fetch context is shared with
the `r.fetchCancel` cancellation, so detach/kill stops the cadence
immediately.

Status state machine: `starting → ready → exited|error`.
- `ready` is detected by matching the substring `server is listening` in stdout.
- `exited` is `process.Wait()` with exit code 0; `error` is non-zero.
- No auto-restart. The user presses `r` to restart manually (confirm modal if status is `ready`).

| Key | Action |
|---|---|
| `o` | Toggle log line-kind colors (persists to `preferences.log-colors`, §15.3) |
| `q` / `Ctrl+C` | Quit prompt: `(k)ill / (d)etach / (c)ancel`. `(k)ill` returns to the main screen; `(d)etach` exits llamaman and leaves llama-server running. |
| `k` | Direct kill shortcut (with `(y)es / (n)o` confirm). On confirm: stops llama-server, removes the log + session record, and returns to the main screen — llamaman itself stays open. |
| `r` | Restart server (confirm if currently ready) |
| `c` | Copy full launch command to clipboard (`wl-copy`, fallback `xclip`, fallback flash status) |
| `i` | Show model & preset detail overlay (alias + Source/HF + preset name + every preset param in source order). Any key closes. |
| `/` | Search forward in output. Live highlights (reverse video + bold) wrap matches as you type; `Enter` applies, `Esc` cancels. |
| `n` / `N` | Next / previous search match |
| `Esc` | Layered: close the router action menu → close router stats → clear an applied search → **detach to Main** (server keeps running, llamaman stays open; the final layer fires only while the session is live — a dead server keeps the crash view in control) |
| `g` / `G` | Jump to top / bottom |
| `↑` / `↓` / wheel | Scroll one line. `j`/`k` are **not** bound here so `k` is free for the kill shortcut. |
| `Space` / `b` | Page down / up |
| `?` | Help overlay |

**Info overlay** (`i`): centered modal showing the model alias +
`Source: <path>` or `HF: <id>` (whichever the model declares) +
preset name + every preset param in source order. The header
deliberately hides per-param detail (sampling knobs, etc.) so this
overlay is the on-demand read for the full configuration.
Implementation iterates `r.preset.Params` directly — see CLAUDE.md
*"Param order matters end-to-end"*.

Auto-scroll: locked to bottom unless the user has scrolled up. When scrolled up, a `↓ N new lines` indicator appears; `G` returns to live tail.

ANSI color codes from llama-server are passed through (Lip Gloss / `bubbles/viewport` render them).

**Render-time line coloring (§15.3):** the log viewport colorizes lines by kind — ERROR (red), WARN (yellow), TIMING (dim), the readiness marker `listening on` (green + bold) — and leaves INFO plain. Conservative classifier: worst case is an uncolored line, never a critical line shown as plain INFO. Coloring is render-time only: search matching, jump-to-match indices, scrollback positions, and the denoise toggle operate on the plain lines, and the on-disk log is byte-identical.

**Status badge glyphs (§15.3):** the `[READY]`-style badge gains a per-state glyph prefix — `● [READY]`, `◌ [STARTING]`, `✕ [ERROR]`, `◌ [EXITED]` — with the `[STARTING]` label text/format preserved.

**Load-progress indicator (§15.4).** While the server is starting, the
left panel (llama-server live-data box in single mode, the model list in
router mode) shows the load-progress block instead of its usual content:
the latest parsed phase (`loading model file` → `offloading layers to
GPU: 21/33` → download %) with an `Accent` block bar when a numeric
progress is known, or a static `loading…` before anything parses.
Tolerant classifier (§15.4): unknown lines change nothing, and format
drift degrades to the static text. The block stays visible for a
minimum of 2 s after the last phase line even once READY (`o`-style
toggle not applicable — it is load-window-only). Separate from the
`[STARTING]` badge, which is untouched.

**Transient flashes (§15.5, owner feedback).** Informational messages
(`match x/y`, `log colors off`, save errors, …) render **top-right
inside the run-mode header's blank space** (the first identity row),
right-aligned via `rightFlash`; the header box keeps its fixed height
and the footer stays a single static line, so **nothing on screen
moves** when a flash shows or hides. Oversized flashes truncate with an
ellipsis; flashes are dropped when no room exists.

**Subtle color animation (§15.5).** Gated by `preferences.animations`
(default on; the run-mode `a` quick key flips it live). Repeating:
the load-progress block breathes and, without numeric progress, shows
an indeterminate moving fill; the `[STARTING]` badge breathes
yellow↔gold; the READY dot pulses while generating; the Gen/Process
bars fill smoothly; the Tokens/Prompt sparklines glow softly while
busy. One-shot: a ready glow on STARTING→READY, a red flash on ERROR,
a pulse on the current search occurrence after `n`/`N`, a glow when
TTFT arrives, and a flash on router models that just loaded/unloaded.
**Frame rate (owner decision, §15.5).** The frame period is decided in
**one place**: `animTickInterval` in `internal/tui/anim.go`, set to
**60 fps** (owner decision, tried 10/15/30/60 and settled on 60).
Overridable at runtime via **`LLAMAMAN_ANIM_FPS`** (e.g. `60`, `30`,
`15`) without rebuilding. The tick still fires only while an animated
element is visible, so the cost stays bounded (§2.4).
(owner-amended from 4 for smoother motion);
truecolor lerp with a 3-step discrete fallback on 256-color (P1); a
frozen clock in tests (P9).

**Terminal title (§15.3):** `tea.SetWindowTitle` sets `llamaman — <alias> [STARTING]` at run-mode entry, `[READY]` on the ready transition, `[ERROR]`/`[EXITED]` on process exit; router runs use the models-file basename. Not restored on exit (v1).

Scrollback: unlimited, in-memory, sourced from the on-disk log file. The whole file is buffered.

### 7.5 Configuration mode

Three-pane master-detail:

```
┌─ Models ──────────┬─ Presets: <selected model> ──┬─ <selected preset> ─────────┐
│ ▶ qwen3.6-27B     │ ▶ default                    │ name:        default        │
│   llama-3.3-70b   │   smallctx                   │ description: balanced       │
│ ─────────────     │ ─────────────                │                             │
│   [+ new model]   │   [+ new preset]             │ Params:                     │
│                   │                              │   ngl              99       │
│ [g] globals       │                              │   ctx-size         262144   │
│                   │                              │   ...                       │
│                   │                              │   [+ add param]             │
└── Tab: pane ─ e: edit ─ c: dup ─ k: clone-to ─ d: del ─ s: save ─ Esc: back ───┘
```

`Tab` / `Shift+Tab` cycle focus across panes. `Right` / `Left` (and `l` / `h`) do the same — the user can navigate to any pane with arrow keys without lifting from the navigation cluster.

**Models pane**:
- `e` rename alias / change source (modal form: alias, source select [`local` | `huggingface`], then either a path input or a `org/repo[:quant]` input depending on the selection). Both value inputs stay free-type and are picker-assisted (§16.5): `ctrl+o` in the path input opens a `.gguf` filepicker (starting in `preferences.models-dir` → the current value's dir → the first local model's dir → `~`), and `ctrl+o` in the HF input opens a cached-repo list (one row per cached repo with quants + sizes, plus "type a new repo…"; a single cached quant pre-fills `org/repo:QUANT`, several pre-fill bare `org/repo`; an empty cache skips the list). Confirming a *typed* bare `org/repo` in the HF field runs one async `tree/main` check offering the shared quant chooser with real sizes; failures open a Save/Dismiss dialog with a distinct message — Save keeps the id, Dismiss returns to the field (§16.6).
- `n` new model (same modal as edit).
- `c` duplicate, prompt for new alias (presets and params copied; source kind and value preserved).
- `d` delete (confirm with preset count).
- `Shift+↑/↓` reorder (persisted in JSON).

**Presets pane**:
- `e` rename preset / edit description.
- `n` new preset (name + description; starts with empty params).
- `c` duplicate within the same model, prompt for new name.
- `k` clone to a different model — opens a form with a target-model select (every model except the source) and a new-name input (default `<src>-copy`). Submitting deep-copies the source preset's description and params into the chosen target. Cursor stays on the source row; flash names the target alias. When only one model exists the shortcut renders dimmed and pressing `k` is a no-op with the flash `no other model to clone to`.
- `d` delete (confirm).
- `Shift+↑/↓` reorder.

**Param editor (right pane)**:
- `e` or `Enter` on a row: inline edit value (type-aware, see below).
- `d` remove that param.
- `n` add new param. Opens a `bubbles/list`-based picker that shows each flag's bare key (no `-`/`--` prefix), kind hint (`(bool)`, `(numeric)`, `(enum: …)`, `(string)`), and the parsed help description on the right. Highlightable rows; `↑`/`↓` to navigate; **the user can just start typing** to enter filter mode (no separate `/`-then-prompt step). `Enter` picks; `Esc` cancels. When the binary registry is empty, falls back to a plain free-text input.
- After picking the name, the value input is type-aware:
  - boolean flag → yes/no toggle
  - numeric → numeric text input
  - enum (e.g., `ctk`/`ctv` against a known set: `f16, q8_0, q4_0, q4_1, q5_0, q5_1, ...`; or any `[a|b|c]` / `{a,b,c}` placeholder parsed from `--help`) → picker
  - other → text input
- `Shift+↑/↓` reorder.

Unknown-flag warnings (keys not in the parsed registry) appear in the right pane below the params, in addition to the run-mode top-pane warning surface.

**Globals form** (`g` from anywhere):

```
┌─ Globals ─────────────────────────────────┐
│ llama-server binary: /usr/local/bin/...   │
│ host:                127.0.0.1            │
│ port:                9080                 │
│                                           │
│ [save]  [cancel]                          │
└───────────────────────────────────────────┘
```

Validates: binary path is non-empty (warn if doesn't exist or isn't executable), host is a valid format (IPv4, IPv6 bracketed, or hostname), port is `1..65535`.

**Save**: `s`. Atomic write (§3.4).

**Entry points**:
- From main mode (`c`) → focus Models pane, top entry.
- From selection mode (`e` on a model) → focus Presets pane of that model.
- From preset sub-list (`e` on a preset) → focus right pane (param editor) for that preset.

**Exit**: `Esc`. With unsaved changes → "Save / Discard / Cancel" modal.

---

## 8. First-run flow

Triggered when no config file exists at the default location and `-c` was not given.

1. **Modal**: "No configuration found. llamaman will guide you through setup. [Enter] begin / [q] quit." Quit → exit 0 (intentional, not an error).
2. **Globals form** (mandatory, auto-opened, same layout as `g` form), prefilled:
   - `llama-server-bin`: autodetected. Order: `which llama-server` → `/usr/local/bin/llama-server` → `/usr/local/llama.cpp/bin/llama-server` → `/opt/llama.cpp/bin/llama-server`. If nothing found, field is empty + inline warning.
   - `host`: `127.0.0.1`.
   - `port`: `9080`.
   - Cancel → "Quit setup? Config will not be saved. (y/N)".
3. **First disk write**: on globals save, write `config.json` with the chosen globals and `models: []`. The config file does not exist on disk before this point.
4. **Drop into configuration mode** with a one-time banner: "First-time setup — globals saved. Press n in the Models pane to add your first model." Banner is dismissed on first `n` press or on quit, never reappears.
5. The user can quit at any time after step 3; the config is valid even with empty `models`.

`-c <path>` never triggers first-run. A missing config at a user-supplied path is a hard error (exit 2).

---

## 9. Errors and edge cases

| Condition | Behavior |
|---|---|
| Config file missing, no `-c` | First-run flow (§8) |
| Config file missing, `-c <path>` | stderr, exit 2 |
| Config file malformed JSON | stderr with line/col, hint about `.bak` if present, exit 2 |
| Schema validation failure | stderr listing every problem, exit 2 |
| Schema version mismatch | stderr, exit 2, suggest upgrade/downgrade |
| Binary path doesn't exist or isn't executable, run-mode launch | CLI: stderr, exit 3. TUI: error toast, no transition to run mode |
| Model `location` doesn't exist, run-mode launch | Same as above |
| Port already in use (TCP bind precheck on `host:port`) | CLI: stderr, exit 4. TUI: modal with `(g)oto globals / (p)reset port / (c)ancel` |
| Two `llamaman` processes try to start sessions | Loser: `Another llamaman is already running` to stderr, exit 0 |
| Stale `session.json` (PID dead) | Silent cleanup, treat as no session |
| Two `llamaman` processes both reattach | Allowed; both tail the same log file |

**TUI error display**:
- Non-blocking: status line at the bottom of the current screen, color-coded (yellow=warning, red=error).
- Blocking: modal that requires `Esc` or `Enter` to dismiss.

---

## 10. Engineering

### 10.1 Project layout

```
llamaman/
  go.mod
  main.go                       # entry: arg parse, mode dispatch
  internal/
    config/                     # load/save/validate, schema, atomic write, lock
    server/                     # spawn, supervise, log file mgmt, session state, flock
    flags/                      # llama-server --help parsing, name→canonical map, cache
    tui/
      main.go                   # main mode model
      selection.go              # selection mode model
      run.go                    # run mode model
      config.go                 # configuration mode model
      common.go                 # shared styles, key bindings, layout primitives
    paths/                      # XDG resolution, tilde+env expansion
  cmd/llamaman-fakeserver/      # test helper that mimics llama-server output
```

### 10.2 Versioning

- SemVer.
- Embedded at build time: `go build -ldflags "-X main.version=v0.1.0 -X main.commit=abc1234 -X main.date=2026-05-01"`.
- Initial release: `v0.1.0`.

### 10.3 Debug log

- Path: `${XDG_STATE_HOME:-$HOME/.local/state}/llamaman/llamaman.log`.
- Default level: INFO. Set `LLAMAMAN_DEBUG=1` for DEBUG.
- Rolled at 10MB, one prior file kept.
- Logged: config load events, session start/stop, `--help` parse outcomes, errors. Not surfaced in the TUI.

### 10.4 Theme

- `preferences.theme` selects a palette (default `auto`); the palette
  table and resolver live in `internal/tui/theme.go` (DESIGN §15.1).
  23 curated palettes: llamaman (the original hard-coded theme,
  background-adaptive), 11 dark + 11 light official counterparts.
  `auto` and `llamaman` resolve to the same adaptive pair.
- `lipgloss.HasDarkBackground()` picks the dark/light variant for
  adaptive palettes and drives the mismatch warning: a palette whose
  background differs from the terminal's is applied with a warning,
  never silently (owner decision — the user may override explicitly).
  An unknown theme degrades to `auto` with a Warning (never a Block).
- Every palette field carries its nearest xterm-256 index (computed
  with the standard 6×6×6-cube + grayscale approximation; ties resolve
  to the lower index) so 256-color SSH renders correctly.
- Status indicators: green ready, yellow starting, red error, gray
  exited (per-palette values).
- `NO_COLOR` env var honored (Lip Gloss handles automatically).

### 10.5 Shell completions

- Generated via Kong's completion support.
- Surfaced as `llamaman --completion <bash|zsh|fish>`, prints script to stdout.
- Documented install: `llamaman --completion fish > ~/.config/fish/completions/llamaman.fish`.

### 10.6 Testing

- Unit tests: config load/save/validate, flag short/long inference, param-to-argv translation, path expansion, `--help` parsing, ordered-map JSON round-trip.
- `cmd/llamaman-fakeserver`: test binary that prints fixture stdout (model load lines, "server is listening", periodic request logs) on a delay, then sleeps. Used by integration tests.
- TUI snapshot tests via `teatest` for the four modes' initial renders and key-driven transitions on the critical paths.
- No live tests against real `llama-server` in CI.

### 10.7 Distribution

- GitHub Releases via GoReleaser, prebuilt `linux/amd64` and `linux/arm64` binaries.
- `go install github.com/cmoro-deusto/llamaman@latest` as a fallback.
- AUR `llamaman-bin` PKGBUILD for Arch / CachyOS users.

---

## 11. Out of current scope

Explicitly deferred:

- Multiple concurrent sessions (single global port).
- `--detach` / `--no-tui` flags.
- Auto-restart on crash. (→ §14.3: planned, opt-in, attached-only.)
- Browser-open shortcut from run mode. (→ §14.3: planned.)
- Telemetry of any kind.
- Themes beyond auto light/dark. (→ §14.1: planned.)
- Search / sort options beyond filter + alphabetical.
- Recently-used sort.
- Disk-backed log paging (sessions are buffered fully in memory).
- Live editing of llama-server while running.

---

## 12. Future work

Items that are not in the current scope but are planned for a future release. Listed here (rather than under §11) because there is concrete intent to ship them; the design notes below capture decisions already taken so we don't relitigate them later.

### 12.1 Config schema migrations

**Goal**: when a future release introduces `version: 2` (or higher), older configs are migrated automatically at load time without user intervention.

**Trigger**: `Load` reads `version` first. If the value is below the binary's current schema version, the loader runs a chain of migration steps `v1→v2→v3→…→current`, each implemented as a function `(map[string]any) → map[string]any` operating on the parsed-but-not-typed JSON. Unknown future versions still error out (forward compatibility is not promised — older binaries reject newer configs, see §3.2).

**Safety**:
- Before writing the migrated file, save the original to `config.json.pre-vN.bak` (separate from the rolling `.bak` produced by configuration-mode saves, so a migration backup is never overwritten by a subsequent edit).
- Atomic write of the migrated config follows §3.4 (tmp → fsync → rename).
- A one-line INFO log entry per migration step records source/target version and any field renames applied.

**Surfacing**: on first launch after an upgrade that performs migration, the TUI shows a single non-blocking status-line notice ("Config migrated from v1 to v2 — backup at config.json.pre-v2.bak") that dismisses on the next keypress. CLI invocations (`-l`, `-p`) print the same notice to stderr.

**Out of scope for the migration system itself**: downgrade migrations (`v2→v1`). If the user downgrades, the older binary errors out as today (`json: unknown field`); they keep the `.pre-v2.bak` to recover from.

### 12.2 Main mode information density

**Goal**: rework how the centred Main mode window presents model and session information. The current layout (figlet wordmark + version line + single-row-per-model inline list with `(running)` marker and preset count) is functional but underuses the available space and surfaces only a fraction of what the user might want at a glance.

**What's likely to change** (not yet committed; recorded so the current layout isn't mistaken for the target):
- Per-row affordance for source kind (local `.gguf` vs HF identifier), so users can tell at a glance which models will hit the network on first launch.
- A more legible representation of the detached-session indicator than the current "extra line above the list" pattern — possibly a sticky header strip with attach affordance.
- Optional preset preview (e.g. expand the highlighted row to show preset names) instead of the current numeric `<n> presets` summary.
- Better use of horizontal space on wide terminals — today the centred window has fixed inner width regardless of terminal columns.

**Constraints to preserve**:
- The "no models configured" state stays minimal (wordmark + shortcuts, no empty list box) so first-run users aren't confronted with a hollow frame.
- Configuration-file row order remains the visible order — no implicit sort.
- All existing keybindings (§7.2) keep their meaning; new affordances are additive.
- `?` help overlay stays the canonical keybinding reference.

**Non-goals**: no server-side state changes (this is purely a presentation rework), no new TUI mode (Main remains the model-selection mode), no persistent per-user UI preferences as part of this rework.

---

## 13. llama.cpp model presets (my-models.ini)

llama.cpp (from mid-Dec 2025, PR #17859) ships a "model presets" feature:
INI files consumed by `llama-server`'s multi-model router mode via
`--models-preset PATH` (env `LLAMA_ARG_MODELS_PRESET`). llamaman treats
these files as a first-class alternative to its own `config.json`:

- **Import** (`llamaman import <file>`): my-models.ini → config.json
  models/presets.
- **Export** (`llamaman export [<path>]`): config.json → my-models.ini.
- **Router runs** (Phase 3+ of this feature): INI files registered in
  `globals.models-files` (or passed with `-i/--ini PATH`) become runnable
  entries that spawn `llama-server --models-preset <file>` — one process
  hosting every model in the file.

The format, parser, and serializer live in `internal/modelsini/`. The
grammar mirrors llama.cpp's own PEG parser in `common/preset.cpp`.

### 13.1 The INI format (as llama.cpp defines it)

- `[name]` sections; keys are CLI arg names without leading dashes
  (`ctx-size`, `ngl`, `hf`); values are raw strings.
- `[*]` is a global section applied to every model; `[default]` is the
  fallback for unmatched model ids. Keys before any header belong to
  `[default]`.
- Comments: `;` or `#` — a comment starts at the first `;`/`#` character
  in a line (whitespace-prefixed or glued to the value).
- Bools accept `true/false`, `on/off`, `enabled/disabled`, `1/0`
  (exact table from `common/arg.cpp`); a negated key (`no-mmap`) inverts.
- Structural errors (malformed lines, unknown keys) abort llama.cpp's
  parser. llamaman's parser errors on malformed structure only; unknown
  *keys* become warnings at import time (forward compatibility with newer
  llama-server flags).
- No quoting/escaping exists; `common_preset::to_ini` escapes newlines as
  backslash + newline, which is inherently lossy.

### 13.2 Import mapping (INI → config.json)

- Every section except `[*]` and `[default]` becomes one `Model`.
- Alias = first comma-part of the `alias` key, else the section name.
- `model`/`m` → `location`; `hf`/`hf-repo` → `hf`; a section setting both
  uses `hf` (llama.cpp prefers `hf_repo`) with a warning.
- Sections without either source key are skipped with a warning.
- `[*]` params are merged into every preset (section params win — the
  same cascade order llama.cpp applies).
- A section named `<alias>:<preset>` with an explicit alias key imports
  as preset `<preset>` (the exporter's multi-preset convention);
  otherwise the preset is named `default`.
- Values are typed via the flag registry: bool flags use the truthiness
  table, numeric flags become `json.Number`, everything else is a string.
  Invalid values for a bool/numeric flag are dropped with a warning.
- `version` (reserved) and preset-only keys (`load-on-startup`,
  `stop-timeout`) are dropped — the latter with a warning; they are not
  llama-server CLI flags.
- Collisions: an imported alias that already exists in config.json is
  renamed with an `-ini` provenance suffix (`foo` → `foo-ini` →
  `foo-ini-2`). Sections *within one import* that share an alias merge as
  additional presets on one model — this is what makes export of
  multi-preset models round-trip.
- Descriptions are recovered from `; description: <text>` comments (the
  exporter's convention).

### 13.3 Export mapping (config.json → INI)

- One section per (model, preset): `[alias]` for single-preset models,
  `[alias:preset]` when a model has several.
- Every section carries explicit `model`/`hf` and `alias` keys so import
  is unambiguous regardless of the section name (which is decorative for
  llamaman round-trips and a model id for llama.cpp's router).
- Preset descriptions become `; description:` comments — llama.cpp
  rejects unknown keys, so this is the only lossless channel.
- Bools are emitted explicitly (`true`/`false`); numbers as literals;
  strings raw. Lossy string values (whitespace, `;`/`#`, newlines) are
  emitted with a warning.
- Round-trip guarantee: `export` → `import` into a fresh config → `export`
  is byte-identical (verified by tests).

### 13.4 CLI surface

- `llamaman import <file> [-c PATH]` — parses, maps, merges, validates,
  saves. Bootstraps a config (autodetected binary, `127.0.0.1:9080`)
  when none exists. Warnings go to stderr; exit 2 on parse/validation
  errors.
- `llamaman export [<path>] [-c PATH]` — writes the file (stdout when no
  path), warnings to stderr.
- Subcommands are dispatched before kong's positional dispatch and have
  their own kong parser (kong v1 cannot mix positional args and `cmd:`
  branches on one struct).

---

## 14. Roadmap

Agreed roadmap for the next releases (owner decision, August 2026). Scope,
constraints, risks, and implementation order are tracked in detail in
`ROADMAP.md` at the repo root; this section records the decisions so they are
not relitigated. Three releases, in priority order 4 → 2 → (3 + 1).

### 14.1 Release 1 — Polish

- **Multi-palette theme system.** `Theme`/`CurrentTheme()` in
  `internal/tui/common.go` become a palette table (4–6 curated palettes +
  `auto`). New additive v1 field `preferences.theme` (string); unknown value
  → warning + `auto`. Picker lives in the Settings mode (§14.1); a Main-mode
  quick key cycles live — both write `preferences.theme`. Palettes declare a
  background mode; incompatible choices warn and apply (explicit
  override, owner decision). Every
  palette keeps the named 256-color mapping (§10.4).
- **Settings mode & `preferences` object.** New top-level `preferences`
  object, separate from `globals` (owner decision — `globals` stays
  launch-param-only: host, port, binary, models-files). New Bubble Tea mode
  under Root, reachable from Main, editing exactly `preferences`. Release 1
  fields: `theme`, `animations`. Quick keys write the same object —
  shortcuts, not a second source of truth (P8).
- **Log & status readability.** Render-time colorizing of llama-server log
  lines by kind (ERROR/WARN/TIMING/INFO), ready-marker highlight, unicode
  status glyphs, terminal title via OSC escape. Search/jump/scrollback and
  the on-disk log are unaffected.
- **Load-progress indicator.** Live phase/progress line parsed from stderr
  (model load → layer offload → HF download % → listening). **Hard
  constraint:** separate from the `[STARTING]` badge — nothing replaces it.
  Tolerant classifiers; unknown phase degrades to today's static UI.
- **Subtle color animation.** `tea.Tick` 60 fps (owner decision;
  overridable via `LLAMAMAN_ANIM_FPS`);
  true-color lerp with a
  6-step discrete fallback on 256-color (P1, owner-amended from 2–3
  steps for less jerky breathing). Scoped to: load-progress
  fill, `[STARTING]` badge breathing, status-dot pulse while generating. No
  wordmark animation; steady state stays static; snapshot tests freeze a
  fake clock. No desktop notifications. **User control (P10):** gated by
  `preferences.animations`, default **on**; toggled in Settings mode.
- **Main-mode layout rework.** Implement §12.2 as designed.

### 14.2 Release 2 — Storage Manager

- **Hybrid storage.** Managed downloads write into llama.cpp's HF cache
  layout by default — the HF hub chain (`$LLAMA_CACHE` → `$HF_HUB_CACHE` →
  `$HUGGINGFACE_HUB_CACHE` → `$HF_HOME/hub` → `$XDG_CACHE_HOME/huggingface/hub`
  → `~/.cache/huggingface/hub`, first set wins), repo folders
  `models--<org>--<model>/` — with optional `preferences.models-dir`
  override (§16.1). The legacy llama.cpp layout (`~/.cache/llama.cpp`,
  `<org>__<model>` / flat `org__repo__file.gguf`) is tolerated for reads.
  One copy shared with `llama-cli`/`--hf-repo`; router `(cache)` tags line
  up.
- **Delegated launch + Downloads manager (owner decision C).** `hf` models
  stay fire-and-forget `--hf-repo`: llama.cpp downloads at startup
  (cache-first, `Range` resume, sha256-oid blob dedup, `mmproj`, `HF_TOKEN`),
  and the run-mode panel shows progress via the §15.4 classifier. No managed
  download on the launch path. A **Storage & Downloads manager** (new mode
  from Main) is the single place downloads are managed: cache listing (both
  layouts, §16.1), sizes, free space, delete-with-confirmation, and a
  "download now" pre-fetch (HF API `tree/main` for list+sizes+sha256,
  `resolve/main` with `Range`, pause/resume, sha256 verify, clear failures).
  Never deletes config entries without asking.
- **Quantization picker.** Per-quant real file sizes from the HF API, with a
  "fits VRAM" hint powered by the §14.3 estimator; hand-off into config or the
  manager's download action.
- **Model editor integration (owner decision, ROADMAP §3.8).** The config
  editor's free-type `location`/`hf` fields become picker-assisted: a GGUF
  `filepicker` overlay for local files, a cached-repo list from the §16.1
  reader for HF, and — for a newly typed repo — one async `tree/main` check
  offering the quant chooser with real sizes (mmproj informational only).
  No new config fields; failures non-blocking (P3).
- **HF model browser.** Search/browse HF in the TUI (search API,
  `filter=gguf`), metadata display, hand-off into config/download. Largest
  item; may slip to Release 3 under effort pressure.
- **Router note.** llama.cpp's router downloads internally; manager-only
  downloads (prefetch into the shared cache) apply to router and
  single-model runs alike; llama.cpp's own download progress is surfaced
  only. Rewriting
  router presets to local paths is a deferred implementation decision.

### 14.3 Release 3 — Trust & Touch

- **Crash diagnostics & auto-restart.** Crash view (exit code with
  interpretation + log tail) and optional auto-restart with exponential
  backoff while attached. **Default off** (opt-in). Detached-server
  watchdog is out of scope (no daemon mode).
- **VRAM preflight.** Rough (±20%) footprint estimate vs NVML VRAM before
  launch; warn, never block. Silent when NVML is unavailable. Feeds the
  quant picker's "fits VRAM" hints.
- **Pre-spawn checks.** Port-in-use probe with next-free-port offer (never a
  silent port change; CLI keeps exit code 4), free-disk check before
  downloads, HF repo existence validation.
- **Quick test prompt.** One key → small `/v1/chat/completions` request →
  overlay with TTFT + first tokens.
- **KV-cache pause/resume.** Save/restore via `/slots?action=save` with
  `--slot-save-path` (a normal preset param); TUI save/restore actions and a
  saved-slots list. Disabled for multimodal presets (llama.cpp #19466).
- **Web-UI shortcut.** `xdg-open http://<host>:<port>/` to llama-server's
  built-in UI; warning line if `xdg-open` is absent.

### 14.4 Deferred (do not re-propose without new information)

Desktop notifications; gradient wordmark (lipgloss v1.1.0 has no gradient
API); wordmark breathing; LoRA hot-swap; cancel-in-flight generation;
restart-with-edited-params (stays §11); config migration machinery (§12.1 —
additive v1 covers the roadmap, so no migration is triggered); health
watchdog (process alive but HTTP hung); daemon mode / detached watchdog;
multiple concurrent sessions.

### 14.5 Cross-cutting rules

- All new config fields are additive `version: 1`. New top-level
  `preferences` object holds user preferences (`theme`, `animations`, later
  `models-dir`, auto-restart); `globals` stays launch-param-only. §12.1
  stays dormant (P2).
- P1 visual contract: capability ladder truecolor → 256 → 8/bold →
  NO_COLOR; palette hexes map to 256; palettes declare background mode.
- P3 severity taxonomy: Info / Warning / Block; block only when the
  operation cannot proceed (exit codes 2/3/4); TUI offers recovery where
  the CLI exits.
- P6: documented minimum llama.cpp build; tolerant below floor for
  single-model use, hard gates for version-dependent features; CI floor →
  latest. Tolerant parsers; degrade to static UI, never crash.
- P7 network & privacy: requests only for explicit user actions, only to
  user-specified hosts; no telemetry, no implicit update checks.
- P8 single source of truth: config.json is the only definition; file-vs-
  config disagreement warns, never silently reconciles.
- P9 determinism: in-process snapshot tests, injectable clock/fetcher/
  spawner; unit tests need no real llama-server or terminal.
- P10 user control: anything costing performance/battery or changing
  behavior is toggleable in Settings mode; animations default on.
- Full principle texts live in ROADMAP.md §1.
- Version-gate router features; tolerate endpoint drift (router mode is
  experimental-grade upstream).
- Track llama.cpp default-port change 8080 → 9931 (PR #26508) on release;
  llamaman's 9080 default is unaffected unless overridden.

---

## 15. Release 1 — implementation design

One subsection per work item, in §2.7 order. Each subsection is the
implementation design note for its item: written *before* code (P5) and
reviewed by the owner; the owner's validation declares the unit done.
Cross-cutting rules from §14.5 and ROADMAP.md §1 apply to every item.
The note is updated in the same change as the code it describes; if
implementation forces a deviation, the note is amended in that same
change.

### 15.1 Theme system, `preferences` object, Settings mode

**Scope.** Replace the hard-coded `Theme`/`CurrentTheme()` pair in
`internal/tui/common.go` with a curated palette table; add the new
top-level `preferences` config object; add a Settings TUI mode that
edits exactly that object; wire the Main-mode quick keys as shortcuts
that write the same object (P8). **Non-goals:** no user-defined
arbitrary colors (ROADMAP §2.1), no animation behavior yet (item 5),
no CLI flag for theme (config.json is the only source of truth).

#### The `preferences` object (config schema)

Additive `version: 1`, per P2:

```jsonc
{
  "version": 1,
  "globals": { ... },
  "preferences": {            // optional object; absent == all defaults
    "theme": "auto",        // string, default "auto"
    "animations": true,      // bool, default true
    "log-colors": true       // bool, default true (§15.3)
  },
  "models": [ ... ]
}
```

- `Config.Preferences *Preferences` — a **pointer**, so the object is
  omitted from the file until the user actually changes a preference.
  Untouched configs stay byte-identical on save; the zero value of
  `Preferences` *is* the defaults, so nil-safe access is trivial.
- `Preferences { Theme string; Animations *bool }` with JSON tags
  `theme,omitempty` and `animations,omitempty`. Field-arrival contract:
  - `theme` absent or empty → `"auto"`.
  - `animations` absent (`nil`) → `true`. An explicit
    `"animations": false` is distinct from absent and must survive a
    save round-trip — hence `*bool`, not `bool` (a plain `bool` with
    omitempty would silently drop an explicit `false` on the next
    save).
  - `log-colors` follows the same `*bool` contract (default `true`,
    §15.3) — the run-mode log-coloring toggle.
- Nil-safe accessor `Config.Prefs() Preferences` (returns the zero
  value when the pointer is nil) is the only way the TUI reads
  preferences; callers never dereference the pointer directly.
- Older binaries reject the whole config with `json: unknown field
  "preferences"` — the accepted P2 contract; `version` stays 1.

**Validation (P2/P3).** `validatePreferences()` in
`internal/config/validate.go`: the fields are type-checked by JSON
decode, so the only config-level rule is that `theme`, when present, is
a non-empty string. The *semantic* check — is the name a real palette —
lives in the TUI resolver, **not** in `config.Validate`: `config`
cannot import `tui` (import direction), and duplicating the palette
name list in config would break P8 (two sources of truth). An unknown
name is a **Warning** (never a Block, P3): resolve to `auto`, log to
the app log, surface in the Settings-mode banner. `llamaman -l` never
resolves themes (no rendering), so no warning there — acceptable
because theme is purely visual.

#### Palette table (TUI)

New file `internal/tui/theme.go`. The `Theme` struct stays in
`common.go` (zones.go and the live bars consume it); the table and the
resolver are new.

- `type Background int` with `BackgroundAdaptive` / `BackgroundDark` /
  `BackgroundLight`.
- `type Palette struct { ID, Display string; Background Background; T Theme }`.
- `auto` is a **value**, not a palette: it is the default of
  `preferences.theme` and the fallback for unknown values (§14.1
  wording kept), and it resolves to the `llamaman` palette. Keeping
  `auto` as the stored default lets a future release move the default
  look without a config change.
- The current hard-coded theme becomes the first-class palette
  **`llamaman`** (owner decision): display "llamaman (default)", kept
  **exactly as-is** (the two existing dark/light variants, chosen by
  terminal background). It is the only **background-adaptive** palette
  (compatible with any terminal); nothing about its colors or the
  `auto` behavior changes in this release — it is only named and tabled.
- Table of 23 curated palettes plus the `auto` value — every dark
  palette ships its official light counterpart (owner decision:
  one light theme was not acceptable; either all-dark or dark+light
  pairs, and light variants add no structural cost). Grouped by
  family:

| Family | Dark | Light |
|---|---|---|
| llamaman | `llamaman` (adaptive — both variants in one palette) | — |
| Catppuccin | `catppuccin-mocha` | `catppuccin-latte` |
| Tokyo Night | `tokyo-night` | `tokyo-night-day` |
| Dracula | `dracula` | `dracula-light` (official name: Alucard) |
| Gruvbox | `gruvbox-dark` | `gruvbox-light` |
| Solarized | `solarized-dark` | `solarized-light` |
| Nord | `nord` | `nord-light` |
| One Dark | `one-dark` | `one-dark-light` |
| Kanagawa | `kanagawa` | `kanagawa-lotus` |
| Monokai | `monokai` | `monokai-light` |
| Rosé Pine | `rose-pine` | `rose-pine-dawn` |
| Night Owl | `night-owl` | `light-owl` (official light name) |

  A light terminal sees llamaman + all 23 palettes grouped by
  background — both variants of every family are offered (owner
  decision: the background is a hint, not a filter). The `t` /
  `shift+t` quick keys cycle the 24 entries (auto + all palettes)
  forward / backward and wrap; the Settings picker scrolls; nothing
  else changes.
  Light palettes cost nothing structurally — the mechanism is identical
  to Solarized Light, which already exercises the light path (resolution,
  filtering, tests); the work is transcribing each theme's official
  light hexes (incl. their 256-color intent) and adding table-driven
  test cases. The official light values are already tuned for contrast
  on light backgrounds (e.g. Alucard red `#CB3A2A`), so the Status*
  fields keep the same discipline as the existing llamaman light theme.
- `llamaman` (and therefore `auto`) keeps today's behavior exactly:
  `lipgloss.HasDarkBackground()` picks between its dark and light
  variants.
- **Resolver** (a pure function of name + background — P9):
  `ResolveTheme(name string, darkBg bool) (Theme, resolvedID string, ok bool)`.
  `auto` and `llamaman` both resolve to the `llamaman` palette.
  Unknown name → (llamaman/auto theme, `"auto"`, `false`); the caller
  turns `!ok` into the Warning above.
- **Background compatibility (P1).** Palettes declare a background
  mode; compatibility is now a **warning-level hint, not a filter**
  (owner decision): the Settings picker offers all 23 palettes, both
  variants, explicitly labeled `(dark)` / `(light)`, and the Settings
  screen shows the detected terminal background. Choosing a
  mismatched palette applies it with a warning (banner in Settings,
  flash in Main) — never a silent fallback. Only a hand-edited
  *unknown* theme degrades to `auto` with a Warning (P3), since it
  cannot render.
- **Color discipline (§10.4, P1).** Every palette field keeps the
  named-color mapping: hexes come from each palette's canonical values,
  chosen so their nearest 256-color index is the palette's classic
  xterm value, and each field carries a `// maps to 256-color NNN`
  comment exactly as today. 8-color terminals degrade through
  lipgloss's basic-color fallback; NO_COLOR keeps working via termenv
  with no code change.
- `CurrentTheme()` is removed. All four mode constructors (main, run,
  config, firstrun) stop calling it and receive the resolved `Theme`
  from Root; Root re-resolves on every preferences change and pushes
  the new theme down via `SetTheme` (MainMode also rebuilds its inline
  list delegates, which capture the theme).

#### Settings mode (TUI)

- New `internal/tui/settings.go`; `ViewSettings` added to the `View`
  enum in `root.go`; Root owns the mode exactly like Main/Run/Config.
- Reached from Main with **`s`** (free key: Main binds ↑/↓, Enter, Esc,
  tab, `c`, `a`, `?`, `q`). The `?` help overlay lists it; the
  shortcut row gains an `s` entry and a `t` entry (the `shift+t`
  backward direction is not shown in the shortcut row — owner call,
  it is well-known).
- **Screen.** A `huh` form, styled with the same `configHuhTheme` used
  by the config-mode forms:
  - `theme` — `huh.NewSelect` over all 23 palettes + `auto`, each
    option showing the display name with an explicit `(dark)` /
    `(light)` variant label.
  - `animations` — `huh.NewConfirm` (on/off), default on.
  - Submit → write `Preferences` into the config, save via the standard
    atomic path (`config.Save`, §3.4), Root re-resolves the theme, back
    to Main. Esc → discard, no mutation, back to Main.
- The form edits the live `cfg.Preferences` and saves on submit — no
  working copy needed (two scalar fields; the atomic save makes it
  safe).
- **Live preview (owner decision).** Arrowing through the theme select
  re-themes the Settings chrome and the form instantly (huh's
  `WithTheme` re-applies post-construction), and a preview pane renders
  the *actual* Main screen (a throwaway `MainMode` with the candidate
  palette — deterministic, side-effect-free) so the user sees the theme
  before committing. Esc discards; the preview never persists anything.
- **Quick keys (P8, shortcuts only).** Main **`t`** cycles the theme
  forward through all palettes + `auto`, and **`shift+t`** cycles
  backward — both live: resolve → write `preferences.theme` →
  atomic save → re-render (Bubble Tea reports the key as `T`). Same
  object, same save path as the Settings form — never a second source
  of truth. A mismatched landed palette flashes its warning instead of
  being skipped. The `?` help overlay (canonical keybinding reference,
  §7.2) lists `t / shift+t`; the shortcut row shows only `t`.
- **First-run:** no preferences step (ROADMAP §2.6); defaults apply;
  the object stays absent until the user visits Settings or presses
  `t`.
- **Detach/reattach (P4):** theme is presentation-only, resolved from
  config at attach/launch; `session.json` is untouched; a theme change
  while a server runs detached shows on the next attach.
- `preferences.animations` ships its field + Settings toggle now (P2:
  field, validation, editor support, and default in the same change);
  its default `true` matches today's behavior, and item 5 (§15.5)
  delivers the visible effects + the run-mode `a` quick-key toggle.

#### What changes in config load/save/validate

- `load.go`: no path expansion applies (no paths in `preferences`);
  `DisallowUnknownFields` already gives older binaries their rejection.
- `save.go`: unchanged — `json.MarshalIndent` on `Config` handles the
  `*Preferences` pointer and the `*bool` correctly (nil → omitted,
  explicit `false` → written).
- `validate.go`: new `validatePreferences()` with the shape-only rules
  above; the unknown-theme Warning is emitted by the resolver at theme
  resolution time, not here.

#### Tests (P9)

- Resolver unit tests: `auto` and `llamaman` resolve to the same
  adaptive palette (dark/light by injected background); unknown name →
  `"auto"` + `!ok`; every named palette resolves to its own colors.
- Deterministic color assertions:
  `lipgloss.SetColorProfile(termenv.ANSI256)` and
  `lipgloss.SetHasDarkBackground(bool)` (both exist in lipgloss
  v1.1.0) force the profile in tests, so snapshots are
  terminal-independent and tests can assert the **specific 256-color
  SGR codes** per palette field (P1: "snapshot tests assert specific
  colors"). Existing snapshots strip ANSI and are unaffected.
- Config tests: load/save round-trip of `preferences`; absent vs
  explicit-`false` `animations` survives a save; the object is absent
  from the file until first edited.
- Settings-mode snapshots: `s` from Main renders the form; submit saves
  preferences and returns to Main; `t` cycles the theme and persists.
- Fakeserver integration tests: unchanged — theme is TUI-local.

#### File map

| File | Change |
|---|---|
| `internal/config/types.go` | `Preferences` struct + `Config.Preferences *Preferences` |
| `internal/config/validate.go` | `validatePreferences` (shape-only rules) |
| `internal/tui/theme.go` (new) | palette table, `ResolveTheme`, `CompatiblePalettes` |
| `internal/tui/common.go` | remove `CurrentTheme()`; `Theme` struct stays |
| `internal/tui/root.go` | `ViewSettings`, `s` routing, theme resolution + `SetTheme` push |
| `internal/tui/settings.go` (new) | Settings mode + huh form |
| `internal/tui/main.go` | `t` / `shift+t` quick keys, `s` shortcut + help text, `SetTheme` |
| `internal/tui/run.go`, `config.go`, `firstrun.go` | constructors take the resolved theme |
| `DESIGN.md` §3.2 / §7.2 / §10.4 | schema example, key tables, theme section — updated in the same commit as the code |

**Deferred to later items:** animation rendering and the run-mode
toggle key (item 5, consumes `preferences.animations`); the §12.2
layout rework consumes palette tokens (item 2).

### 15.5 Subtle color animation (§2.4)

**Goal.** Minimal, tasteful motion that signals transitions without
noise: 60 fps (owner decision; overridable via `LLAMAMAN_ANIM_FPS`), only while transitional/generating, **never** in steady
state, no wordmark animation (ROADMAP §2.4 scope). Gated by
`preferences.animations` (default **on** — item 1's field; Settings
toggles it) and a new run-mode quick key. Determinism first: all
animation state is derived from an injectable clock at render time
(P9).

**Animated elements (all render-time; owner-approved set):**

*Repeating — only while the state is active:*

1. **Load-progress block** (only while the load window is open, §15.4):
   the phase/bar rows **breathe** (slow color lerp, sine wave) and,
   when no numeric progress is known, the bar row shows an
   **indeterminate comet** — a solid `█` head leading with a
   7-fragment tail behind it (`▏▎▍▌▋▊▉█` going right, `█▉▊▋▌▍▎▏`
   going left — the tail always follows the head, owner's design);
   at the far edge the head pins while the tail keeps sliding and
   merges into the solid block. Constant-speed triangle motion,
   10 steps/s, no fabricated percentages, no fragment on the wrong
   side of the head.
2. **`[STARTING]` badge:** the badge color breathes **yellow ↔ gold**
   (sine) while starting. The `[STARTING]` text is unchanged (§2.3).
3. **Status dot:** the READY badge's `●` glyph pulses while a request
   is generating (`busyCount > 0`), lerping between `StatusReady` and a
   brighter green.
4. **Gen/Process bar smooth fill** — the llama-server panel's Gen and
   Process progress rows fill smoothly toward their real target
   fraction between polls while generating/processing (honest motion
   on real data, not a fabricated percentage).
5. **Sparkline live-edge glow** — the newest Tokens/Prompt sparkline
   cell pulses softly while tokens flow (subtle).

*One-shot — fire on a transition, then static:*

6. **Ready glow** — when the status flips STARTING → READY, the badge
   does one brighten→settle (~0.6 s).
7. **Search-jump pulse** — on `n`/`N`, the newly-current occurrence
   fades briefly so the eye tracks the jump (complements the item-3
   tint).
8. **Error flash** — entering `✕ [ERROR]` does a brief red emphasis,
   then static (errors never breathe).
9. **TTFT arrival glow** — a brief glow on the Gen row when the first
   token of a response arrives.
10. **Router model-state flash** — a router-panel row that just
    loaded/unloaded flashes green/red once.

*(The "new-lines pulse" candidate was dropped: the `↓ N new lines`
indicator described in §7.4 does not exist in the codebase, so there
was nothing to animate.)*

**Mechanics.** `clock func() time.Time` (package-level; tests override)
feeds `animPhase(period)` → a 0..1 sine phase. A `tea.Tick` at 250 ms
(`animTickMsg`) is scheduled **only** while an animated element is
visible (load window open, starting, or busy) and animations are
enabled — steady state schedules nothing (§2.4 cost note). Colors:
`lerpColor(a, b, t)` (hex → RGB lerp → hex). Truecolor terminals get
the continuous interpolation; **256-color terminals quantize `t` to 3
discrete steps** (P1); NO_COLOR or `animations` off → no color change
and no tick.

**Run-mode quick key (P10/P8).** `a` in run mode toggles
`preferences.animations` live — same object, same persist path as `o`
(log colors, §15.3); footer + help gain `a anim`. `a` is free in run
mode (verified). Toggling off stops all motion immediately.

**Determinism (P9).** Tests freeze the clock: `animPhase` at fixed
instants; the STARTING badge color differs between phase 0 and 0.5
(forced ANSI-256 profile); the 256-color quantization yields ≤ 3
distinct colors across a full period; no tick cmd is scheduled when
animations are off or nothing animated is visible.

**Non-goals.** No wordmark animation; no steady-state READY idle
animation; no desktop notifications; no fabricated progress percentages
(item 4 stays honest — the indeterminate bar is position motion only).

**File map.** `internal/tui/anim.go` (new) — `clock`, `animPhase`,
`lerpColor`, `animTickMsg` + scheduling helper. `internal/tui/run.go`
— badge/dot/bar animation hooks (renderTopStrip + loadRows), `a` quick
key. Tests in `anim_test.go` / `run_test.go`. DESIGN §7.4 and §15.1
(animations field description) updated in the same change (P5).

### 15.4 Load-progress indicator (§2.3)

**Goal.** While a model loads, show a live phase/progress line parsed
from llama-server's stderr — `loading model file` → `offloading N
layers to GPU` → HF download % (when applicable) → done. **Hard
constraint (owner decision):** the indicator is **separate** from the
`[STARTING]` badge; no spinner or progress element replaces the badge,
which stays exactly as-is. If parsing yields nothing, the UI is
identical to today (tolerant classifiers, §2.3 risk / P6).

**Phase classifier** — `parseLoadPhase(line string) (phase string,
progress *float64)` in `run.go`, a pure function over single stderr
lines (P9), tolerant (patterns stop matching → no indicator):

| Phase | Match (case-insensitive) | Progress |
|---|---|---|
| load-file | `loading model (from\|file)` | none (text only) |
| offload | `offloaded (\d+)\/(\d+) layers` | fraction `n/m` |
| offload-start | `offloading \d+ repeating layers` | none (text only) |
| download | `(downloading\|download).*?(\d+(?:\.\d+)?)%` | percent/100 |
| done | `listening on` | — (existing readyMarker handles transition) |

Unknown lines → no phase; the newest matching line wins (a `lastPhase`/
`lastProgress` pair, cleared when status leaves `StatusStarting`).

**Indicator placement & rendering (owner decision).** The load-progress
block lives **inside the left panel** — the "llama-server" live-data
box in single-model mode, the model list in router mode — replacing
that panel's content while the starting window is open (live stats /
the model list can't render meaningfully before READY anyway):

```
╭── llama-server ─────────────────────╮
│ loading model file …                │
│ offloading layers to GPU: 21/33     │
│ ▓▓▓▓▓░░░░░░░░ 41%                   │
╰─────────────────────────────────────╯
```

- phase text and the progress bar in `Accent` (brighter — owner
  polish; the bar uses blocks `▓`/`░`, capped ~12 cells, with a
  `Subtle` percent suffix); a blank row sits above the block, each row
  carries a ` > ` prefix and one trailing space so the text never
  touches the panel edges. The bar appears only when a numeric
  progress is known.
  Multiple phase lines accumulate (newest at the bottom) within the
  block, capped to the panel height.
- **Data-gated bar (owner finding, b10281).** llama-server's server
  mode suppresses the loader's per-layer logs (`offloaded N/M
  layers`, download %) and reports load only through its own
  `load_model:` sequence — `loading model` → `initializing` →
  `model loaded` → `listening` (verified on the owner's real logs for
  both local and HF models). So in practice the block shows phase
  text without a bar; the bar lights up automatically when a fraction
  or percentage line is present (llama-cli output, an uncached HF
  download, or a future server build). A higher `-lv` verbosity may
  surface the loader lines; the classifier would pick them up with no
  changes. An *indeterminate* moving bar for the "loading model"
  phase is a candidate for item 5 (animation, gated by
  `preferences.animations`) — no fabricated percentages in item 4.
- When starting but no phase has parsed yet, the block shows the
  static text `loading…` (§2.3 "unknown phase → static text"). On
  READY (or exit) the normal panel content returns.
- **Minimum visible time (owner decision):** once a phase line is
  shown, the load block stays visible for **≥ 2 s** even if READY
  arrives sooner (`loadPhaseUntil = shownAt + 2s`; the panel switches
  back only when READY *and* the deadline has passed) — the indicator
  never flashes by.
- **Separate from `[STARTING]`:** the badge (row 1) is untouched; the
  indicator is panel content (§2.3 hard constraint).
- **Both modes:** single-model (llama-server stats panel) and router
  (model list panel) show the same block during the starting window —
  the owner tests both.

**Data flow.** The `logChunkMsg` handler already scans incoming chunks
for the ready marker; it now also feeds each chunk line to
`parseLoadPhase` while `StatusStarting`, storing the latest
phase/progress and stamping `loadPhaseUntil = now + 2s` when a phase
first appears. The left panel renders the load block while
`status == StatusStarting || now < loadPhaseUntil`. On exit the pair
is cleared. No new keys, no polling — purely reactive to log chunks
plus the 2s minimum-visible clock.

**Determinism (P9).** Classifier unit tests on synthetic lines (real
llama.cpp shapes + near-misses); panel render tests for the load block
(single + router) with a stored pair; a chunk-driven test asserting
the phase appears and the block clears after ready + the 2s deadline
(`loadPhaseUntil` is a settable field — tests pin the past/future
cases). The fakeserver fixture gains load-progress lines (offload
fraction + a download % line) so integration tests exercise the real
path. The classifier is kept as a single small function — the
§6-synergy "same tolerant classifier" needed by Release 2's download
progress can reuse/extend it.

**Non-goals.** No spinner animation (item 5, gated by
`preferences.animations`); nothing replaces or modifies the
`[STARTING]` badge; no model-load percent from llama.cpp internals
(only what the log text exposes).

**File map.** `internal/tui/run.go` — `parseLoadPhase`,
`loadPhase`/`loadProgress`/`loadPhaseUntil` fields, the load block in
`renderServerPanel` (single) and `renderRouterPanel` (router),
`logChunkMsg` hook. `cmd/llamaman-fakeserver` — fixture lines.
New tests in `run_test.go` / `loglines_test.go`. DESIGN §7.4 gains the
indicator description in the same change (P5).

### 15.3 Log & status readability (§2.2)

**Goal.** Make the run-mode log readable at a glance: colorize
llama-server output by line kind, highlight the readiness marker,
refresh the terminal title, and add unicode status glyphs — all
render-time only, never touching the on-disk log, search, scrollback,
or the denoise toggle (§2.2 constraints).

**Line-kind classifier** — new `classifyLine(line string) LineKind`
plus a `colorizeLine` render helper in `run.go`:

| Kind | Match (conservative, case-insensitive) | Color |
|---|---|---|
| ERROR | severity letter `E` in llama.cpp's `0.00.… E …` prefix, or `\berror\b` \| `\bfailed\b` \| `\bfatal\b` \| `\baborted\b` | `StatusErr` |
| WARN | severity letter `W`, or `\bwarn(ing)?\b` \| `\bdeprecated\b` | `StatusStart` |
| TIMING | `tokens? per second` \| `ms per token` \| `eval time` \| `prompt eval time` \| `total time` \| `load time` | `Muted` |
| READY | contains `listening on` (the ready marker) | `StatusReady` + bold |
| INFO | default (incl. llama.cpp severity letters `I` / `D`) | none (plain) |

The severity-letter prefix (`^[0-9.]+ [IWED] `) is checked first — it is
the authoritative severity in llama.cpp's default logger (owner
feedback). The remaining rules are a single slice of (regex, kind)
pairs — cheap to extend as llama.cpp output drifts (P6). **Conservative
by design (§2.2 risk):** the worst case is an uncolored line; the
ERROR/WARN patterns are broad enough that a real critical line is never
rendered as plain INFO.

**Where it applies.** `renderViewportContent()` is the single hook:
per line → `colorizeLine(kind, highlightOccurrences(line, q))`.
`visibleLogLines()` stays plain, so search matching, jump-to-match
indices, scrollback positions, and the denoise toggle are all
unaffected; the on-disk log is byte-identical (colorizing is
render-time only). Search highlight (bold+reverse) takes visual
precedence during an active search; a match inside a colored line
reverts the remainder of that line to default color until the next
line's reset — accepted cosmetic during active search.

**Current search occurrence (owner feedback).** `n`/`N` navigation
marks the current matching line's matches with **bold+reverse tinted
with the theme's `StatusIdle` color** (a colored background) while
other matches stay plain bold+reverse — the selected occurrence is
unmistakable at a glance, and the tint deliberately avoids the
line-kind colors (`StatusErr`/`StatusStart`/`StatusReady`/`Muted`) so
it never disappears on a WARN or ERROR line. The tint moves with the
selection (`jumpSearch` re-renders before scrolling). Granularity is
per matching line (the existing `searchMatches` model). A scanning
animation for next/previous occurrence is a candidate for item 5
(gated by `preferences.animations`), not part of item 3.

**Log-colors toggle (owner decision: preference + quick key).**
Coloring is controlled by the new `preferences.log-colors` field
(default `true`, §15.1) — the Settings form edits it — and the
run-mode `o` quick key flips it live, writing the same object and
persisting via the config path (P8 shortcut pattern, like theme).

**Terminal title (OSC).** `tea.SetWindowTitle` (bubbletea v1.3.10):
`llamaman — <alias> [STARTING]` on run-mode init; `[READY]` on both
ready paths (the textual `listening on` marker and the /props
transition); `[ERROR]` / `[EXITED]` on those state changes. Router runs
use the models-file basename as the alias. No restore-on-exit in v1
(the terminal keeps the last title).

**Unicode status glyphs.** The `[LABEL]` badge (`statusBadge`) gains a
per-state glyph prefix, colored with the state color:
`● [READY]`, `◌ [STARTING]`, `✕ [ERROR]`, `◌ [EXITED]`.
**§2.3 constraint honored:** the `[STARTING]` badge's text and format
stay — the hollow-dot prefix is additive, and nothing replaces the
badge (the load-progress indicator is item 4's job). The glyphs are
owner-vetoable if the plain badge is preferred.

**Determinism (P9).** Classifier unit tests on synthetic lines
(error/warn/timing/ready/info + near-misses); a render test with a
forced ANSI-256 profile asserting the viewport wraps an ERROR line in
the expected SGR and leaves INFO lines plain; a title test asserting
the OSC string helper. Existing snapshot tests strip ANSI and are
unaffected (the one `renderViewportContent` test already strips).

**Non-goals.** No on-disk log changes; no new keys; no spinner or
progress element (item 4); no change to the `[STARTING]` badge
semantics.

**File map.** `internal/tui/run.go` — `classifyLine`, `colorizeLine`,
`renderViewportContent` hook, `statusBadge` glyph prefix,
`tea.SetWindowTitle` on init/ready/error/exited transitions. New tests
in `run_test.go`. DESIGN §7.4 gains the status/lines description in the
same change (P5).

### 15.2 Main-mode layout rework (§12.2)

**Goal.** Make Main mode surface what the user wants at a glance and use
the terminal it has. Commits the four §12.2 "likely to change" items:
per-row source kind, a session reattach screen, preset preview on the
highlighted row, and wider lists on wide terminals. Pure presentation —
no server-side state, no new mode, `?` help overlay stays canonical
(§12.2 constraints). Consumes the §15.1 palette tokens.

**Layout, top to bottom:**

1. **Session reattach screen** — when a session is running (live PID in
   `session.json`), Main becomes a **single reattach entry**; the model
   list is hidden until the session ends (owner decision: with a single
   session nothing else can launch, so the list would invite dead Ends;
   the list-with-markers variant returns when concurrent sessions
   arrive):
   ```
   running  alpha/default · listening on :9080
   ```
   - one highlighted row (plain text, single reverse-video SGR pair),
     `running` tag in `Muted`; router sessions report the models-file
     path as the alias.
   - **Enter** or **`a`** attaches. No kill affordance on this screen
     (owner decision — attach → `k` → confirm covers it; concurrent-
     session work will design per-session kill properly).
   - Shortcut row: `Enter attach · a attach · c configure · s settings ·
     ? help · q quit` (`q` quits llamaman leaving the server running;
     `tab`/↑↓ are gone with the list).
   - **Reachability (owner decision).** A no-args launch with a live
     session lands on Main mode (§4.3), not run view. In-app: `esc` in
     run mode (final layer after menu/stats/search) detaches to Main
     with the server running and llamaman still open; the quit-prompt
     `d` still detaches *and quits* llamaman. `llamaman <alias>` with a
     live session still reattaches directly.

2. **Wider list on wide terminals** — the inline list box width cap
   grows from 60 to **90** columns (`min(90, width − 8)`, floor stays
   20). This is the only width-dependent change; no behavioral
   threshold.

3. **Per-row source tag** — every row gains a leading tag rendered in
   `Muted` before the alias: `local` (config model with `location`),
   `hf` (config model with Hugging Face ID), `router` (my-models.ini
   source). So users see at a glance which models will hit the network
   on first launch. The `(running)` suffix and the alias stay.

4. **Preset preview on highlight** — when the highlighted row is a
   config model with ≥ 2 presets, its description line shows the actual
   preset names instead of the bare count, single line, ellipsized to
   the row width (`2 presets: fast · smallctx · …`). 1 preset already
   shows its name; 0 shows `0 presets`. **Committed decision:** the
   preview is single-line (height-stable, no variable-height delegate
   requirement in bubbles/list); the two-line expand is deferred. The
   preview is passive — Enter still pivots to the preset sub-list to
   launch (§7.2 mechanics unchanged).

5. **Router rows** — tag `router`; description unchanged
   (`router · N models — <path>`).

**Palette tokens (from §15.1).** source tags → `Muted`; preset preview →
`Subtle`; reattach entry → `Muted` tag, reverse-video highlight; borders →
`Border`/`BorderFocus` as today; wordmark → `Accent` (unchanged).
`SetTheme` (item 1) already rebuilds the inline delegates, so a theme
change re-renders all of this consistently.

**Constraints preserved (§12.2).** The no-models empty state stays
minimal (wordmark + shortcuts, no empty list box); configuration-file
row order remains the visible order; every existing keybinding keeps
its meaning (no new keys, so §7.2 and the help overlay text are
unchanged).

**Determinism (P9).** All changes are pure render. Snapshot tests
assert: source tags on local/hf/router rows; preset names on the
highlighted multi-preset row (and count-only on non-highlighted rows);
the session reattach entry when a session is running and the model
list otherwise; the list-box width on a wide window.

**Non-goals.** No per-user layout preferences (the `preferences` object
stays item 1's scope); no server-side changes; no new TUI mode; no
change to the preset-pivot mechanics; no two-line row expansion in v1.

**File map.** `internal/tui/main.go` — session reattach screen
(`renderReattachBox` replaces the old strip/detached line),
`inlineDelegate.Render` (source tag + preset preview + stable box),
`listWidth` cap 60 → 90, reattach-state shortcuts/help/Update.
`internal/tui/run.go` — `esc` final layer detaches to Main (live
session only), footer + help gain `esc back`. `main.go` — no-args
launch lands on Main when a session is live (§4.3).
`snapshot_test.go` + `main_test.go` — updated and new assertions.

---

## 16. Release 2 (Storage Manager) — implementation design

One subsection per work item, in §3.7 order. Each subsection is the
implementation design note for its item: written *before* code (P5) and
reviewed by the owner; the owner's validation declares the unit done.
Cross-cutting rules from §14.5 and ROADMAP.md §1 apply to every item.
The note is updated in the same change as the code it describes; if
implementation forces a deviation, the note is amended in that same
change.

### 16.1 Hybrid storage foundation — the cache-layout reader

**Scope.** First item of Release 2 (ROADMAP §3.7). Two deliverables in
one unit:

1. `preferences.models-dir` — the additive Release-2 field (ROADMAP §5,
   DESIGN §14.5) with its full P2 field-arrival contract: JSON tag,
   validation rule, Settings-mode editor, and a documented default, all
   in the same change.
2. The cache-layout reader — a new `internal/storage/` package that
   resolves llama.cpp's HF cache root, classifies the entries it finds,
   reads both known layouts, and warns on unrecognized entries
   (ROADMAP §3.1; §8 risk row).

**Non-goals.** No downloader (the manager's download action is the
storage-manager item, ROADMAP §3.4), no HF API client (item 2), no
storage-manager TUI (ROADMAP §3.4), no write path (the manager's
download action creates cache entries), no quant-level filtering (quant
picker item). The reader ships tested and ready; its first production
consumer is the config editor's cached-repo list (ROADMAP §3.8 step A),
then the Storage & Downloads manager (§3.4). The only new
user-visible surface in this item is the Settings-mode field.

#### Cache-path resolution rules

The effective cache root resolves first-match-wins, in this order:

| Priority | Source | Resolved root |
|---|---|---|
| 1 | `preferences.models-dir` (set) | the value, `~`/`$VAR`-expanded at load time (§3.3); wins over every environment variable |
| 2 | `$LLAMA_CACHE` | the value, used as-is |
| 3 | `$HF_HUB_CACHE` | the value, used as-is |
| 4 | `$HUGGINGFACE_HUB_CACHE` | the value, used as-is |
| 5 | `$HF_HOME` | `<HF_HOME>/hub` |
| 6 | `$XDG_CACHE_HOME` | `<XDG_CACHE_HOME>/huggingface/hub` |
| 7 | `$HOME` | `~/.cache/huggingface/hub` |

This mirrors llama.cpp's `get_cache_directory()` in
`common/hf-cache.cpp` exactly (verified against master at the time of
writing: `LLAMA_CACHE` → `HF_HUB_CACHE` → `HUGGINGFACE_HUB_CACHE` →
`HF_HOME/hub` → `XDG_CACHE_HOME/huggingface/hub` →
`HOME/.cache/huggingface/hub`; llama.cpp's `getpwuid` fallback is
covered by Go's `os.UserHomeDir`). First non-empty wins.

Rules:

- `models-dir` outranks the environment: an explicit config preference
  beats a shell variable (P8 — config.json is the only definition of
  preferences; the env chain is llama.cpp's fallback, not the user's
  choice).
- The root is **llama.cpp's** cache root, never llamaman's own
  `$XDG_CACHE_HOME/llamaman` (`paths.CacheDir`). The two never merge.
- The root does not need to exist (the manager's download action
  creates it on first download).
- `CacheRoot(modelsDir string) (string, error)` takes the preference
  value; it does not expand paths (load-time job) and does not
  absolutize relative values (pass-through, documented).

#### Layout detection and tolerance

**Two known layouts** (verified against llama.cpp history; see the
acceptance-risk note):

| Layout | Shape at root | Verified origin |
|---|---|---|
| **HF hub** (primary; written and read by llama.cpp since PR #20775, ~Mar 2025, and by master today) | `<root>/models--<org>--<model>/` — `refs/main` (commit hash), `snapshots/<commit>/<file>`, `blobs/<sha256>`; `snapshots/` files are usually symlinks to `../../blobs/<oid>` (`finalize_file`) | `repo_to_folder_name`: `"models--" + repo_id` with `/` → `--` |
| **Legacy llama.cpp** (pre-#20775; llama.cpp stopped migrating it in PR #23266) | flat `<root>/<org>__<repo>__<file>.gguf` (+ `.etag` sidecars, `manifest=<org>=<repo>=latest.json`); tolerated folder variant `<root>/<org>__<model>/<file>` | #20775's migration code (the commit message shows the flat names); ROADMAP §3.1 documents the folder form |

**Detection** — every child of the cache root is classified once, by
name and type:

1. Directory named `models--…` → HF hub repo (repo id: strip
   `models--`, then `--` → `/`). Recognized even without `snapshots/`
   (empty cache state — normal during or after a download; returns zero
   files, no warning). A `models--` name whose converted repo id is
   invalid (no `/`) → unrecognized → warning.
2. Directory matching `^[\w.-]+__[\w.-]+$` → legacy folder repo.
3. File matching `<org>__<repo>__<file>.gguf` / `.mmproj` → legacy flat
   file (repo = the first two `__` segments).
4. Metadata — `*.etag` sidecars, `manifest=…` files, and the HF hub
   root files (`CACHEDIR.TAG`, `version.txt`, `.locks/`, written by
   `huggingface_hub`) → recognized, skipped silently.
5. Anything else → **unrecognized** → warning (tolerance strategy
   below).

**Lookup** — `Lookup(root, hfID)` with `hfID` = `org/repo[:quant]`:

- Strip `:quant` (the quant lives in the file *name*; llama.cpp's repo
  folder never encodes it).
- Hub: read `refs/main` for the commit; enumerate files under
  `snapshots/<commit>/`; if no ref resolves, fall back to any
  `snapshots/*/` directory. Sizes via `os.Stat` (follows symlinks); the
  reported path is the snapshots path (llama.cpp's `final_path`), the
  canonical file for the cached model.
- Legacy: files matching `org__repo__*` in the root (skip `.etag`),
  plus files under the `<org>__<model>` folder variant.
- Return `[]CachedFile{RepoID, Path, Size, Layout}` for `.gguf` and
  `.mmproj` files only (tokenizers, `config.json` etc. are metadata,
  not models). No quant filtering in this item.

**Tolerance strategy (P6, P3):**

- Both known layouts read without error. Unrecognized entries are a
  **Warning**, never a Block, never a crash.
- A missing or empty root is not an error: "not cached" is a normal
  answer (the manager's download action and the cache listing depend on
  this).
- The reader never mutates anything (P8 — no silent reconciliation;
  item 5 owns deletion).
- Warnings leave the package through a `warn func(string)` callback;
  item 1 routes it to the app log (`internal/logging`). Warn once per
  entry.

#### models-dir field-arrival contract (P2)

- `Preferences.ModelsDir string` with JSON tag `models-dir,omitempty`.
  A plain string, not a pointer: the default is `""` = "follow
  llama.cpp's chain", and there is no meaningful explicit-empty
  distinct from absent (unlike `animations`, where explicit `false`
  must survive a round trip).
- Absent or `""` → env chain (table above). Set → single root.
- The zero value is the default, so the existing nil-safe
  `Config.Prefs()` already covers access; no new accessor.
- Older binaries reject the whole config with
  `json: unknown field "models-dir"` — the accepted P2 contract;
  `version` stays 1.

**Config / load / validate changes (same change):**

- `internal/config/types.go` — the field plus doc.
- `internal/config/load.go` — extend the §3.3 expansion pass to
  `preferences.models-dir` (`paths.ExpandPath`).
- `internal/config/validate.go` — `validatePreferences` gains one
  rule: when set and the path exists and is not a directory → Warning
  (`models-dir is not a directory`). No existence requirement
  otherwise (the directory is created later).
- `internal/tui/settings.go` — a `huh.NewInput` field after log-colors;
  the description states the default chain and the llama-cli sharing
  benefit. Empty input removes the field from the persisted object
  (`snapshot()` contract: only non-default values persist; untouched
  configs stay byte-identical on save).
- `DESIGN.md` §3.2 (schema example) and §3.3 (path-expansion list)
  updated in the same change (P5).

#### Interplay with downloads (owner decision C: delegated launch,
#### manager-owned downloads)

Recorded here so the storage-manager item implements against the right
foundation:

- The **launch path stays delegated** (`--hf-repo`): llama.cpp downloads
  at startup, cache-first, into the same root the reader resolves.
  `Lookup` is NOT used on the launch path — there is no `--model <cached
  path>` takeover (ROADMAP §3.2).
- The reader's first production consumer is the config editor's
  cached-repo list (§3.8 step A); the **Storage & Downloads manager**
  (§3.4) lists cached files via `Scan`/`Lookup`, and its "download
  now" action writes into the same root (the writer reuses `CacheRoot` +
  `RepoFolderNames`), verifies sha256, and leaves the cache populated
  for the next launch.
- Router `(cache)` rows in run mode (llama.cpp's own downloads) line up
  with the manager's listing because both read and write the same hub
  layout.
- Users with `LLAMA_CACHE` set get downloads in their custom root,
  matching `llama-cli`.

#### Acceptance risk (owner confirmation)

1. **ROADMAP §3.1 and DESIGN §14.2 describe a layout llama.cpp no
   longer writes.** Both say: default chain `$LLAMA_CACHE → ~/.cache/
   llama.cpp → HF hub layout` and repo folder form `<org>__<model>/
   <file>` via `repo_to_folder_name`. Verified against llama.cpp
   history and master at the time of writing: PR #20775 (~Mar 2025)
   switched llama.cpp to the **standard HF hub layout** — chain
   `LLAMA_CACHE → HF_HUB_CACHE → HUGGINGFACE_HUB_CACHE → HF_HOME/hub →
   XDG_CACHE_HOME/huggingface/hub → ~/.cache/huggingface/hub`, repo
   folder `models--<org>--<model>`, files under `snapshots/<commit>/`
   (+ `blobs/`, `refs/`). `~/.cache/llama.cpp` and the
   `<org>__<model>` / `<org>__<repo>__<file>` forms are the **legacy**
   layout; llama.cpp stopped migrating it in PR #23266. **This note
   adopts the verified current layout as default and treats the legacy
   forms as the second known layout (read-only).** Confirming this also
   amends §14.2 and ROADMAP §3.1 (same-change P5). If the owner prefers
   the §3.1 wording as the default, that desyncs from llama.cpp ≥ b5xxx
   and breaks the "one copy shared with `llama-cli`/`--hf-repo`" goal
   (§3.1 Why) — the note asks the owner to confirm the verified-current
   default.
2. **Layout-change risk (ROADMAP §8).** llama.cpp may change the layout
   again; the reader concentrates every pattern in two tables (env
   chain + layout rules), degrades to warnings (P6), and never crashes;
   the tables are the single update point, each table-driven tested.
3. **Env-chain mirroring.** llamaman mirrors llama.cpp's env-var order
   so behavior matches `llama-cli` under `LLAMA_CACHE` / `HF_HOME`; if
   llama.cpp renames or reorders variables, only the one table changes.

#### Determinism and tests (P9)

No network, no llama-server, no real terminal.

- `CacheRoot` — table-driven env matrix with `t.Setenv` (each var
  alone; priority order; `models-dir` beats all env; HOME fallback; the
  `getpwuid` path is not unit-tested — `os.UserHomeDir` covers it).
- `RepoFolderNames`, `DetectLayout` — table tests (hub folder, legacy
  folder, legacy flat, `.etag`, `manifest=`, junk file, junk dir,
  dotfiles, invalid `models--` name).
- `Scan`, `Lookup` — fake cache trees in `t.TempDir`: hub repo with
  `refs/main` + `snapshots/<commit>/file`, the symlink-to-blob
  variant, no-ref fallback to any snapshot; legacy flat + `.etag`;
  legacy folder; unrecognized entries captured via the warn callback;
  unknown repo → empty, no error; missing root → empty, no error.
- Config — round trip: `"models-dir": "~/models"` loads expanded; empty
  stays absent on save; validation Warning when the path is a file.
- Settings — form gains the field; `snapshot()` writes it only when
  non-empty and removes it on empty; existing settings form tests
  updated for the extra field.

**File map.** New `internal/storage/` — `root.go` (cache-root
resolution), `layout.go` (folder-name mapping + detection), `scan.go`
(scan/lookup + types), each with tests. `internal/config/types.go`,
`load.go`, `validate.go` + tests. `internal/tui/settings.go` +
`settings_test.go`. `DESIGN.md` — this §16.1, §3.2, §3.3 (and, on
owner confirmation, §14.2). No `main.go` change.


### 16.2 HF API client

**Scope.** Second item of Release 2 (ROADMAP §3.7). A small,
dependency-free HTTP client for the Hugging Face API surface named in
ROADMAP §3.2 — the piece shared by every network-consuming item that
follows (quant picker §3.3, the manager's download action §3.4, the
config editor's repo check §3.8b, the browser §3.5). New package
`internal/hf/`. **Non-goals:** no download loop (the manager's download
action owns resume/sha256, §3.4), no quant filtering (item 3), no search
(item 7), no config token (`HF_TOKEN` env only; `preferences.hf-token`
stays deferred).

**Endpoints** (P7: requests only on explicit caller actions, only to
huggingface.co unless overridden):

| Call | Request | Returns |
|---|---|---|
| File tree | `GET {endpoint}/api/models/{repo}/tree/{revision}?recursive=true` | `[]RepoFile{Path, Size, OID}` — existence check + quant list + sizes + LFS sha256 in one round trip |
| Repo metadata | `GET {endpoint}/api/models/{repo}` | `RepoMeta{ID, SHA, Downloads, Likes, Tags}` (browser extends later) |

- **Endpoint:** `$HF_ENDPOINT`, default `https://huggingface.co`
  (mirrors llama.cpp's `common_get_model_endpoint` and
  `huggingface_hub`; keeps a mirror usable and is the single update
  point if the API moves). Revision defaults to `main`; callers may
  pass a branch or commit.
- **Tree entries:** `type: "file"` only; size = `lfs.size` when present
  else `size`; OID = `lfs.oid` else `oid` (the LFS oid is the sha256
  the downloader verifies against). Directories are skipped.
- **Token:** read `HF_TOKEN` once per client; when non-empty, send
  `Authorization: Bearer <token>` on every request (gated repos).
- **Errors (P3, for §3.8b's distinct messages):** typed `hf.Error` with
  a kind: `ErrNotFound` (404), `ErrGated` (401/403), `ErrNetwork`
  (DNS/timeout/transport), `ErrHTTP` (other status, carries the code).
  Convenience predicates `IsNotFound`/`IsGated`.

**API surface.**

```go
func New() (*Client, error)                     // endpoint/token from env
func NewWithEndpoint(endpoint, token string) *Client // tests + injection
func (c *Client) Tree(ctx, repo, revision string) ([]RepoFile, error)
func (c *Client) Repo(ctx, repo string) (RepoMeta, error)
func (c *Client) RepoExists(ctx, repo string) (bool, error) // Tree, kind-filtered
```

- Repo ids are path-escaped per segment (`Qwen/Qwen3-32B-GGUF`).
- A bounded `http.Client` timeout; `ctx` passes cancellation through
  (P10 — user control later).
- `RepoExists` is `Tree` with `IsNotFound` → false, other errors
  surfaced (a gated repo *exists*).

**Determinism (P9).** All tests run against `httptest.Server` — no
network, no llama-server. Cover: URL path + `recursive=true` query;
Bearer header with `HF_TOKEN` set (`t.Setenv`) and absent; lfs-oid and
lfs-size extraction; directory entries skipped; 404 → `ErrNotFound`,
401/403 → `ErrGated`, transport error → `ErrNetwork`; malformed JSON →
error; `HF_ENDPOINT` honored; empty repo response.

**File map.** New `internal/hf/` — `client.go` (endpoint/token/HTTP,
error types), `types.go` (`RepoFile`, `RepoMeta`), `client_test.go`,
`types_test.go`. No `main.go` change, no TUI change, no config change.
DESIGN §14.2 / ROADMAP §3.2 already describe the item; this note adds
the implementation contract.

### 16.3 Quantization picker — the shared quant chooser

**Scope.** Third item of Release 2 (ROADMAP §3.7). Under owner decision C
and §3.8, the "quantization picker" is a **shared data component**, not
a UI host: it turns a repo's `hf.Tree` listing into a pickable, sized
quant list. The UI pickers that render it land with their hosts — the
Storage & Downloads manager's download action (§3.4) and the config
editor's typed-repo flow (§3.8 step B). Ships in `internal/hf/`
(`quant.go`) on top of the §16.2 client. **Non-goals:** no download loop
(item 4), no VRAM math (the §14.3 estimator is a hook, sizes-only until
it ships), no search (item 7), no mmproj *selection* (informational
only — llama.cpp auto-downloads it), no TUI code in this item.

**Quant parsing.** The quant lives in the file name, matching llama.cpp's
`get_gguf_split_info` (verified): strip `.gguf`, then a trailing
`[-.]([A-Z0-9_]+)` tag (case-insensitive match, uppercased) — e.g.
`qwen3-UD-Q4_K_XL.gguf` → `UD-Q4_K_XL`, `model-Q8_0.gguf` → `Q8_0`,
`model-F16.gguf` → `F16`. Split models (`-NNNNN-of-NNNNN`) share one
quant: their parts group into a single option whose size is the **sum**
of the parts. Files with no parseable tag fall back to their basename as
the option name.

**API surface.**

```go
type QuantOption struct {
    Tag   string // quant tag (uppercased) or basename fallback
    Files []hf.RepoFile
    Size  int64 // total (split parts summed)
}

func Quants(files []hf.RepoFile) []QuantOption // filters .gguf, groups, sorts
func HasMMProj(files []hf.RepoFile) bool       // informational note for §3.8b
func HumanSize(n int64) string                 // 1.2 GiB style, for pickers
func Choose(ctx context.Context, c *hf.Client, repo string) ([]QuantOption, error)
    // Tree(repo, "main") → Quants; the single fetch behind the pickers
```

- `Quants` is a pure function of `[]hf.RepoFile` — fully testable
  without network (P9); `Choose` is the only network wrapper.
- Sort: size ascending, ties by tag — the natural "smallest first"
  order for a "fits in VRAM" picker.
- The chosen `Tag` becomes the `:quant` suffix of the config `hf` entry
  (`org/repo:Q4_K_XL`). Verified: llama.cpp's `find_best_model` matches
  the tag as a regex `tag + "[.-]"` **substring over the file path**, so
  the strict tag always selects the right file — e.g. `Q4_K_XL` selects
  `Qwen3.6-27B-UD-Q4_K_XL.gguf` (the `UD-` prefix is irrelevant to
  matching). Two files with the same strict tag in one repo merge into
  one option — upstream selection is equally ambiguous there.
- **VRAM hint hook (R3 §4.2):** hosts attach the estimate; `QuantOption`
  carries `Size` only, so the hint is a render-time addition — no
  coupling to the estimator in this item.

**Determinism (P9).** Table tests on synthetic file lists: real quant
shapes (Q4_K_M, Q8_0, F16, IQ3_XXS, UD-Q4_K_XL), split files summing,
case-insensitive tags, no-tag fallback, `.mmproj` excluded from `Quants`
but detected by `HasMMProj`, non-model files ignored, deterministic
ordering. `Choose` tested against `httptest.Server` reusing the §16.2
client.

**File map.** New `internal/hf/quant.go`, `quant_test.go`. No `main.go`,
TUI, config, or storage changes. DESIGN §14.2 / ROADMAP §3.3 already
describe the item; this note adds the component contract.

### 16.4 Storage & Downloads manager

**Scope.** Fourth item of Release 2 (ROADMAP §3.7; §3.4 with owner
decision C). The first user-visible surface of the release and the
**single place downloads are managed**: a new TUI mode from Main that
lists what is on disk, deletes it with confirmation, and pre-fetches
repos into the cache ("download now") with pause/resume/cancel, sha256
verification, and clear failures. Launch stays delegated (§3.2) — the
run-mode panel keeps only the passive §15.4 progress. **Non-goals:** no
search/browse (item 7), no config-editor pickers (§3.8), no VRAM math
(R3), no router-mode changes.

**Esc keeps downloads alive.** Leaving the manager with Esc does not
cancel an in-flight download: the manager is reused on re-entry, and
Main surfaces a `⬇ downloading … — s to view` status line (refreshed on
the session tick) so a download is never silently orphaned.

**Concurrent downloads.** Several downloads may run at once; each has
its own row (spinner, progress, speed), is individually
pausable/resumable/cancellable via its action menu (or `x` for the
selected one), and Main aggregates them (`⣾ 2 downloads: a:q, b:q —
s to view`).

#### Mode structure

- New `ViewStorage` under Root; entry key **`s`** in Main (`s storage`).
  The Settings key moves from `s` to **`p`** (`p preferences` — the
  config object is `preferences`, so the label matches; owner
  decision). List-based view: cache repos + local config models +
  in-flight downloads, one pane, footer of actions — mirrors the
  router model-panel action-menu pattern (Enter → action menu).
- Rendering groups: **(1) cache repos** (from `storage.Scan` grouped by
  repo — each shows its quants + total size, `storage.HumanSize`-style),
  **(2) local config models** (each `location` file with on-disk size;
  missing files marked), **(3) in-flight downloads** (own state, §16.3
  progress), plus a **free-disk** line for the cache root's filesystem
  (`syscall.Statfs`). Sizes via `os.Stat`; all read-only rendering.
- Actions per entry: cache repo → **delete** (confirm) / **re-download**;
  local model → **reveal** (open parent dir); a *missing* local model
  additionally offers **delete from config** (confirmed — the entry is
  removed and persisted via the standard atomic save; P8's "never
  without asking" is the confirmation); download row → **pause /
  resume / cancel**.

#### Delete

- Cache repo: hub layout → remove the whole `models--org--model/` dir
  (blobs die with it); legacy → remove the flat `.gguf`/`.mmproj` files
  plus their `.etag` and `manifest=` sidecars. Confirmation prompt
  first. Config entries are never touched here (P8).
- Only files llamaman can account for are ever removed (§3.4).

#### Download action ("download now")

1. User types `org/repo[:quant]` (explicit action, P7) — or picks a
   cached repo to re-download.
2. `hf.Choose` → the §16.3 quant picker (sizes shown; no quant suffix →
   pick; suffix present → confirm only). mmproj noted when present
   (auto-downloaded by llama.cpp at launch, informational).
3. `hf.Download` fetches into the resolved cache root (§16.1) with a
   live progress bar (bytes done / total, per file and overall),
   **pause / resume / cancel** keys, sha256 verify (the LFS oid), and
   distinct failure messages (not-found / gated / network — §16.2
   errors). Pause keeps the partial; cancel discards it.

#### The downloader (`internal/hf/download.go`)

- New client call **`Refs(ctx, repo)`** — `GET /api/models/{repo}/refs`
  → the `main` branch's target commit (llama.cpp's `get_repo_commit`;
  the downloader needs the commit for `refs/main` + `snapshots/`).
- Writer mirrors llama.cpp's hub layout exactly (verified): blobs
  written to `blobs/<oid>` (partial as `<oid>.incomplete`), then
  `refs/main` = commit, and `snapshots/<commit>/<file>` as a symlink to
  the blob (`finalize_file` behavior) — so llama.cpp reads the result
  directly. Split parts download individually; the model is complete
  only when every part is in place.
- **Range resume:** a blob already at N bytes (from `<oid>.incomplete`)
  continues with `Range: bytes=N-`; 206 handled, 200 restarts cleanly.
- **sha256 verify:** after each blob completes, hash it and compare to
  the oid; mismatch → error, partial removed, clear message.
- Progress via a callback (`done, total int64` per file); cancellation
  via `ctx`. `Download` is synchronous; the TUI runs it as a Bubble Tea
  task and renders pause/resume/cancel from task state.

#### Determinism (P9)

- Downloader tests against `httptest.Server`: full download, Range
  resume (pre-seeded `.incomplete`), 206/200 handling, sha256 mismatch,
  refs parsing, split parts, cancel via ctx, blob/symlink layout
  assertions against a `t.TempDir` root. No real network.
- TUI tests with a **stub downloader** (the `stubSpawner` pattern) and
  fake cache trees: listing groups/sizes, delete-with-confirmation,
  action flow, free-disk line (`Statfs` on a TempDir works).
- Snapshot tests render the manager in-process (P9).

**File map.** `internal/hf/refs.go`, `download.go`, `download_test.go`
(the client gains `Refs`). New `internal/tui/storage.go` +
`storage_test.go` (the manager mode). `internal/tui/root.go` —
`ViewStorage`, `m` dispatch, `returnFromStorageMsg`. `main.go` —
shortcut-row text. `DESIGN.md` §16.4 + §7.5 mode list (same change,
P5). No config, no `internal/storage` changes.

### 16.5 Config-editor pickers — GGUF filepicker + cached-repo list

**Scope.** Fifth item of Release 2 (ROADMAP §3.7, delivered after the
Storage & Downloads manager per the owner's order; §3.8 **step A**). The
config editor's free-type `location` / `hf` fields (DESIGN §7.5) become
picker-assisted: a `bubbles/filepicker` overlay for local `.gguf` files,
and a cached-repo list (from `storage.Scan`) for the HF branch. This
makes the config editor the storage reader's first production consumer
(§16.1). **Non-goals:** step B — typed-repo existence check and quant
offer (`tree/main` round-trip) is item 6 (§3.8 step B); no VRAM hint;
no network in the pickers; no config schema change (P8); the pickers
never write anything — they only pre-fill the form's staging pointers,
and nothing is persisted except through the normal save flow.

**UX shape (unchanged schema, picker-assisted inputs).** In the model
form, the location and HF identifier inputs keep working exactly as
free-type inputs; a new hotkey **`ctrl+o`** ("open picker") while
focused in either input opens its picker as a centered overlay (the
§7.5 `paramPicker` pattern — a custom picker outside huh, driven by a
done message). Esc in the overlay returns to the
form with the field value unchanged; picking pre-fills the field and
returns to the form **on the same field** (no field advance). huh's
help line shows the new binding automatically.

#### The trigger: a custom huh field

huh v1.0.0 has no custom-key escape hatch: an `Input` field consumes
every key except Prev/Next/Submit (verified in `field_input.go`), so a
hotkey typed in a plain input never reaches `ConfigMode`. (huh v1.0.0's
built-in `FilePicker` field was considered and rejected: it replaces
free typing entirely, which §3.8 explicitly requires as a fallback, and
it cannot render the cached-repo list.) Instead the model form uses a
small custom huh field — `pickerInput` — that **embeds `*huh.Input`**
and overrides three methods:

- `Update`: when a `tea.KeyMsg` matches the field's `openKey`
  (`ctrl+o`), return a cmd emitting `openModelPickerMsg{kind}` **and do
  not forward the key to the embedded input**; otherwise delegate to the
  embedded input unchanged.
- `KeyBinds`: embedded input's binds plus the `openKey` binding, so
  huh's help row renders `ctrl+o open picker`.
- `View`: delegate to the embedded input (form rendering is otherwise
  byte-identical to today — no existing snapshot churn).

The rest of the `huh.Field` interface (Focus/Blur/WithKeyMap/
WithPosition/GetKey/GetValue/…) is inherited from the embedded input.
`buildModelForm` swaps the plain `huh.NewInput()` in the local and HF
groups for `pickerInput` with the matching `kind`; alias and the other
forms stay plain inputs.

`ConfigMode.Update` gains the picker-open arm **before** the
`c.form != nil` branch (alongside the existing `paramPickerDoneMsg`
arm, config.go:180): `openModelPickerMsg` → build the overlay and
return a nil cmd; `modelPickerDoneMsg` → consume and route. This keeps
the "overlay handlers run on EVERY message" discipline: the done msg
arrives as its own message through the tea loop and must be consumed
before the form ever sees it (a form left un-updated mid-flow is what
produces the swallowed-`nextFieldMsg` failure mode recorded in §16.4's
gotchas).

#### Local branch — the GGUF filepicker

The standard `bubbles/filepicker` v1.0.0 control (owner round-3: back
to the widget — the round-2 custom filterable browser was dropped; no
filtering in the local picker):

- `AllowedTypes = [".gguf"]` — other files render disabled (the
  picker's `canSelect` suffix rule; selecting one is a no-op that shows
  a brief `.gguf files only` error line, matching `DidSelectDisabledFile`).
- `ShowSize = true`; `ShowPermissions = false`; **`ShowHidden = true`**
  — hidden files/dirs are listed by default, and **`.`** toggles them
  (owner feedback; `fp.Init()` re-reads the current directory with the
  new visibility; the hint line shows the current state).
  `DirAllowed` stays **false** — directories remain navigable via
  enter, but never selectable: with it true, entering a directory sets
  `fp.Path` and `DidSelectFile` reports the directory itself as picked
  (huh keeps it false for the same reason).
- Keymap trimmed to the config-mode arrow convention (same rationale as
  `paramPicker` dropping j/k): `↑`/`↓` move, `enter` opens a directory
  or selects a file, `←`/`backspace` **always go up one level — even
  from the opening directory** (owner round-4), `esc` always cancels
  the picker. `g`/`G`/`j`/`k`/`h`/`l` removed.
- Sized via the existing `pickerSize()` height + `overlayCenter` (the
  same box treatment `paramPicker.View` uses), height through
  `picker.SetHeight`.

**Start-directory resolution**, first candidate that exists wins:

1. `preferences.models-dir` when set (the user's explicit choice wins,
   P8 — same precedence as `storage.CacheRoot`).
2. When editing an existing model whose current value is a non-empty
   path, that value's directory (the natural "last-used" for the edit
   case).
3. The directory of the **first local model** in the config (a proxy
   for "last-used model directory": llamaman keeps no usage history and
   P8 forbids adding a field to record one — flagged for the owner).
   (As implemented, the filepicker is rebuilt on every open at the
   resolved start dir; the in-session "remembers the last browsed
   directory" nicety of the original note was dropped — amending per
   P5.)
4. `~`.

If every candidate is unset or nonexistent, `~` is used. The resolved
dir is the filepicker's opening directory (where `←`/`backspace` can
still go up — only `esc` cancels); the picker reads it at construction.

#### HF branch — the cached-repo list

On `ctrl+o` in the HF input, resolve the cache root with
`storage.CacheRoot(c.work.Prefs().ModelsDir)` — the same call root.go
makes for `ViewStorage` (§16.4) — and `storage.Scan(root, warn)`.
Grouping and formatting reuse the Storage manager's repo-row logic
exactly (the same helpers, so both surfaces always agree): group
`CachedFile`s by `RepoID` (`byRepo` map, sorted keys), quants + sizes
via `hf.Quants(repoFiles(fs))` + `hf.HumanSize` — both package-level in
`internal/tui`/`internal/hf` already.

The overlay is a `bubbles/list` picker in the `paramPicker` shape
(arrows-only keymap, reverse-video selection, no
chrome). The repo list is sized to **half the screen width**
(`width/2 - 6` inner), and the popup box is padded to exactly
`width/2` cells (owner round-4: no full width) — the enclosing
rectangle never changes size when the selection moves (owner round-3:
all four row styles share the same 2-cell left padding, every line is
padded — and truncated if longer than the list — via the
`repoPicker.View` pass, and the box is right-padded via `padLinesTo`).
The delegate ellipsizes anything longer than the screen, it never
wraps. ROADMAP §3.8 names
`huh.Select`, but per-option descriptions
(quants + sizes) are exactly what `huh.Select` cannot render — the same
reason `paramPicker` exists — so the custom list picker is used
instead; the mechanism (overlay outside huh driven by a done message)
is the one §3.8 itself prescribes to reuse. Rows:

- One per cached repo: title `org/repo`, description =
  `Q4_K_M — 4.2 GB, Q8_0 — 8.4 GB` (comma-joined, `hf.HumanSize`), or
  `no model quants cached` when the repo has files but no quants (e.g.
  only mmproj — same "empty cache repo" rule as the manager).
- A trailing **`select a repo…`** row, always present (never
  filterable out); selecting it closes the picker and lets the user
  type an id in the field (owner-chosen label).
- **Live filter**: typing filters as you go and `enter` picks the
  selected repo directly; `esc` clears the filter first. `enter`
  picks; `esc` cancels (both emit `modelPickerDoneMsg`).
- **Empty cache** (zero repos) → the picker does not open; the field
  stays a plain free-type input (§3.8: "an empty cache skips the
  list"). Scan errors / unresolvable root → same no-op (P3; never a
  blocking error in a form).

**Pre-fill rule** (writes into the form's staging `hf` pointer, so the
input displays the value and the form continues from that field):

- repo with exactly one quant → `org/repo:QUANT`
- repo with several quants, or none → `org/repo` (no suffix; §3.8:
  "quant empty when the repo has several")
- `type a new repo…` → field untouched (keeps whatever was typed)

#### Routing and state

`ConfigMode` gains one overlay slot, `modelPicker *modelPicker`
(active only during `formNewModel`/`formEditModel`), holding the kind
(`local`|`hf`) and either the filepicker or the list model. Routing,
mirroring the existing `picker` slot (config.go:160–199):

1. `modelPickerDoneMsg` → consume, write staging if not cancelled,
   clear the slot, return to the form (no cmd).
2. `modelPicker != nil` → forward the message exclusively to the
   overlay (the form underneath is shielded while it is open).
3. otherwise the existing routing (form → keys) continues unchanged.

`SetSize` propagates to the overlay like it does for `picker`;
`View` renders the overlay via `overlayCenter` when the slot is set,
ahead of the form (same precedence as the param picker).

#### Determinism and tests (P9)

- `pickerInput` unit tests: `ctrl+o` emits `openModelPickerMsg` and
  does not mutate the input value; other keys (typing, arrows, enter)
  delegate to the embedded input unchanged; `KeyBinds` includes the
  binding.
- Start-dir resolution table test: models-dir set/unset, edit-value
  dir, first-local-model dir, nonexistent candidates falling through,
  home fallback — over `t.TempDir` trees.
- ConfigMode flow tests (synchronous, the existing config_test.go
  style): open the model form, drive to the location field, send
  `ctrl+o` → overlay active; a temp dir holding `model.gguf` +
  `notes.txt` renders with the `.txt` disabled; `enter` on the `.gguf`
  → done msg → staging `location` set and the form is still on the
  location field. Esc cancels without touching the value.
- HF branch: fake hub-layout cache trees in `t.TempDir` (the
  `scan_test.go` fixture style: `refs/main` + `snapshots/<commit>/`
  symlinked files): single-quant repo pre-fills `org/repo:QUANT`,
  multi-quant pre-fills `org/repo`, empty cache makes `ctrl+o` a no-op,
  `type a new repo…` leaves the field as typed.
- Overlay render assertions (in-process, deterministic temp dirs);
  existing snapshots are unaffected (the form view is unchanged apart
  from the help row).

**File map.** New `internal/tui/modelpicker.go` + `modelpicker_test.go`
(the `pickerInput` field, both overlays, the done/open messages,
start-dir resolution; the form-flow test harness reuses the
`snapshot_test.go` `drainCmds` via a `tea.Model` adapter). `internal/tui/config.go` — `buildModelForm` uses
`pickerInput`, `Update` gains the two message arms and the overlay
slot, `SetSize`/`View` propagate it, `handleModelPickerDone` rebuilds
the form's cached view after a pre-fill. `DESIGN.md` §16.5 + the §7.5 Models
pane bullet (same change, P5). No changes to `internal/storage`,
`internal/hf`, the config schema, or ROADMAP.

### 16.6 Typed-repo check + quant offer — config editor, §3.8 step B

**Scope.** Sixth item of Release 2 (ROADMAP §3.7 item 6, §3.8 **step
B**), delivered after the Storage & Downloads manager and the config-
editor pickers per the owner's order. When the user confirms a **typed,
bare** `org/repo` id in the model form's HF field, one async `tree/main`
call (via the §16.2 client) checks existence and fetches the quant list
with real sizes; on success the shared quant chooser (§16.3 data, §16.4
UI shape) offers the quants, and the picked quant is written back as the
`:quant` suffix. **Non-goals:** no VRAM math (the §14.3 estimator is a
render-time hook, sizes-only until it ships), no existence validation
for ids that already carry a `:quant` (that is R3's pre-spawn "HF repo
existence validation", §14.3 — a quanted id is an explicit choice and
llama-server surfaces problems at launch), no mmproj handling (llama.cpp
auto-downloads it; presets already set `no-mmproj`), no config schema
change (P8), no new dependency, no `internal/hf` change (the check
composes the existing exported `Tree` + `hf.Quants` + `hf.HasMMProj`).

**Trigger — what counts as "a typed repo id".** The check fires only
when the user **confirms the HF field with enter** (the "explicit user
action" P7 requires) and all of these hold:

- the id is **valid** (`hfFormValidator`, the same check the form runs
  today — otherwise the key is delegated so huh shows its inline error),
- the id is **bare** — no `:quant` suffix (the check's point is the
  quant *offer*; a suffix means the quant is already decided),
- the id was **typed in this form session** — tracked by a new `edited`
  flag on `pickerInput`, set whenever the field's `Update` sees any key
  other than `ctrl+o`/enter. A pre-fill from the cached-repo picker
  (§16.5) goes through `RefreshValue()` *outside* `Update`, so `edited`
  stays false for picked ids; an unchanged id in edit mode likewise
  skips the check. Typing anything (even a single backspace fix) flips
  the flag — the check is advisory, so over-eagerness is acceptable.

The cache list is the offline path (no network), and an unchanged
existing entry was never "typed" — both skip. When the check is
skipped, enter behaves exactly as today (delegate to the embedded
input; the form advances normally). When the runner is unavailable
(see below), the same.

**Mechanism.** `pickerInput.Update` (modelpicker.go:94) gains, after the
`ctrl+o` arm and before delegating: when `kind == sourceHF` and the key
is enter and `edited && hfFormValidator(*p.value) == nil && bare` —
return the wrapper unchanged plus a cmd emitting a new
`hfCheckRequestedMsg{id: strings.TrimSpace(*p.value)}`, **without**
forwarding the key. Swallowing the enter means the form does not
advance; the user stays on the HF field while the check runs. The
validator gate keeps huh's inline error display intact for invalid ids.

**The async check.** ConfigMode gains:

- `hfRunner hfCheckRunner` — `CheckHF(ctx, repo) ([]hf.QuantOption,
  bool /*mmproj*/, error)`, nil = check disabled (P3: the form just
  advances). The production adapter `hfCheckClient{c *hf.Client}`
  composes one `Tree(repo, "main")` round trip into `hf.Quants` +
  `hf.HasMMProj` — the ROADMAP's "existence + quant list + sizes + LFS
  sha256 in a single round-trip". Wired via a new `SetHFCheckRunner`
  setter, mirroring `StorageMode.SetEngine` (storage.go:183); root
  builds a lazy `hf.New()` at the two ConfigMode creation sites
  (root.go:272, 457) and a nil-safe adapter (client error → runner nil
  → check disabled, never a crash).
- `hfCheck *hfCheckState{repo, cancel}` — the in-flight state. On
  `hfCheckRequestedMsg`: guard `formKind` is new/edit model, split the
  id (`splitRepoQuant`, storage.go:748 — bare, so repo = trimmed id),
  create a cancellable ctx, set the slot, and return the check cmd
  (`func() tea.Msg { opts, mmproj, err := runner.CheckHF(ctx, repo);
  return hfCheckDoneMsg{id, opts, mmproj, err} }` — the same cmd-returns-
  a-msg shape as `fetchPropsCmd`, fetcher.go:230). The request is
  bounded by the §16.2 client's 30s `requestTimeout`; esc cancels the
  ctx (P10).
- **Shield:** while `hfCheck != nil`, every key is swallowed except
  `esc` (cancel the ctx, clear the slot, stay on the HF field) and the
  `hfCheckDoneMsg` itself. `View` renders a small centered box
  (`overlayCenter`, `pickerSize`) — static text
  `checking org/repo…` + `esc: cancel` (static, no spinner — the check
  is one bounded call and static text keeps snapshot tests
  deterministic).
- **Cancel surfaces as cancellation:** the §16.2 client wraps the
  transport error (including a canceled request) into an `hf.Error`
  with no `Unwrap`/`Is`, so the adapter re-raises `ctx.Err()` when the
  ctx is done — `errors.Is(err, context.Canceled)` must match for esc
  to abort cleanly. Each check gets a generation counter
  (`hfCheckGen`); the done msg carries it and `handleHFCheckDone`
  drops any result whose gen does not match the current check (a stale
  msg from a canceled earlier check must never resolve — or clear the
  shield of — a later one).

**On `hfCheckDoneMsg`** (handled on every message, before the errorModal
arm, alongside the other overlay-done messages — the §16.4 discipline):

- `ctx.Canceled` → no dialog, no chooser; clear the slot, stay on the
  HF field (the user aborted).
- success, `len(opts) > 0` → open the **quant chooser** (below).
- every other outcome (no quants, not-found, gated, network/HTTP) →
  a **Save/Dismiss dialog** (owner round: a flash was too quick to
  read), titled with the distinct message (`org/repo: no GGUF files
  found` + ` (mmproj only)` when `HasMMProj`; `org/repo: not found on
  Hugging Face`; `org/repo: gated — requires HF_TOKEN`; `org/repo:
  could not reach Hugging Face` / `org/repo: HTTP <code>`). **Save**
  completes the form via `c.form.NextField()` (huh v1.0.0 exports it,
  form.go:488 — the gotcha's "forms complete on a follow-up
  nextFieldMsg" chain drives completion; the HF field is the last
  visible field, so `applyForm` saves the model with the typed id
  as-is) — non-blocking per ROADMAP §3.8: "the id can still be saved
  (llama-server surfaces it at launch)". **Dismiss** (or `esc`) closes
  the dialog and returns to the HF field, nothing committed — the
  model form stayed alive underneath (its enter was swallowed), so the
  user can fix the id and re-confirm. The dialog is a dedicated
  `hfFail *huh.Form` slot with a `huh.Select` (Save pre-selected),
  rendered boxed like the chooser.

**The quant chooser.** Reuses the §16.4 label shape exactly — the same
`Tag — hf.HumanSize(Size)` rows plus ` (cached)` markers from
`storage.Lookup(root, repo)` when the cache root resolves (P3: lookup
failure → empty marker set). Extracted into a shared
`quantChooserForm(repo string, opts []hf.QuantOption, cached
map[string]bool, note string, value *string, maxRows int) *huh.Form`
helper (new `hfcheck.go`); `StorageMode.openQuantPicker`
(storage.go:756) switches to it so both hosts always agree (the same
"same helpers" rule item 5 applied to the repo list). The mmproj note
(`mmproj present — llama.cpp auto-downloads it`) rides the form's
`Description` as an informational line only.
**VRAM hint:** sizes-only; per §16.3 the hint is a render-time addition
hosts attach when the §14.3 estimator ships — no coupling here. The
chooser is a dedicated ConfigMode slot `hfQuant *huh.Form`, rendered
via `overlayCenter` ahead of the form **inside the standard bordered
popup box** (`overlayBox` — the same treatment the pickers and the
checking overlay use; owner round: it rendered unboxed and diverged
from the app's style), sized like §16.4's form (`formWidth()`).
`maxRows` caps the visible option rows to the host height minus
overhead, so the box always fits small terminals — huh Select's
viewport scrolls past the cap (owner round: an uncapped list
overflowed the screen and its bottom rows were clipped).

Chooser exits: **enter on a quant** writes `repo + ":" + tag` into the
staging `hf` pointer, calls `hfField.RefreshValue()` (the item-5
gotcha: the input's internal text must be re-synced or the form's
`GetValue` at completion saves the stale bare id), then `NextField()`
— the form completes and `applyForm` persists `org/repo:QUANT`.
**`esc` saves the bare id** (owner round: the keep-bare row was
dropped — the Save/Dismiss dialog already covers "keep the id", so the
chooser needs only two exits, pick-quant and no-quant; esc is never
forwarded to the form, which would abort it). `esc` always means "no
quant" regardless of the current selection, because huh.Select syncs
its bound value on focus — `hfQuantVal` cannot distinguish "picked"
from "initial selection"; only enter commits a pick. The chooser's
completion msgs are routed on every message while `hfQuant` is set,
same discipline as the other overlays.

**Routing and state.** `Update` order (config.go:168): `savedExpiredMsg`
→ `hfCheckDoneMsg` → errorModal → `hfCheckRequestedMsg` → `hfCheck`
shield → `hfQuant` routing → the existing picker/modelPicker/form arms.
The new slots are mutually exclusive with the existing overlays, so the
relative order within the overlay block is immaterial; `dismissForm`
clears `hfCheck`/`hfQuant` defensively. `SetSize` propagates to the new
overlays like it does for `modelPicker` (config.go:151).

**Determinism and tests (P9).**

- `pickerInput` unit: enter on the HF field with a typed bare id emits
  `hfCheckRequestedMsg` and does **not** advance; quanted id → delegates
  (no msg); invalid id → delegates (huh shows the error); `edited ==
  false` (fresh edit-form, or after a picker pre-fill) → delegates;
  local field → delegates; `ctrl+o` still wins (checked first).
- ConfigMode flow (the modelpicker_test harness + `drainCmds`): drive
  the model form to the HF field, type a bare id, enter → checking
  overlay renders; esc → canceled, no flash, still on the field; stub
  runner (synchronous, `SetHFCheckRunner`) → success+quants → chooser
  renders sizes + mmproj note **inside the bordered box**; enter on a
  quant → staging `org/repo:QUANT`, form completes, model applied
  with the suffix; esc on the chooser → bare id saved (no quant);
  `ErrNotFound` / `ErrGated` / network / no-quants → the Save/Dismiss
  dialog opens with its distinct message and **Save** completes the
  form with the bare id, **Dismiss**/esc returns to the HF field with
  nothing committed (the id stays editable and re-checkable); runner
  nil → enter completes the form with no check.
- `hfCheckClient` adapter: `httptest.Server` tree fixture → quants +
  mmproj derived from one response; the §16.2 typed errors map through
  unchanged.
- Existing snapshots are unaffected: the model form's `View` is
  byte-identical (the enter intercept changes no rendering, and the
  help row gains no binding).

**File map.** New `internal/tui/hfcheck.go` + `hfcheck_test.go` (the two
messages, `hfCheckRunner` + `hfCheckClient` adapter, the check cmd, the
checking overlay, `quantChooserForm`). `internal/tui/modelpicker.go` —
`pickerInput` gains `edited` + the enter-intercept. `internal/tui/config.go`
— `SetHFCheckRunner`, the `hfCheck`/`hfQuant` slots, the Update arms +
ordering, `View`/`SetSize` propagation, `handleHFCheckDone`,
`handleQuantChooserDone`. `internal/tui/storage.go` — `openQuantPicker`
switches to the shared `quantChooserForm`. `internal/tui/root.go` — wire
the lazy real runner at the two ConfigMode creation sites.
`DESIGN.md` §16.6 + the §7.5 Models pane bullet (same change, P5). No
changes to `internal/hf`, the config schema, or ROADMAP.

### 16.7 HF model browser — search/browse Hugging Face from the TUI

**Scope.** Final item of Release 2 (ROADMAP §3.5, §3.7 item 7), delivered
after the Storage & Downloads manager, the config-editor pickers, and the
typed-repo check per the owner's order. A new **Browse** mode from Main
searches/browses Hugging Face inside the TUI: a search box with
server-side tag filters (language, license) — the HF search endpoint
with the `gguf` library filter — a result list (downloads / likes /
license / languages / task), a metadata + quant pane for the selected
repo (real sizes
from one `tree/main` round trip, `(cached)` markers), and a **hand-off** of
the picked `org/repo:QUANT` straight into either the config editor's new-
model form or the Storage manager's download action. **Non-goals:** no
download loop here (the manager owns downloads — §16.4), no model-card
parsing for params/context (see scope cut below), no write path of its own
(hand-offs go through the existing editor save flow and the existing
download engine), no config schema change (P8), no pagination beyond one
page (see scope cut below), no VRAM math (R3).

**API reality check (verified against the live API at the time of
writing).** The search endpoint is

```
GET {endpoint}/api/models?search=<q>&filter=gguf&sort=<field>&direction=<±1>&limit=<N>
```

and returns a **plain JSON array** of repo objects with `id` (+ `modelId`
alias), `downloads`, `likes`, `tags` (carries `license:<id>`,
`base_model:<id>`, and language-code entries), `pipeline_tag`,
`createdAt`, `private`. Three facts shape the design:

1. **No file sizes in the search response — not even with `full=true`**
   (verified: `full=true` adds `sha`, `lastModified`, `siblings` whose
   entries carry only `rfilename`, `library_name`, `gated` — no size, no
   LFS info). Per-repo sizes therefore require the existing `tree/main`
   round trip; the design fetches it once, per selected repo, on the
   user's explicit enter (§16.6's P7 discipline), and reuses the existing
   §16.3/§16.6 machinery for quants + sizes + `(cached)`.
2. **No server-side quant or size filter.** `filter=gguf` is the only
   library filter; quant tags and file sizes are not queryable. "Filter
   by quant/size" (§3.5) is therefore client-side: the search query
   itself, plus the per-repo quant pane that lists every quant with its
   real size (a GiB-budget filter over that pane was proposed and **cut
   by owner decision** — see scope cuts). Quant *names* are served by
   the search box (repo-level) and the quant pane (file-level).
3. **Tags are a server-side browse axis.** Every search hit carries
   `license:<id>`, bare language codes (`en`, `ja`, …), task tags, and
   `base_model:<id>` / `base_model:quantized:<id>`. The `filter` param
   is comma-joined and accepts **any** tag — `filter=gguf,ja` and
   `filter=gguf,license:cc-by-nc-4.0` both verified live — so language
   and license filters are one query param, zero client-side logic
   (owner decision: included; §3.5's "filter by quant/size" reading).

**Scope cut — params/context metadata (owner decision).** ROADMAP §3.5
lists "params, context, license" as metadata. `license` comes free from
`tags`; **params (parameter count) and context length are not fields of
the search or repo APIs** — they live in model cards / `config.json`,
which would mean fetching and parsing card text per repo (heavy, brittle,
and only present on a subset of GGUF repos). This note cuts them: the
metadata pane shows downloads, likes, license, task, base model, and
repo-commit recency where cheap, and the quant pane carries the size
story. Params/context are a deferred extension ("read the
model card" button) — the owner confirms the cut or the note grows a card
parser. This is the §14.2 "largest item" pressure valve.

**Scope cut — size filter (owner decision).** A client-side GiB-budget
filter hiding over-budget quant rows was proposed as the "filter by
size" reading of §3.5; the owner cut it — the quant pane is sizes-only.
The R3 VRAM estimator (§14.3) is the natural future home of a
budget/fits-VRAM filter.

**Scope cut — pagination.** One request of `limit=50` (verified
accepted; the API returns up to at least 110 in practice) ranked by
downloads; a "load more" / cursor loop is deferred. Search is
stateless and re-runnable, so the browse loop stays useful without it.

#### The client (`internal/hf/search.go`)

The §16.2 client gains one method (its "browser extends later" hook from
§16.2). No changes to existing methods or types:

```go
type SearchOpts struct {
    Query     string // "" = browse: top GGUF repos by sort
    Limit     int    // 0 → 50
    Sort      string // "downloads" | "likes" | "lastModified"; "" → downloads
    Direction int    // -1 desc (default), 1 asc
    Filter    []string // extra tags beyond the fixed "gguf", in order
                       // (e.g. "ja", "license:apache-2.0")
}

type SearchResult struct {
    ID          string
    Downloads   int64
    Likes       int64
    Tags        []string // raw: "license:*", "base_model:*", language codes
    PipelineTag string
}

func (c *Client) Search(ctx context.Context, opts SearchOpts) ([]SearchResult, error)
```

- `filter=gguf` always, then `opts.Filter` tags appended in order
  (`filter=gguf,ja,license:apache-2.0`); `search` omitted when empty;
  query and each filter tag are URL-escaped per segment (same escaping
  rule as §16.2). Errors map
  through the existing typed `hf.Error` kinds (404 → `ErrNotFound`,
  401/403 → `ErrGated`, transport → `ErrNetwork`, other → `ErrHTTP`)
  and the Bearer-token rule (HF_TOKEN) applies unchanged.
- Decode only the fields above; unknown fields ignored (forward
  compatibility). Malformed JSON → error.

#### Browser mode (`internal/tui/browser.go`)

New `ViewBrowser` under Root; entry key **`b`** in Main
(`b browse` — free in the current shortcut row; the reattach row gains
it too), shortcut text and help line updated (main.go:544–580,
654–666). Mirror of the `ViewStorage` wiring: a Root-owned
`browser *BrowserMode` reused across entries (its search state and
loaded quants survive Esc), `openBrowser()` builds it lazily like
`openStorage` (root.go:563) — `storage.CacheRoot(r.cfg.Prefs().ModelsDir)`
for the `(cached)` markers — and injects the runner.

**Runner injection.** Same lazy, nil-safe pattern as §16.6's
`hfCheckRunner` (root.go:549–557):

```go
type browserRunner interface {
    Search(ctx context.Context, opts hf.SearchOpts) ([]hf.SearchResult, error)
    // CheckHF is the §16.6 tree/main check — one round trip yields the
    // quant list, sizes, and mmproj presence for the quant pane.
    CheckHF(ctx context.Context, repo string) ([]hf.QuantOption, bool, error)
}
```

`SetBrowserRunner` setter; `r.browserRunner()` builds a lazy `hf.New()`
(the production adapter reuses the §16.6 `hfCheckClient` for the check);
client error → nil runner → search disabled (P3: the mode renders and
Esc works; search shows a "search unavailable" flash, never a crash).

**Layout — three zones, Tab cycles.** The mode is a static layout, not
an overlay (this is a full screen like the Storage manager, not a form
popup):

```
┌ browse — Hugging Face (gguf) ────────────────────────────────┐
│ ╭ search ──────────────────────────────────────────────────╮ │
│ │ search: [llama 3          ]  sort: downloads            │ │
│ ╰─────────────────────────────────────────────────────────╯ │
│ ╭─ results (2) ───────────╮ ╭─ model info ────────────────╮ │
│ │ ▶ lm-anon/vntl-llama3…  │ │ org/repo                   │ │
│ │   743k dl · 17 likes …  │ │ from meta-llama/Llama-3…   │ │
│ │   bartowski/Meta-Llama… │ │ ────────────────────────   │ │
│ │   …                     │ │ ⬇ 743.5k downloads        │ │
│ └─────────────────────────┘ │ ♥ 17 likes                 │ │
│                             │ license: llama3.1          │ │
│                             │ task: text-generation      │ │
│                             │ ⚠ non-commercial license   │ │
│                             │ ────────────────────────   │ │
│                             │ quants (2)                 │ │
│                             │ ▶ Q4_K_M — 5 GiB ●cached   │ │
│ ─────────────────────────   │   Q8_0 — 10 GiB            │ │
│ ↑/↓ navigate · tab quants · l/L filter · t sort · esc    │ │
└─────────────────────────────────────────────────────────────┘
```

**Layout — three zones; the panes follow the cursor.** The model-info
+ quants + card panes **auto-update as the results are navigated**
(owner flow): moving in the results list re-renders the right column
instantly from the search response and fires one gen-guarded
`tree/main` fetch (quants) plus a `raw/main/README.md` fetch (card) in
the background — no enter needed on a result, so enter is free for
selecting a quant. **All panels carry their title embedded in the top
border line** (`╭─ search ─…`, `╭─ results (N) ─…`, `╭─ model info ─…`,
`╭─ quants (N) ─…`, `╭─ model card ─…`), drawn manually (`titledBoxLines`
— lipgloss `Width()` wraps long lines instead of truncating, which
previously pushed boxes past their allocation; `truncatePad` clamps
content). The sort indicator sits at the top right of the search
panel with the input width reserved so it never overlaps the border,
and the value is accent-bold with a friendly label. **The default sort
is `trendingScore`** (owner round — browse *and* search start at
"trending", HF-site parity); the **`s` key** (owner round: renamed from
`t`) cycles trending → downloads → likes → newest → updated. There is
**no `search: ` prompt** — the panel's `search` title makes it
redundant (owner round); the placeholder reads `search Hugging Face…
(empty = browse)`. **The search bar is part of the tab cycle**: tab cycles search → results → quants → search; esc still backs
out one step. **The focused panel's border lights up** (`BorderFocus`,
the router-mode pattern) — search / results / quants panels; the info
and card panels are display-only. Result rows are colored (titles
Subtle, descriptions Muted, selection accent-bold on reversed
background). **Browsing without a search term works** — an empty query
lists the top GGUF repos by the current sort (the placeholder reads
`search Hugging Face… (empty = browse)`).

- **focusSearch** — a `bubbles/textinput` line inside its thin box
  (accent-bold `search: ` prompt). Typing goes to it; `enter` runs the
  search — or, with an empty query, **browses** the top GGUF repos by
  the current sort (gen-guarded async, shield + `esc: cancel` like
  §16.6); `esc` returns to Main. The input is re-shown pre-filled with
  the last query so edits re-search. (Sort cycling and the filters
  live in the results zone — every printable key must type, the §16.5
  modifier precedent.)
- **focusResults** — a `bubbles/list` (the §16.5 repoPicker delegate
  shape: **arrows-only** keymap — page jumps, help, and quit unbound —
  no chrome, colored rows) over the results; row = `org/repo`,
  description = `Nk downloads · N likes · <languages> · license: <id>`
  (languages = the bare tag codes, space-joined; license pulled from
  `tags`). Moving the cursor (or a fresh search's first hit) runs the
  auto-fetch: the §16.6 async discipline (cancellable ctx, per-request
  gen counter, `ctx.Canceled` re-raised by the adapter because the
  §16.2 client swallows it — the item-6 gotcha, hfcheck.go:64–77), but
  **background** — no full-screen shield (that would flicker during
  fast navigation): the pane shows an inline `loading quants…` line and
  a superseded fetch is cancelled and gen-dropped. `esc` returns to
  focusSearch. In this zone **`s` cycles sort trending → downloads →
  likes → newest → updated** (re-runs the same query, same gen guard)
  and `l`/`L`/`k`/`m` open the filters (below).
- **Right column — three titled panels** (owner round), stacking to the
  results height so the column bottom-aligns:
  1. **model info** (content-sized) — repo name (accent bold); the
     **params line** `8B params · from <base_model>` (count
     StatusReady-green, "from" Muted, base Subtle; **name-derived** —
     the search API has no params field, so `paramCountOf` regexes the
     `8B`-style suffix out of the base-model/repo id, a flagged display
     heuristic); a blank line (owner round: the `─` separator is an
     empty line now); `⬇ N downloads` (count green) and
     `♥ N likes` (count accent); `⚖ license: <id>` and
     `▷ task: <pipeline_tag>` (label Muted, value Subtle); a
     **⚠ non-commercial license — check terms** warning for `cc-by-nc*`
     (P3: display only).
  2. **quants (N)** — a **fixed 5-row window** (owner round: the panel
     is a short 7-line box — title border + 5 rows + bottom border —
     so the model card panel below gets every freed line); the window
     follows the cursor (standard list behavior) and the `quants (N)`
     title carries the total count. Rows `Tag — hf.HumanSize(Size)`
     with the size in Muted plus the green `● cached` badge when
     `storage.Lookup(root, repo)` marks it (the storage.go:764–769
     logic); the mmproj note lives in the model info panel now (repo
     level, and the quants box has no spare slot). This panel is the
     tab focus target of the quants zone: ↑/↓ select a quant, enter
     opens the hand-off dialog.
  3. **model card** — the README text, fetched alongside the quants
     (new `hf.Client.Card` — `GET {endpoint}/{repo}/raw/main/README.md`,
     the §16.6 async discipline with its own gen/cancel; YAML
     frontmatter trimmed — **robustly**: some cards put HTML comments
     before the frontmatter, so the first `---` line is found anywhere
     in the card head, owner round), then **rendered from markdown to
     styled lines** (`internal/tui/markdown.go` — goldmark v1.7.11 +
     GFM, a custom `cardRenderer` emits lipgloss-styled text instead
     of HTML). Renderer notes (round 7): the custom renderer registers
     at **priority 100** — `extension.GFM` adds its own HTML table
     renderer at 500 and goldmark lets the LAST registered func win,
     so without the lower priority raw `<table>`/`<th>` HTML leaked
     into cards (owner report: unsloth cards); **blank lines follow a
     sections-only policy** (owner round): headings, thematic breaks,
     code blocks, tables and lists get one blank line before AND after
     them, while consecutive paragraphs flow together (thematic breaks
     and tables used to drop their after-blank entirely — fixed;
     **consecutive quoted lines flow together** — goldmark parses
     `> a` blank-separated lines as separate Blockquote nodes, and the
     inner paragraph's newline already separates them, so only the
     last line of a quoted run adds the trailing blank;
     **list items add their newline only when the content didn't end
     with one** — blank-separated `*`/`-` items are Paragraph-wrapped
     (their paragraph newline already separates them; without the
     guard every bullet got a blank after it, owner report:
     RichardErkhov/microsoft_-_phi-1-gguf), while simple items are
     TextBlocks that need the item's own newline);
     **links and
     autolinks are OSC 8 terminal hyperlinks** — `ESC]8;;URL ESC\
     … ESC]8;; ESC\\` — so ctrl/cmd-click opens the URL in the
     user's browser (owner round); **the OSC 8 wrap is the OUTERMOST
     transformation** — a style applied after it (e.g. a heading
     wrapping the link) would mangle the escape sequence and kill
     ctrl+click (owner report: the Guide! link inside the unsloth
     card's heading). **The panel never re-styles card lines** — the
     renderer bakes a Subtle base color into plain text, and
     cardPanelLines passes the lines through untouched, because a
     lipgloss re-style strips the OSC 8 sequences (links rendered but
     dead) and a corrupted one garbles the whole view (owner report:
     's' broke the layout once a link line was on screen); raw HTML
     skipped; tables render
     cell-separated (`a │ b`). Windowed and scrollable — **`pgup`/
     `pgdown` scroll the card from ANY zone** (owner round: moved out
     of the quants-zone key handler into the browser-wide routing; the
     footer advertises `pgup/pgdn` in every zone, compactly; **the
     footer (and flash/filter lines) are clamped to the content width
     and the empty-label shortcut emits no trailing space** — the outer
     box sizes itself to its widest line and Place cannot shrink it, so
     an unclamped footer pushed the box past the terminal, clipping the
     right edge of the whole view on narrow terminals (owner report:
     's' broke the layout — the results-zone footer is the widest, and
     `shortcut("pgup/pgdn", "", …)` added a trailing space making it 71
     chars, overflowing anything ≤ 77 cols)) — with a
     **scroll indicator** `NN% ▰▱▱▱▱▱▱▱▱▱` (10-dot bar filling as you
     scroll). Friendly non-blocking states: `loading card…`, `no model
     card` (404), `could not load model card` (other). The panel takes
     the column's remaining height — `quantsH = min(7, rem)`,
     `cardH = rem - quantsH` — so the card grows as much as the
     terminal allows. **The Preferences theme reaches the browser**:
     Root.applyTheme (Settings save / `t` cycle) pushes the resolved
     palette into every live mode — the browser (and the Storage
     manager) are lazily created once and reused, so a theme changed
     after their creation must still apply (`SetTheme`; the rendered
     card is re-rendered under the new theme, and the results list is
     rebuilt — its delegate captures palette colors at creation, so it
     would otherwise stay on the stale palette, the owner's remaining
     report after the first push; the cursor survives the rebuild).
- **focusQuants** — the quant list with its own cursor (↑/↓); `enter`
  on a quant opens the **hand-off dialog** (below); a repo with no
  GGUF quants shows a `use org/repo without a quant` row that hands
  off the **bare** id (same semantics as the chooser's esc-saves-bare
  and §16.6's Save path); while a fetch is in flight `enter` is a
  no-op. `esc` returns to focusResults.

Tab / Shift+Tab **toggle the results/quants pair** (the pane follows
the cursor, so tab just picks which side you act on); from the search
box either direction lands on the results. The footer is **zone-
aware** (owner): the quants zone advertises `↑/↓ quant · enter hand off
· tab results · esc back`, the results zone `↑/↓ navigate · tab quants
· l/L filter · t sort · esc search`. Each zone's key handling is
exclusive (the same "overlay handlers run on EVERY message" discipline
— zone messages are consumed in `Update` before anything else).

**Filters (`l` / `L` / `k` / `m`, results zone).** Curated overlays —
the §16.6 dialog pattern (boxed, height-capped huh select, dedicated
slot `tagFilter *huh.Form` + `tagFilterVal`). The keys live in
**focusResults**, not focusSearch: the search box must own every
printable key (typing `llama` starts with `l`), the same reason §16.5's
picker hotkey is a modifier (`ctrl+o`) — the owner's keys are
preserved, just where no text entry happens:

- `l` — **language**: `all languages`, en, es, de, fr, it, pt, ja, zh,
  ko, ru, ar, hi, th, multilingual → `Filter: ["ja"]`.
- `L` — **license**: `any license`, apache-2.0, mit, llama3.1/3.2/3.3,
  gemma, openrail, cc-by-nc-4.0, other → `Filter: ["license:<id>"]`.
- `k` — **task** (owner round; verified server-side): `any task`,
  text-generation, translation, text2text-generation,
  feature-extraction, sentence-similarity, image-text-to-text → the
  `pipeline_tag` query param (a new `SearchOpts.PipelineTag` field).
- `m` — **params min/max** (owner round): a boxed two-input form (min /
  max in billions, empty = none). **Client-side and page-scoped** — the
  search API has no params field, so the filter prunes the fetched page
  through the name-derived `paramCountOf` heuristic (repos whose count
  is unparseable are kept); the header shows `filter: 7B-70B`.

Language/license/task combine into the request (`filter=gguf,ja,
license:apache-2.0` + `pipeline_tag=text-generation` — all verified
server-side), re-run with the same gen guard as sort changes; the
params filter applies locally on top. Each picker's clear row (`all
languages` / `any license` / `any task` / empty-empty) resets it; esc
**dismisses without changing the filter**. The header renders the
active filters (`filter: en · apache-2.0 · text-generation · 7B-70B`,
key hint `(l/L/k/m)`). Escaping a tag uses the same per-segment rule as
the query. The curated lists are package-level constants (the "same
helpers" rule — the picker options and the request assembly both read
from them), so the two surfaces can never disagree.

**Hand-off dialog.** `enter` on a quant (or the no-quant row) opens a
boxed, height-capped huh select — the §16.6 dialog pattern
(hfFail/hfQuant: dedicated slot, `overlayBox`, `maxRows` = height −
overhead):

- **`add to config`** → emits `browserConfigHandoffMsg{id}`. Root opens
  the config editor's **new-model form pre-filled**: `configEntry`
  (root.go:450) gains `prefillHF string`; `openConfig` sets a new
  `ConfigMode.prefillHF` and `openNewModelForm` (config.go:844) seeds
  the staging `source = sourceHF` and `hf = prefillHF` instead of `""`
  (cleared after use). The pre-fill goes through the staging-pointer +
  `RefreshValue()` path, so `edited` stays false and the §16.6 check
  does **not** fire on the pre-filled id (correct: it was never typed).
  The form opens on the alias field; the user names the model and saves
  normally (the existing save flow, P8).
- **`download now`** → emits `browserDownloadHandoffMsg{id}`. Root opens
  the Storage manager (`openStorage()`, reusing the live instance so
  existing downloads survive) and starts the download directly:
  `splitRepoQuant(id)` → `r.storage.startDownload(repo, quant)` +
  `rebuild()` + `focusDownloadRow()` (the openStorage pattern,
  root.go:573–593). Downloads stay in the manager — the single place
  downloads are managed (§16.4); the browser never spawns its own rows.
- **`cancel`** → closes the dialog, stays on the quant pane.

`esc` in the dialog = cancel. Errors during the quant fetch (not-found /
gated / network / HTTP — the §16.2 kinds) flash the distinct message in
the browser footer and leave the pane on the metadata-only state, so the
user can still hand off the bare id (mirrors §16.6's Save path). The
runner-nil case (search disabled) also disables hand-off (flash only).

**Routing and state.** `BrowserMode` fields: `query string`, `input
textinput.Model`, `sort string`, `filterLang, filterLic, filterTask
string`, `paramMin, paramMax float64` (billions; 0 = none),
`allResults []hf.SearchResult` (the fetched page; the displayed
`results list.Model` is the params-filtered view), `zone browserZone`,
`selected *hf.SearchResult`, `quants []hf.QuantOption`, `cached
map[string]bool`, `mmproj bool`, `quantsLoaded/quantsLoading bool`,
`quantIdx int`, `searchGen/quantGen` counters + `quantCancel
context.CancelFunc` (the pane's fetch is cancelled on navigation; the
search keeps the `shield` popup), `handoff *huh.Form` + `handoffVal`,
`tagFilter *huh.Form` + `tagFilterVal` (kinds `lang`/`lic`/`task`),
`paramsForm *huh.Form` + `paramsMinVal/paramsMaxVal`, `flash string`.
`SetSize` propagates to the textinput/list/dialogs like `StorageMode`'s.
Stale-result discipline (the item-6 gotcha): a done msg whose gen
mismatches the current request is dropped — a stale search may never
overwrite a newer query's results, and a stale quant fetch (user
selected another repo meanwhile) may never fill the pane. The search
shield's esc also bumps the gen, so a done msg landing just after a
cancel is dropped (cancel-then-complete race).

**Determinism and tests (P9).**

- `hf/search_test.go` against `httptest.Server` (the §16.2 style): URL
  path + query assembly (`filter=gguf` always; `search` absent when
  empty; **filter-tag assembly** — `Filter: []string{"ja"}` →
  `filter=gguf,ja`, `["ja","license:apache-2.0"]` →
  `filter=gguf,ja,license:apache-2.0`, order preserved, escaping;
  limit/sort/direction; query escaping incl. `+`/spaces);
  Bearer header with and without `HF_TOKEN`; response parse (id,
  downloads, likes, tags, pipeline_tag; absent fields → zero values);
  `full=true` not requested; 404/401/transport → typed kinds; empty
  list; malformed JSON → error.
- Browser flow tests with a **stub runner** (the stubSpawner pattern,
  `SetBrowserRunner`): type query → enter → results render and the
  **panes auto-follow the first hit** (no enter on a result);
  navigating with ↓ auto-updates the right column (metadata instantly,
  quants + card async, superseded fetches cancelled/gen-dropped) with
  `(cached)` markers (fake hub cache trees via `storage.Lookup`, the
  scan_test fixture style) + mmproj note + the `● cached` badge; tab →
  quants → enter on a quant → hand-off dialog → **add to config**
  yields `browserConfigHandoffMsg{org/repo:QUANT}` and Root's arm opens
  ConfigMode with the new-model form pre-filled source=hf,
  hf=`org/repo:QUANT` (assert the form's staging, and that no
  `hfCheckRequestedMsg` fires — pre-filled, not typed); **download
  now** yields `browserDownloadHandoffMsg` and Root's arm opens the
  Storage manager with a running download row for the split repo/quant
  (stub engine); esc paths at every zone and the dialog; the search
  shield renders static text and its esc bumps the gen (cancel-then-
  complete race); gen-mismatch drop (stale search, quant, and card
  msgs); the no-quant bare hand-off row; the sort cycle (s key,
  trending default) re-runs the search; **card panel** — README text
  rendered frontmatter-trimmed, 404 → `no model card`, pgup/pgdown
  scroll with ▴/▾ indicators; **quants window** — the panel windows
  its rows with a `▾ more` marker and the window follows the cursor;
  **width-fit regression** — no rendered line may exceed the terminal
  width (the lipgloss Width-wrap bug); **per-rune typing regression**
  (keystrokes arrive one rune at a time — "mistral" must type, s/l/L
  must not hijack the input); **`l`/`L` tag filters** — the picker
  opens, picking `ja` re-runs the search with `Filter: ["ja"]`,
  picking a license appends `license:<id>`, both combine in one
  request, `all languages`/`any license` clear, esc dismisses without
  changing, header shows the active filters; **metadata pane** — stub
  results with `base_model:quantized:<id>` render the
  `quantized from` line, `license:cc-by-nc-4.0` renders the
  non-commercial warning, language codes render in the pane and the
  result row; runner nil → search flash, no crash.
- Root dispatch: `b` opens ViewBrowser from both Main states (idle +
  reattach); shortcut-row and help text updated; size propagation and
  `forward`/`View` wiring (the §16.4 checklist).
- Snapshot tests render the browser in-process with the stub runner:
  empty search, results + metadata pane, quant pane with a cached
  marker, hand-off dialog — deterministic (no network, no timing).

**File map.** New `internal/hf/search.go` + `search_test.go`.
New `internal/tui/browser.go` + `browser_test.go` (BrowserMode, the zone
state, search/quant-fetch cmds + gen counters, shield, tag-filter
overlays (`l`/`L`) + the curated constants, hand-off dialog,
the two hand-off messages, runner interface).
`internal/tui/hfcheck.go` — extract `quantRowLabel` (chooser + browser
share it). `internal/tui/root.go` — `ViewBrowser`, `b` key + dispatch,
`openBrowser` + lazy `browserRunner`, size/forward/View wiring, the two
hand-off msg arms (`browserConfigHandoffMsg` → openConfig prefill;
`browserDownloadHandoffMsg` → openStorage + startDownload +
focusDownloadRow). `internal/tui/config.go` — `prefillHF` field + the
`openNewModelForm` seeding branch; `configEntry` gains `prefillHF`.
`internal/tui/main.go` — `b browse` shortcut + help line. `DESIGN.md`
§16.7 + §7.5 mode list + §14.2 browser bullet (same change, P5). No
changes to the config schema, `internal/storage`, or ROADMAP.
