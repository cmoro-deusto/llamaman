# llamaman

[![CI](https://github.com/cmoro-deusto/llamaman/actions/workflows/ci.yml/badge.svg)](https://github.com/cmoro-deusto/llamaman/actions/workflows/ci.yml)
[![Latest release](https://img.shields.io/github/v/release/cmoro-deusto/llamaman?display_name=tag&sort=semver)](https://github.com/cmoro-deusto/llamaman/releases/latest)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go version](https://img.shields.io/github/go-mod/go-version/cmoro-deusto/llamaman)](go.mod)
[![AUR](https://img.shields.io/aur/version/llamaman-bin)](https://aur.archlinux.org/packages/llamaman-bin)

A modern terminal manager for [`llama-server`](https://github.com/ggml-org/llama.cpp) (the HTTP server bundled with llama.cpp). Define your models and launch presets once in a JSON config, pick them from a Bubble Tea TUI, follow the log live, detach when you're done, and reattach later from another shell — without ever memorising a 60-flag command line.

## Why llamaman?

If you've already chosen llama.cpp, you've felt the friction: every model wants different flags, every backend tweak (CUDA layers, context size, flash attention, KV-cache quant) is a new shell-history entry, and you eventually keep a scratchpad of "the command that worked last time". llamaman is the launcher you wish `llama-server` shipped with — your models and presets become first-class config, the TUI knows what flags `llama-server --help` exposes today, and a running server stays running across terminal sessions instead of dying with the shell that started it.

## Highlights

- Models and named launch presets live in one versioned JSON file.
- Detach and reattach across `llamaman` invocations — the server keeps serving.
- Log tailing with live forward search, mouse-wheel scrollback, and `n`/`N` / `g`/`G` navigation.
- Type-aware parameter editor whose autocomplete and validation come straight from `llama-server --help`.
- Local `.gguf` files or Hugging Face identifiers (`-m` vs `-hf`) on a per-model basis.
- Shell completions for `bash`, `zsh`, and `fish`.
- XDG-compliant config / state / cache / log paths.
- Single static Go binary, no runtime dependencies beyond `llama-server` itself.

### Configuration

A single JSON file at `${XDG_CONFIG_HOME:-~/.config}/llamaman/config.json` is the source of truth. Each model carries an alias, a source (a local `.gguf` path or a Hugging Face identifier), and any number of named presets. Presets are ordered objects whose key order is preserved end-to-end — the order you write your params in is the order they show up on the `llama-server` command line.

### Process model

llamaman spawns `llama-server` in its own session (`setsid(2)`), so the child outlives `llamaman` itself when you detach. A `flock(2)`-protected `session.json` records the live PID, alias, preset, and full argv, so a second `llamaman` invocation can reattach by reading state instead of guessing. The log file is followed via `fsnotify` rather than polled, so the run-mode viewport stays current without burning CPU.

### TUI experience

Three modes — main (centred launcher with embedded model list), run (status header + log viewport with search), and configuration (three-pane master/detail editor with type-aware param picker). Modal dialogs overlay the existing screen rather than blanking it, so you never lose context. The figlet-style wordmark adapts to terminal width; status indicators use colour but respect `NO_COLOR`.

### Sources & translation

A model is **either** local (`location: "~/models/foo.gguf"`) **or** Hugging Face (`hf: "Qwen/Qwen3-32B-GGUF:Q4_K_M"`) — never both. llamaman emits the matching `-m` or `-hf` flag automatically, plus `--alias`, `--host`, and `--port`. A preset can override any of those by setting the same key, which is the universal escape hatch when you need something unusual. Short-vs-long flag form is decided by parsing `llama-server --help` and caching the result by the binary's mtime.

### Quality of life

Copy the launch command to your clipboard with `c` (Wayland's `wl-copy` first, X11's `xclip` as fallback). Restart the running server in place with `r`. List models or a model's presets non-interactively (`-l`, `-p`) for scripting. First-run setup autodetects `llama-server` from `which`, then `/usr/local`, then `/opt/llama.cpp`. Unknown flag warnings (a key your llama-server build doesn't expose) are non-blocking — surface in the run-mode header and config-mode pane, never crash the launch.

<details>
<summary>Table of contents</summary>

- [Requirements](#requirements)
- [Install](#install)
  - [Prerequisites](#prerequisites)
  - [Pre-built binary (GitHub Releases)](#pre-built-binary-github-releases)
  - [Arch / CachyOS (AUR)](#arch--cachyos-aur)
  - [From source](#from-source)
  - [Shell completions](#shell-completions)
  - [Updating](#updating)
  - [Uninstall](#uninstall)
- [Quick start](#quick-start)
- [Walkthrough](#walkthrough)
  - [Your first model](#your-first-model)
  - [Adding a Hugging Face model](#adding-a-hugging-face-model)
  - [Tuning a preset](#tuning-a-preset)
  - [Detach and reattach](#detach-and-reattach)
- [TUI modes](#tui-modes)
  - [Main mode](#main-mode)
  - [Run mode](#run-mode)
  - [Configuration mode](#configuration-mode)
- [How it works](#how-it-works)
- [Configuration schema](#configuration-schema)
- [Parameter translation](#parameter-translation)
- [Files](#files)
- [Compatibility](#compatibility)
- [Keybindings cheat sheet](#keybindings-cheat-sheet)
- [Known limitations](#known-limitations)
- [Troubleshooting](#troubleshooting)
- [FAQ](#faq)
- [Acknowledgments](#acknowledgments)
- [Disclaimer](#disclaimer)
- [License](#license)

</details>

## Requirements

- **OS**: Linux (x86_64 or arm64). macOS and Windows are not currently supported — see the [FAQ](#faq).
- **Terminal**: any modern terminal emulator with UTF-8 and 24-bit colour. The TUI degrades gracefully below that, but boxes and accents look best with truecolor. `NO_COLOR` is honoured.
- **Width**: 80 columns minimum; 110+ enables the figlet wordmark in the run-mode header. Narrower terminals collapse to a compact identity line.
- **Locale**: a UTF-8 locale (`LANG=*.UTF-8`).
- **Clipboard (optional)**: `wl-clipboard` (Wayland) or `xclip` (X11) for the `c` "copy launch command" shortcut. Without either, the shortcut becomes a no-op with a brief status flash.
- **`llama-server`**: a working build of llama.cpp's HTTP server on `PATH` or under one of the standard prefixes (`/usr/local/bin`, `/usr/local/llama.cpp/bin`, `/opt/llama.cpp/bin`). See [Compatibility](#compatibility).

## Install

### Prerequisites

llamaman is a launcher, not a runtime — you also need `llama-server` itself. Build llama.cpp from source ([upstream instructions](https://github.com/ggml-org/llama.cpp#building)) or install a packaged build for your distro. Once the binary is reachable, llamaman's first-run flow autodetects it.

### Pre-built binary (GitHub Releases)

Versioned tarballs are published for `linux/amd64` and `linux/arm64`, alongside a `checksums.txt` for verification.

```sh
# Resolve latest release tag and architecture
TAG=$(curl -fsSL https://api.github.com/repos/cmoro-deusto/llamaman/releases/latest \
  | grep -oP '"tag_name":\s*"\K[^"]+')
VERSION=${TAG#v}
ARCH=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
ASSET="llamaman_${VERSION}_linux_${ARCH}.tar.gz"
BASE="https://github.com/cmoro-deusto/llamaman/releases/download/${TAG}"

# Download tarball + checksums
curl -fsSL -o "/tmp/${ASSET}"          "${BASE}/${ASSET}"
curl -fsSL -o /tmp/llamaman-checksums "${BASE}/checksums.txt"

# Verify
( cd /tmp && sha256sum -c --ignore-missing llamaman-checksums )

# Install
tar -xzf "/tmp/${ASSET}" -C /tmp llamaman
sudo install -m 0755 /tmp/llamaman /usr/local/bin/llamaman
```

### Arch / CachyOS (AUR)

```sh
yay -S llamaman-bin
# or: paru -S llamaman-bin
```

The PKGBUILD is generated by goreleaser as part of the release pipeline; no hand-maintained recipe lives in this repo.

### From source

Requires a recent Go toolchain (the module pins `go 1.26`).

```sh
go install github.com/cmoro-deusto/llamaman@latest
```

The resulting binary lacks the version metadata embedded by goreleaser; `llamaman --version` will say `dev`. Tag a build manually if you need the metadata:

```sh
git clone https://github.com/cmoro-deusto/llamaman.git
cd llamaman
go build \
  -ldflags "-X main.version=$(git describe --tags) -X main.commit=$(git rev-parse --short HEAD) -X main.date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -o ./bin/llamaman .
```

### Shell completions

```sh
llamaman --completion bash > ~/.local/share/bash-completion/completions/llamaman
llamaman --completion zsh  > ~/.zsh/completions/_llamaman
llamaman --completion fish > ~/.config/fish/completions/llamaman.fish
```

Reload your shell (or `source` the new file) to activate.

### Updating

- **Pre-built binary**: re-run the install snippet above; the resolved `TAG` will pick up the new release.
- **AUR**: `yay -Syu llamaman-bin` (or your AUR helper's equivalent).
- **`go install`**: `go install github.com/cmoro-deusto/llamaman@latest`.

llamaman's config schema is at `version: 1` and evolves additively within that version: new optional fields land in place, so a newer binary reads an older config without ceremony. Older binaries reject newer fields with `unknown field "..."` (the loader uses `DisallowUnknownFields`). No migration is needed today because only one schema version exists. When a future release introduces `version: 2` or higher, llamaman will run automatic, in-place migration at config-load time (with a `.bak` of the prior file kept alongside the upgraded one) — see [Known limitations](#known-limitations).

### Uninstall

```sh
# Binary
sudo rm /usr/local/bin/llamaman          # tarball install
yay -R llamaman-bin                      # AUR
rm "$(go env GOPATH)/bin/llamaman"       # go install

# Shell completions (paths from the install step)
rm -f ~/.local/share/bash-completion/completions/llamaman \
      ~/.zsh/completions/_llamaman \
      ~/.config/fish/completions/llamaman.fish

# Config and runtime state (everything llamaman wrote)
rm -rf "${XDG_CONFIG_HOME:-$HOME/.config}/llamaman" \
       "${XDG_CACHE_HOME:-$HOME/.cache}/llamaman" \
       "${XDG_STATE_HOME:-$HOME/.local/state}/llamaman" \
       "${XDG_RUNTIME_DIR:-/tmp/llamaman-$UID}/llamaman"
```

## Quick start

```sh
llamaman                # launch TUI
llamaman <alias>        # launch a model with its default preset
llamaman <alias> <pre>  # launch with a named preset
llamaman -l             # list configured models
llamaman -p <alias>     # list presets for a model
```

The first invocation with no config triggers a setup flow that writes `${XDG_CONFIG_HOME:-~/.config}/llamaman/config.json` with autodetected defaults.

## Walkthrough

### Your first model

The first time you run `llamaman` with no existing config, you're walked through setup.

1. **Welcome modal**. `Enter` to begin, `q` to quit (clean exit, nothing written).
2. **Globals form**. Three fields, prefilled from autodetection:
   - `llama-server binary` — `which llama-server`, then standard prefixes, then empty.
   - `host` — `127.0.0.1`.
   - `port` — `9080`.
   Save the form to write the initial `config.json` (`models: []`, valid as-is).
3. **Configuration mode** opens with a one-time banner: *"First-time setup — globals saved. Press n in the Models pane to add your first model."*
4. **Add a model** — `n` in the Models pane. Choose source (`local` for a `.gguf` path, or `huggingface` for a `org/repo[:quant]` identifier) and supply an alias.
5. **Add a preset** — `Tab` to the Presets pane, `n`. Give it a name (e.g. `default`) and an optional description. Empty params are valid: llamaman will still emit `-m`/`-hf`, `--alias`, `--host`, `--port`.
6. **Add params** — `Tab` to the param editor pane, `n`. The picker lists every flag your `llama-server --help` exposes; start typing to filter. Pick a flag, supply a value (the input is type-aware: bool toggles, numerics, enums, free-form strings).
7. **Save** with `s`, exit configuration with `Esc`. Back in main mode, your model is the first row of the inline list. `Enter` to launch.

You'll land in run mode with a status header (`starting → ready` once `llama-server` prints `server is listening`) and a tailing log viewport. From here, the next sub-scenarios apply.

### Adding a Hugging Face model

A Hugging Face model is identified by `org/repo[:quant]`. llama.cpp downloads and caches it on first launch; subsequent launches reuse the cache.

1. From main mode, press `c` (configuration) → in the Models pane, `n`.
2. Set source to `huggingface`, alias to something like `qwen3-32b`, and identifier to `Qwen/Qwen3-32B-GGUF:Q4_K_M`.
3. Add at least one preset (`default` is fine) and any params you want (e.g. `ngl: 99`, `fa: "on"`).
4. Save (`s`), `Esc` back to main, `Enter` on the new alias.

llamaman emits `-hf <identifier>` instead of `-m <path>`. The first launch will be slower while llama.cpp downloads weights; the run-mode log shows download progress.

### Tuning a preset

The param editor surfaces every flag `llama-server` advertises in its `--help`. To tune an existing preset:

1. Main mode → `c` → Models pane: select the model.
2. Presets pane: select the preset, `e` to edit name/description, or `Tab` to drop into the param editor.
3. **Edit** an existing param: highlight the row, `Enter` (or `e`). Numeric / bool / enum / string editors open inline.
4. **Add** a param: `n` opens the picker. Type any substring of the flag name to filter — descriptions appear next to each entry. `Enter` to pick, then supply the value with the type-appropriate input.
5. **Reorder** params with `Shift+↑`/`Shift+↓` — the order on disk is the order on the command line.
6. **Remove** a param with `d`.

Save with `s`. Restart a running session with `r` in run mode to apply the new params (kill + restart with the new argv).

### Detach and reattach

The flagship trick: leave the server running while you close the terminal.

1. From run mode, press `q` (or `Ctrl+C`). The quit prompt offers `(k)ill / (d)etach / (c)ancel`.
2. Choose `(d)etach`. llamaman exits; `llama-server` keeps running in its own session, still bound to its host:port, still writing to its log file.
3. Open a new terminal. Run `llamaman` (no arguments). It detects the live `session.json`, skips main mode, and drops you back into run mode against the same `llama-server` — log buffered from disk, search and navigation work as before.
4. Two `llamaman` instances can reattach simultaneously (read-only on session state, both tail the same log).
5. From any reattached run-mode session, `k` (with confirm) or `q → (k)ill` cleanly stops `llama-server`, removes the log and `session.json`, and returns to main mode.

If `llama-server` crashes while detached, the next `llamaman` invocation finds the stale `session.json` (PID dead), silently cleans it up, and you're back at main mode.

## TUI modes

### Main mode

A centred window: the figlet "llamaman" wordmark, version line, and an inline single-row-per-model selection list (when at least one model is configured). Pre-selected: the first model. Each row shows the alias, an optional `(running)` marker, and a subtle preset count.

> The information density and layout of this top window are an early cut. A future release will iterate on how model identity, source, presets, and run state are surfaced — see [Known limitations](#known-limitations).

If a session is running, an extra line above the list reads `▶ Detached: <alias>/<preset> listening on :<port> — press a to attach`.

If **no** models are configured (e.g. immediately after first-run setup), the list is hidden so users aren't confronted with an empty box.

| Key | Action |
|---|---|
| `↑` / `↓` | Move selection in the inline list (only when models exist) |
| `Enter` | Run the selected model. With 2+ presets, the box pivots to a preset sub-list (Enter to confirm, Esc to back out) |
| `Esc` | Back out of the preset sub-list to the model list |
| `c` | Configuration mode |
| `a` | Attach to running session (only shown when a session is running) |
| `?` | Help overlay |
| `q` | Quit |

Row order follows the configuration file's `models[]` order; reorder via `Shift+↑`/`Shift+↓` in configuration mode. There is no separate "selection mode" — model selection lives in main mode.

### Run mode

```
┌─ <alias> / <preset> ──────── <host>:<port>  <status>  uptime hh:mm:ss ──┐
│ <condensed param summary: model basename, ngl, ctx, fa, ctk/ctv>        │
│ <boolean flags>    [warning: unknown flag "..."]                        │
└─────────────────────────────────────────────────────────────────────────┘
┌─ output (tailing) ─── /: search  G: end  g: top ────────────────────────┐
│ ...                                                                     │
│ main: HTTP server is listening, hostname: 127.0.0.1, port: 9080         │
│ ▼                                                                       │
└─ q: quit  k: kill  r: restart  c: copy  /: search  ↑/↓: scroll ─────────┘
```

The top box shows the wordmark on terminals ≥110 cols, plus alias / `llama-server` version / context size / uptime / `[Metrics]` on row 1, and preset / temp / top_p / top_k / min_p on row 2. `[Metrics]` lights up black-on-green when the active preset has `metrics: true`. Param keys are looked up canonically — a preset using the short form `c` for `ctx-size` still surfaces the value. The log area below sits in its own bordered frame and fills the remaining screen.

Status state machine: `starting → ready → exited|error`. `ready` is detected by matching `server is listening` in stdout. Auto-scroll is locked to the bottom unless you've scrolled up; when scrolled up, a `↓ N new lines` indicator appears, and `G` returns to live tail.

| Key | Action |
|---|---|
| `q` / `Ctrl+C` | Quit prompt: `(k)ill` returns to main, `(d)etach` exits llamaman leaving `llama-server` running, `(c)ancel` stays put |
| `k` | Direct kill (with `(y)es / (n)o` confirm). Stops the server, removes log + session, returns to main; llamaman stays open |
| `r` | Restart the server (confirm if status is `ready`) |
| `c` | Copy the full launch command to clipboard (`wl-copy` → `xclip` fallback) |
| `/` | Search forward — matches highlighted live (reverse video) as you type, persist after `Enter` for `n`/`N` |
| `Esc` | Clear the active search and remove highlights |
| `n` / `N` | Next / previous match |
| `g` / `G` | Jump to top / bottom |
| `Space` / `b` | Page down / up |
| `↑` / `↓` / wheel | Scroll one line — `j`/`k` are deliberately not bound here so `k` is free for kill |
| `?` | Help overlay |

ANSI colour codes from `llama-server` are passed through. Scrollback is unlimited and sourced from the on-disk log file.

### Configuration mode

A three-pane master/detail editor:

```
┌─ Models ──────────┬─ Presets: <selected model> ──┬─ <selected preset> ─────────┐
│ ▶ qwen3-32b       │ ▶ default                    │ name:        default        │
│   llama-3.3-70b   │   smallctx                   │ description: balanced       │
│ ─────────────     │ ─────────────                │                             │
│   [+ new model]   │   [+ new preset]             │ Params:                     │
│                   │                              │   ngl              99       │
│ [g] globals       │                              │   ctx-size         262144   │
│                   │                              │   ...                       │
│                   │                              │   [+ add param]             │
└── Tab: pane ─ e: edit ─ D: dup ─ d: del ─ s: save ─ Esc: back ─────────────────┘
```

| Key | Action |
|---|---|
| `Tab` / `Shift+Tab` | Cycle focus across panes |
| `←` / `→` (or `h` / `l`) | Same as Tab/Shift+Tab |
| `↑` / `↓` | Navigate within the focused pane |
| `Shift+↑` / `Shift+↓` | Reorder the focused row (models, presets, or params) |
| `e` | Edit the highlighted item |
| `n` | New item (model / preset / param, depending on focused pane) |
| `D` | Duplicate (models and presets) |
| `d` | Delete (with confirm where lossy) |
| `g` | Open the Globals form (binary path, host, port) |
| `s` | Save (atomic, with `.bak`) |
| `Esc` | Back. With unsaved changes, prompts Save / Discard / Cancel |
| `?` | Help overlay |

The new-param picker shows every flag's bare name + parsed help description. Just start typing to filter — there's no `/`-then-prompt step. The value editor is type-aware: booleans are yes/no toggles, numerics use a numeric input, parsed enums (e.g. `ctk`/`ctv`'s known set, or any `[a|b|c]` placeholder in `--help`) are pickers, everything else is a text input.

Form behaviour: forms are `huh` instances mounted via the Bubble Tea message loop. Modal dialogs (kill confirm, restart confirm, save/discard prompt, help) overlay the existing screen rather than blanking it.

## How it works

Three layers, strictly bottom-up imports:

- **Process supervision (`internal/server/`)**. `Spawn` builds a `*exec.Cmd` with `Setsid: true` so the child outlives `llamaman` if the user detaches. stdout and stderr are redirected to a single log file, opened with `O_TRUNC` for fresh sessions. `Adopt` reattaches by reading `session.json`, polling the live PID, and tailing the same log file read-only. `Tailer` follows the log via `fsnotify` (no polling). A `PortAvailable` precheck runs before spawn to surface bind failures early.
- **Session state (`internal/server/SessionManager`)**. A `flock(2)` on `session.json` serialises session-start across competing `llamaman` instances; the loser sees `Another llamaman is already running` and exits 0. Stale records (PID dead) are cleaned silently. Multiple readers (reattaching instances) share access without locking.
- **Param translation (`internal/translate/`)**. `Build(globals, model, preset, registry) → (Result, error)` produces the argv. Auto-added flags are emitted in fixed order: `<bin> {-m <loc> | -hf <id>} --alias --host <preset.params...> --port`. Preset params that share a key with an auto-added flag (`m`, `hf`, `alias`, `host`, `port`) shadow the auto entry — the universal escape hatch. Short-vs-long form is decided by `internal/flags/`, which parses `llama-server --help` once per binary mtime and caches the result at `${XDG_CACHE_HOME}/llamaman/flags-<mtime>.json`. Unknown keys (not in the parsed registry) yield non-blocking warnings, surfaced in the run-mode header and config-mode pane.

For the full architecture — every type, every state machine, every edge case — see [`DESIGN.md`](DESIGN.md). It's the canonical reference and is kept in sync with the implementation.

## Configuration schema

The config file is JSON at `${XDG_CONFIG_HOME:-~/.config}/llamaman/config.json` (override with `-c <path>`). Schema:

```jsonc
{
  "version":  1,                                  // integer, REQUIRED, must be 1 (unknown → exit 2)

  "globals": {
    "llama-server-bin": "<absolute path>",        // REQUIRED, ~ and $VAR expanded at load; warn-only if missing/non-exec
    "ip_address":       "<IPv4 | IPv6 | host>",   // REQUIRED, format-validated
    "port":             <integer 1..65535>        // REQUIRED
  },

  "models": [
    {
      "alias":    "<string>",                     // REQUIRED, unique across models, case-sensitive
      "location": "<absolute path to .gguf>",     // ONE-OF (location | hf), ~ and $VAR expanded
      "hf":       "<org/repo[:quant]>",           // ONE-OF (location | hf), regex ^[\w.-]+/[\w.-]+(?::[\w.-]+)?$

      "presets": [
        {
          "preset":      "<string>",              // REQUIRED, unique within this model
          "description": "<string>",              // optional, free-form
          "params": {                             // object; KEY ORDER PRESERVED on disk and in argv
            "<flag-name>": <bool | number | string>
          }
        }
      ]
    }
  ]
}
```

Field-by-field:

- **`version`**. Integer, must be `1` (the only version that has ever shipped). Unknown values are a hard error today (exit 2). Schema evolution within v1 is additive — new optional fields are added in place; reading a newer config with an older binary errors out (`json: unknown field`). When a future release introduces `version: 2` or higher, llamaman will migrate older configs automatically on load (keeping a `.bak`); the value of this field is what the migration step keys off.
- **`globals.llama-server-bin`**. Absolute path to the `llama-server` binary. Leading `~` is expanded to `$HOME`; `$VAR` and `${VAR}` are looked up in the environment (unset variables are left literal so typos surface). Validation warns (does not block) if the path doesn't exist or isn't executable.
- **`globals.ip_address`**. Listen address passed as `--host`. Accepts IPv4 (`127.0.0.1`), IPv6 (`::1`), bracketed IPv6, or hostnames. **Always** emitted explicitly — no implicit `localhost`.
- **`globals.port`**. TCP port, integer in `[1, 65535]`. A port-availability precheck runs before spawn; if the port is bound, the TUI surfaces a modal (`(g)oto globals / (p)reset port / (c)ancel`) and CLI launches exit `4`.
- **`models[].alias`**. The user-visible name used in `llamaman <alias>` and in the TUI lists. Case-sensitive; must be unique across the array. Also passed to `llama-server` as `--alias <alias>` so it shows up in completions metadata.
- **`models[].location` / `models[].hf`**. **Exactly one** must be set. `location` is a path to a local `.gguf` file (expanded as above); `hf` is a Hugging Face identifier in `org/repo[:quant]` form (passed verbatim — llama-server downloads and caches on first launch, no reachability check at config-load). Both empty or both filled is a validation error.
- **`models[].presets`**. Array; may be empty (`[]`). An alias with zero presets is launchable from main mode and runs with only the auto-added flags.
- **`presets[].preset`**. Preset name. Unique within a model, case-sensitive. The "default" preset (literal name `default`) is what `llamaman <alias>` picks when no preset is given on the CLI; if no `default` exists and the model has exactly one preset, that one is used.
- **`presets[].description`**. Free-form, displayed in the config-mode preset pane. No length cap; one line renders cleanly, multi-line is preserved.
- **`presets[].params`**. JSON object whose **key order is preserved** end-to-end (custom marshal/unmarshal under the hood). The order you write the keys in is the order they appear on the `llama-server` command line. Numeric values are stored as `json.Number` so `0.0` round-trips as `0.0`, not `0`. Object or array values are a config-load error. See [Parameter translation](#parameter-translation) for how each value type maps to argv.

## Parameter translation

llamaman does **not** maintain a hand-curated list of `llama-server` flags. Instead, on startup (and whenever the binary's mtime changes) it parses `llama-server --help` and caches a registry of `{name → canonical_form, kind, description, enum_values}`. The configuration mode's param picker, autocomplete, type-aware editors, and unknown-flag warnings all consume this registry. Add a new flag in upstream llama.cpp, rebuild, and llamaman picks it up automatically — no llamaman release needed.

The translation rules from `params` to argv:

### Auto-added flags

In every spawn command, in this order:

```
<bin> {-m <location> | -hf <id>} --alias <alias> --host <ip_address> <preset.params...> --port <port>
```

- The first slot is the model source: `-m <location>` for a local model, `-hf <id>` for a Hugging Face model. Chosen by which field is set on the model.
- `--host` is **always** passed, even when it's `127.0.0.1` (explicit beats implicit).
- If a preset's `params` contains a key that overlaps with one of the auto-added flags (`m`, `hf`, `alias`, `host`, `port`), the preset value wins and the corresponding auto entry is suppressed. This is the universal escape hatch — a preset can redirect a model to a different file, or override `--host` for one-off testing, without touching globals.

### Short vs long form

Each flag has a canonical form (single-dash for `-m`, `-c`, `-ngl`, `-hf`; double-dash for `--temp`, `--ctx-size`, etc.) discovered from `--help`. Whatever short-or-long form you write in your config, llamaman emits the canonical form. The cache lives at `${XDG_CACHE_HOME}/llamaman/flags-<mtime>.json` and rebuilds when the binary's mtime changes.

When the binary is missing or `--help` can't be parsed, llamaman falls back to a small hard-coded short-form set (`m, n, c, t, s, b, h, p, ngl, ctk, ctv, fa, np, cb, hf, hff, hft, hfr, hfd, hfv`) and double-dash for everything else. The `hf*` family is in the hard-coded set so HF-sourced models work even with no registry.

### Booleans

- `"key": true` → emit `--key` (no value).
- `"key": false` → emit nothing.

llama.cpp also has flags that take literal `on`/`off` strings (e.g. `--fa on` for flash attention). Those are JSON strings (`"fa": "on"`), not booleans — they go through the string passthrough below.

### Numbers and strings

- Number → `--key 42` (two argv entries, value preserved verbatim).
- String → `--key value` (two argv entries; the string is one argv element regardless of spaces, quotes, or shell metacharacters — llamaman `exec`s directly with no shell, so no quoting is needed or applied).
- Object or array → config-load error.

### Key order

Object key order is preserved end-to-end. The order you write `params` in your JSON is the order they appear on the command line — group related flags however you like.

### Validation

When a param key isn't in the parsed `--help` registry:

- A warning is logged to the debug log.
- The warning surfaces in the run-mode top pane (line 3) and in the configuration-mode right pane.
- Execution is **not** blocked.

This keeps llamaman forward-compatible with new llama-server flags without requiring a release.

## Files

| Purpose | Path |
|---|---|
| Config | `${XDG_CONFIG_HOME:-~/.config}/llamaman/config.json` |
| Live session record | `${XDG_RUNTIME_DIR:-/tmp/llamaman-$UID}/llamaman/session.json` |
| `llama-server` log (current session) | `${XDG_RUNTIME_DIR:-…}/llamaman/llama-server.log` |
| Flag-name cache | `${XDG_CACHE_HOME:-~/.cache}/llamaman/flags-<mtime>.json` |
| Debug log | `${XDG_STATE_HOME:-~/.local/state}/llamaman/llamaman.log` |
| Config backup (rolling) | `${XDG_CONFIG_HOME:-~/.config}/llamaman/config.json.bak` |

Set `LLAMAMAN_DEBUG=1` to raise the debug log level to `DEBUG`.

The session record and `llama-server` log live under `XDG_RUNTIME_DIR` and are wiped at reboot — that's intentional. The debug log rotates at 10 MB and keeps one prior file.

## Compatibility

llamaman targets a recent build of llama.cpp's `llama-server`. Because flags are discovered dynamically from `llama-server --help`, most upstream changes are absorbed automatically — a flag rename surfaces as an "unknown flag" warning in the next launch's run-mode header rather than a crash, and existing config keeps working until you update the param to its new name. Brand-new flags are usable in presets immediately, with no llamaman release required.

The minimum tested `llama-server` build is **`build 8994 (aab68217b)`**. Older builds will probably work for the common-case flags; very old builds (predating dynamic `--help` machine-readable structure) may fall back to llamaman's hard-coded short-form set.

## Keybindings cheat sheet

> Reference table consolidating every binding from the per-mode tables above. The per-mode sections remain the source of truth — when in doubt, check there.

| Mode | Key | Action |
|---|---|---|
| Main | `↑` / `↓` | Move selection in inline list |
| Main | `Enter` | Run selected model (pivot to preset sub-list if 2+ presets) |
| Main | `Esc` | Back out of preset sub-list |
| Main | `c` | Open configuration mode |
| Main | `a` | Attach to running session |
| Main | `?` | Help overlay |
| Main | `q` | Quit |
| Run | `q` / `Ctrl+C` | Quit prompt: `(k)ill / (d)etach / (c)ancel` |
| Run | `k` | Direct kill (with confirm), back to main |
| Run | `r` | Restart server (confirm if `ready`) |
| Run | `c` | Copy launch command to clipboard |
| Run | `/` | Search forward (live highlights) |
| Run | `Esc` | Clear search highlights |
| Run | `n` / `N` | Next / previous match |
| Run | `g` / `G` | Jump to top / bottom |
| Run | `Space` / `b` | Page down / up |
| Run | `↑` / `↓` / wheel | Scroll one line |
| Run | `?` | Help overlay |
| Config | `Tab` / `Shift+Tab` | Cycle panes (also `←` / `→`, `h` / `l`) |
| Config | `↑` / `↓` | Navigate within pane |
| Config | `Shift+↑` / `Shift+↓` | Reorder rows |
| Config | `e` | Edit highlighted item |
| Config | `n` | New item (model / preset / param) |
| Config | `D` | Duplicate (models, presets) |
| Config | `d` | Delete (with confirm) |
| Config | `g` | Open Globals form |
| Config | `s` | Save (atomic, with `.bak`) |
| Config | `Esc` | Back (Save / Discard / Cancel if unsaved) |
| Config | `?` | Help overlay |

## Known limitations

The current scope is deliberately tight. The following are not in the box today:

- **One running session at a time.** llamaman binds a single `host:port` per session; running two `llama-server` instances simultaneously isn't supported.
- **No auto-restart.** If `llama-server` exits or crashes, run mode shows the exit status and you press `r` (with confirm) to restart manually. There is no supervision daemon.
- **No headless / `--detach` flag.** Detach is a TUI action only — there's no way to spawn a session from the CLI without dropping into run mode first.
- **No live editing.** Configuration changes don't apply to a running server; restart with `r` to pick them up.
- **Sessions buffer fully in memory.** The run-mode log viewport reads the entire log file into memory and tails new writes. Acceptable for realistic session sizes; not designed for week-long runs producing gigabytes of logs.
- **No theme customisation.** Two built-in palettes (auto-selected by `lipgloss.HasDarkBackground()`); `NO_COLOR` is honoured.
- **No config schema migrations *yet*.** Schema version is `1` and evolves additively — new optional fields don't break old configs, so no migration is needed today. Automatic in-place migration (with a `.bak` of the prior file) is planned for whenever a future release introduces `version: 2` or higher.
- **Main mode's top window is a first cut.** The current centred window (wordmark + version line + inline model list) is functional but spartan. A future release will iterate on how model identity, source (local vs HF), preset counts, and detached-session state are surfaced; expect the layout to change.

## Troubleshooting

### `llama-server binary not found`

llamaman couldn't autodetect the binary, or the path in `globals.llama-server-bin` doesn't exist / isn't executable.

- Run `which llama-server`. If empty, install or build llama.cpp first.
- If the binary is in a non-standard location, open configuration mode (`llamaman` → `c` → `g`) and point `llama-server binary` at the absolute path.
- Permissions: `chmod +x` the binary if needed.

### `Port already in use`

Something is already bound to `globals.ip_address:globals.port`. Most often it's a previous `llama-server` you forgot about.

- Check: `ss -tlnp | grep :<port>` (Linux).
- If it's a stale `llama-server`: `pkill llama-server` (the next `llamaman` invocation will detect a stale `session.json` and clean up silently).
- Otherwise, change the port in Globals (`g` from configuration mode) or pass a one-off override via a preset that sets `"port": <other>`.

### TUI: `Another llamaman is already running`

Two `llamaman` processes raced to start a session and you lost. The other instance is now in run mode. Reattach instead: just run `llamaman` again — it'll detect the live session and drop you into run mode.

### Stale session: `(running)` shown in main mode but the server is dead

llamaman cleans these up automatically on next launch. If you're stuck, manually remove `${XDG_RUNTIME_DIR}/llamaman/session.json` and `llama-server.log`.

### `c` (copy launch command) does nothing

You're missing both `wl-copy` (Wayland) and `xclip` (X11). Install whichever fits your session:

- Wayland: `pacman -S wl-clipboard` / `apt install wl-clipboard` / etc.
- X11: `pacman -S xclip` / `apt install xclip` / etc.

The shortcut briefly flashes a status indicator when neither tool is found.

### Unknown flag warning in run mode

A param key in your preset isn't in `llama-server --help`. The launch still happens — `llama-server` will either accept the flag (if it's renamed) or reject it (if it's gone), in which case it'll exit and you'll see the error in the log viewport.

- Check upstream changelog for renames.
- Rename the param key in configuration mode.
- Force a fresh registry parse by `touch`-ing the binary or deleting `${XDG_CACHE_HOME}/llamaman/flags-*.json`.

### Config won't save (`could not acquire lock`)

Another `llamaman` instance has configuration mode open. Switch to it and save / exit, or kill the other process.

### Reading the debug log

```sh
LLAMAMAN_DEBUG=1 llamaman           # raise to DEBUG level
tail -f ~/.local/state/llamaman/llamaman.log
```

The debug log captures config load events, session lifecycle, `--help` parse outcomes, and errors. It does **not** capture `llama-server`'s output — that's the run-mode log file under `XDG_RUNTIME_DIR`.

## FAQ

### Why Linux only?

llamaman's implementation assumes Linux semantics throughout: XDG base directories, `setsid(2)` for child-process detachment, `fsnotify` inotify events on the log file, `flock(2)` for session serialisation, and Wayland/X11 clipboard tools. Porting to macOS is plausible (most of these have Darwin equivalents) and on the table if the project sees usage. There's no commitment beyond that. Windows is not currently planned.

### Why JSON and not TOML / YAML?

JSON has a key feature the others don't ergonomically expose: **stable, parseable key ordering**. llamaman preserves the order of keys in `params` end-to-end, so the order you write your flags in is the order they appear on the `llama-server` command line. JSON's spec-mandated object semantics + a small custom marshal/unmarshal layer makes this clean. TOML would have worked too; YAML's spec ambiguities (especially around numbers and booleans) made it a poor fit for verbatim param values.

### Can I run multiple sessions concurrently?

Not currently. llamaman binds a single `host:port` per session and the `flock` on `session.json` enforces one-at-a-time. Multiple `llamaman` instances can **reattach** to the same running server (they share the log file read-only), but spawning two `llama-server` processes simultaneously isn't supported.

### Why isn't auto-restart a thing?

`llama-server` exits cleanly on success and non-zero on failure; auto-restart would mask real problems (OOM, model corruption, port conflict) behind a tight retry loop. The run-mode `r` shortcut is one keystroke away when you actually want to restart, with a confirm dialog if the server is `ready`.

### Does it work with Vulkan / ROCm / CUDA / Metal builds of `llama-server`?

Yes — llamaman shells out to whatever `llama-server` binary you point `globals.llama-server-bin` at. The backend (CPU, CUDA, ROCm, Vulkan, Metal once macOS support lands) is decided at llama.cpp build time; llamaman is agnostic.

### Will my old configs work after a llamaman update?

Yes, within schema `version: 1`. The schema evolves additively — new optional fields don't break old configs. Older binaries will reject newer fields with `json: unknown field` (because `Load` uses `DisallowUnknownFields`). When a future release introduces `version: 2` or higher, llamaman will migrate older configs automatically on load and keep a `.bak` of the pre-migration file; today no migration step exists because only one schema version has ever shipped.

### Can I edit the config file by hand?

Yes — it's plain JSON and llamaman re-reads it on every launch. The configuration mode is just a convenience layer with type-aware editing, validation, and atomic save. If you prefer your editor of choice, use it. The schema in [Configuration schema](#configuration-schema) is the contract.

## Acknowledgments

llamaman stands on the work of others. Sincere thanks to:

- **[llama.cpp](https://github.com/ggml-org/llama.cpp)** by Georgi Gerganov and contributors — the runtime llamaman wraps. Without `llama-server`'s clean process surface and self-documenting `--help`, this project couldn't exist in its current shape.
- **[Bubble Tea](https://github.com/charmbracelet/bubbletea)**, **[Lip Gloss](https://github.com/charmbracelet/lipgloss)**, **[Bubbles](https://github.com/charmbracelet/bubbles)**, and **[Huh](https://github.com/charmbracelet/huh)** by the [Charm](https://charm.sh/) crew — the TUI quality is largely theirs.
- **[Kong](https://github.com/alecthomas/kong)** by Alec Thomas — the CLI parser, including the completion machinery exposed via `--completion`.
- **[fsnotify](https://github.com/fsnotify/fsnotify)** — log tailing without polling.
- **[teatest](https://github.com/charmbracelet/x/tree/main/exp/teatest)** — TUI snapshot tests that catch regressions across the four modes.

The `llama-server` CLI surface is the inspiration for llamaman's translation layer. Many small UI choices (the embedded model list, the `c`-to-copy shortcut, the live-highlighted search) were shamelessly inspired by terminal tools that have nothing to do with LLMs but a lot to do with not getting in your way.

## Disclaimer

llamaman is an independent open-source project. It is **not** affiliated with, endorsed by, sponsored by, or otherwise associated with the [llama.cpp](https://github.com/ggml-org/llama.cpp) project, its maintainers, or its contributors. llamaman is a third-party launcher that shells out to `llama-server`; the runtime, the model formats, and the network behaviour are all upstream's work, not ours.

Likewise, llamaman is not affiliated with Meta Platforms, Inc. (the originators of the Llama model family and the "Llama" trademark), with Hugging Face, Inc. (referenced for the `-hf` model source), or with any model author whose weights you may run through this tool. All trademarks, model names, and brand references are the property of their respective owners and are used here for descriptive interoperability purposes only.

If you encounter a bug while running a model under llamaman, please reproduce it against `llama-server` directly before filing it upstream — many issues that surface in the run-mode log come from llama.cpp itself or from the model weights, and the upstream maintainers shouldn't be asked to debug llamaman's argv translation on their behalf.

## License

[MIT](LICENSE).
