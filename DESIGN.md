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
| `llamaman` (no positional args) | yes | TUI run mode (reattach) |
| `llamaman <alias>` | no, alias exists | TUI run mode (start fresh, default preset or only preset) |
| `llamaman <alias> <preset>` | no, both exist | TUI run mode (start fresh, named preset) |
| `llamaman <alias>` or `llamaman <alias> <preset>` | yes | TUI run mode (reattach, **arguments ignored**) |
| `llamaman <alias>` | no, alias missing | stderr error, exit 2 |
| `llamaman <alias> <preset>` | no, alias exists, preset missing | stderr error, exit 2 |

If two `llamaman` instances race to start a session, the loser sees `Another llamaman is already running` on stderr and exits 0.

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

When at least one model is configured, a bordered single-line-per-row selection list is embedded directly in the landing screen between the version line and the shortcuts. The first model is pre-selected; the row is reverse-video. Each row shows the alias, an optional `(running)` marker, and a subtle preset-count summary.

When **no** models are configured, the list is hidden and the screen reverts to its bare wordmark + shortcuts form so first-run users aren't confronted with an empty box.

| Key | Action |
|---|---|
| `↑` / `↓` | Move selection in the inline list (only when models exist) |
| `Enter` | Run the selected model. If the model has 0 or 1 presets it spawns directly; with 2+ presets the box pivots to a preset sub-list with the same Enter/Esc semantics |
| `Esc` | Back out of the preset sub-list to the model list |
| `c` | Configuration mode |
| `?` | Help overlay |
| `q` | Quit |
| `a` | Attach to running session (only shown when a session is running) |

If a session is running, an additional line appears above the list: `▶ Detached: <alias>/<preset> listening on :<port> — press a to attach`.

Order: rows follow the configuration order (`models[]` in the JSON). No alphabetical sort — users who reorder via Shift+↑/↓ in configuration mode see the change reflected here.

A model alias with **zero** presets selected via Enter → run mode using only auto-added flags (`-m`, `--alias`, `--host`, `--port`).

There is no separate "selection mode" — model selection is the main mode.

### 7.4 Run mode

```
╭───────────────────────────────────────────────────────────────────────────────╮
│ ▜  ▜                                                                          │
│ ▐  ▐  ▝▀▖ ▛▚▀▖ ...   Alias: alpha   Server: 8994   Context Size: 8192         │
│ ▐  ▐  ▞▀▌ ▌▐ ▌ ...   Preset: fast   Uptime: 00:01:30   [READY]                │
│  ▘  ▘ ▝▀▘ ▘▝ ▘ ...                                                            │
╰───────────────────────────────────────────────────────────────────────────────╯
╭── llama-server ───────────────────────╮╭── Hardware ──────────────────────────╮
│ Tokens/s:  80.0 /  60.0 avg  Prompt…  ││ [0] AMD Ryzen 9 7950X                │
│ Busy: 2/4 slots              Queued:1 ││     Util  23%  RAM  65%  120W  68°C  │
│                                       ││ [0] NVIDIA GeForce RTX 4090          │
│                                       ││     Util  89% VRAM  78%  320W  72°C  │
╰───────────────────────────────────────╯╰──────────────────────────────────────╯
╭─ output (tailing) ────────────────────────────────────────────────────────────╮
│ main: HTTP server is listening, hostname: 127.0.0.1, port: 9080               │
│ …                                                                             │
╰─ q: quit  k: kill  r: restart  c: copy  i: info  /: search  ?: help ──────────╯
```

The header is a two-section block: a **top strip** carrying the
identity cells (alias, server version, ctx size, preset, uptime,
status badge) and a **live band** of two side-by-side panels
showing real-time data from the running server. Both sections are
fixed-shape — there is no graceful stacking; we *peel off*
sections at width breakpoints so columns never shift mid-render.

**Layout state machine** (driven by terminal width):

| Width | Top strip | Live band |
|---|---|---|
| **≥ 110 cols** (State 1) | wordmark + 3 identity cells × 2 rows | both panels visible |
| **90 – 110** (State 2) | wordmark + 2 identity cells × 3 rows | hidden |
| **< 90** (State 3) | identity only (3 cells × 2 rows, no wordmark) | hidden |

**Wordmark**: the smblock-letterspaced llamaman wordmark
(31 cols × 4 rows) embedded from `internal/tui/wordmark.txt`.

**Identity cells**: `Alias`, `Server` (parsed `llama-server --version`),
`Context Size` (`/props.n_ctx` if available, else preset value, else
`n/a`), `Preset`, `Uptime`, status badge. The badge is bracketed,
bold, themed-foreground only — `[STARTING]` / `[READY]` / `[EXITED]`
/ `[ERROR]` — no background fill, so it works in both dark and light
themes.

**Live band — `llama-server` panel**: tokens/s and prompt-eval rates
shown as `now / avg avg`. The `now` half is sampled across two
`/metrics` ticks (Δtokens / Δseconds); the `avg` half is the
lifetime gauge llama-server already maintains. `Busy` reads
`busy/total` from `/slots`; `Queued` reads
`llamacpp:requests_deferred` from `/metrics`. All numeric values
land in fixed-width slots so column positions stay stable as values
transition. When `--metrics` is off (preset doesn't set
`metrics: true`), tokens/s and queued show `n/a`; busy still works.
When the last tick saw zero token delta the `now` half shows `—`
while `avg` keeps its lifetime value.

**Live band — `Hardware` panel**: CPU socket(s) first (deduped by
gopsutil `PhysicalID`), then NVIDIA GPUs via NVML. Two rows per
device — header `[N] <name>` then a value row of `Util` / `RAM|VRAM`
/ power / temp / fan. Per-class indexing (`[0]`, `[1]`); names
disambiguate. Memory label is `RAM` for CPU and `VRAM` for GPU.
Missing optional fields render `n/a` in fixed-width slots so column
shape is stable. The binary works on non-NVIDIA hosts: NVML init
failure yields zero GPU rows but the panel keeps rendering CPU.

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
| `q` / `Ctrl+C` | Quit prompt: `(k)ill / (d)etach / (c)ancel`. `(k)ill` returns to the main screen; `(d)etach` exits llamaman and leaves llama-server running. |
| `k` | Direct kill shortcut (with `(y)es / (n)o` confirm). On confirm: stops llama-server, removes the log + session record, and returns to the main screen — llamaman itself stays open. |
| `r` | Restart server (confirm if currently ready) |
| `c` | Copy full launch command to clipboard (`wl-copy`, fallback `xclip`, fallback flash status) |
| `i` | Show model & preset detail overlay (alias + Source/HF + preset name + every preset param in source order). Any key closes. |
| `/` | Search forward in output. Live highlights (reverse video + bold) wrap matches as you type; `Enter` applies, `Esc` cancels. |
| `n` / `N` | Next / previous search match |
| `Esc` | Clear active search and remove highlights (no-op when nothing is applied) |
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
└── Tab: pane ─ e: edit ─ D: dup ─ d: del ─ s: save ─ Esc: back ─────────────────┘
```

`Tab` / `Shift+Tab` cycle focus across panes. `Right` / `Left` (and `l` / `h`) do the same — the user can navigate to any pane with arrow keys without lifting from the navigation cluster.

**Models pane**:
- `e` rename alias / change source (modal form: alias, source select [`local` | `huggingface`], then either a path input or a `org/repo[:quant]` input depending on the selection).
- `n` new model (same modal as edit).
- `D` duplicate, prompt for new alias (presets and params copied; source kind and value preserved).
- `d` delete (confirm with preset count).
- `Shift+↑/↓` reorder (persisted in JSON).

**Presets pane**:
- `e` rename preset / edit description.
- `n` new preset (name + description; starts with empty params).
- `D` duplicate, prompt for new name.
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

- `lipgloss.HasDarkBackground()` chooses palette (light / dark).
- Two built-in palettes, no user customization currently.
- Accent: soft orange (`#E8A33D`-ish).
- Status indicators: green ready, yellow starting, red error, gray exited.
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
- Auto-restart on crash.
- Browser-open shortcut from run mode.
- Config import / export / sharing.
- Telemetry of any kind.
- Themes beyond auto light/dark.
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
