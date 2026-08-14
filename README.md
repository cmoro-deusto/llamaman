# llamaman

[![CI](https://github.com/cmoro-deusto/llamaman/actions/workflows/ci.yml/badge.svg)](https://github.com/cmoro-deusto/llamaman/actions/workflows/ci.yml)
[![Latest release](https://img.shields.io/github/v/release/cmoro-deusto/llamaman?display_name=tag&sort=semver)](https://github.com/cmoro-deusto/llamaman/releases/latest)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go version](https://img.shields.io/github/go-mod/go-version/cmoro-deusto/llamaman)](go.mod)
[![AUR](https://img.shields.io/aur/version/llamaman-bin)](https://aur.archlinux.org/packages/llamaman-bin)

A modern terminal manager for [`llama-server`](https://github.com/ggml-org/llama.cpp) (the HTTP server bundled with llama.cpp). Define your models and launch presets once in a JSON config, pick them from a Bubble Tea TUI, follow the log live, detach when you're done, and reattach later from another shell — without ever memorising a 60-flag command line. The same TUI manages your storage and downloads: scan the cache, pull GGUF quants straight from Hugging Face, or search the hub and hand a model straight into your config.

## Why llamaman?

If you've already chosen llama.cpp, you've felt the friction: every model wants different flags, every backend tweak (CUDA layers, context size, flash attention, KV-cache quant) is a new shell-history entry, and you eventually keep a scratchpad of "the command that worked last time". llamaman is the launcher you wish `llama-server` shipped with — your models and presets become first-class config, the TUI knows what flags `llama-server --help` exposes today, and a running server stays running across terminal sessions instead of dying with the shell that started it.

## Highlights

- Models and named launch presets live in one versioned JSON file.
- Detach and reattach across `llamaman` invocations — the server keeps serving.
- Log tailing with live forward search, mouse-wheel scrollback, and `n`/`N` / `g`/`G` navigation.
- Type-aware parameter editor whose autocomplete and validation come straight from `llama-server --help`.
- Local `.gguf` files or Hugging Face identifiers (`-m` vs `-hf`) on a per-model basis.
- **Storage & Downloads manager** (`s`): scan the cache (local models + HF downloads), spot `(cached)` quants, and download GGUF files straight from the hub — pause / resume / cancel, with SHA-256 verification.
- **Hugging Face browser** (`b`): search or browse the hub inside the TUI — trending / downloads / likes / newest / recently-updated rankings, language / license / task / size filters, per-repo metadata and rendered model card with clickable links, and a one-key hand-off into your config or a download.
- **Themes & preferences**: 23 curated palettes (+ `auto`) with a live-preview preferences screen (`p`) and instant `t` / `T` cycling from Main.
- Shell completions for `bash`, `zsh`, and `fish`.
- **Router mode**: serve every model in a `my-models.ini` (llama.cpp model presets) from a single `llama-server`, with per-model load/unload and full live statistics.
- **Import / export**: `llamaman import` ingests a `my-models.ini` as config presets; `llamaman export` / TUI `x` writes one. Every config save also keeps a derived `models.ini` in sync, which doubles as the default Router source.
- XDG-compliant config / state / cache / log paths.
- Single static Go binary, no runtime dependencies beyond `llama-server` itself.

### Configuration

A single JSON file at `${XDG_CONFIG_HOME:-~/.config}/llamaman/config.json` is the source of truth. Each model carries an alias, a source (a local `.gguf` path or a Hugging Face identifier), and any number of named presets. Presets are ordered objects whose key order is preserved end-to-end — the order you write your params in is the order they show up on the `llama-server` command line.

### Process model

llamaman spawns `llama-server` in its own session (`setsid(2)`), so the child outlives `llamaman` itself when you detach. A `flock(2)`-protected `session.json` records the live PID, alias, preset, and full argv, so a second `llamaman` invocation can reattach by reading state instead of guessing. The log file is followed via `fsnotify` rather than polled, so the run-mode viewport stays current without burning CPU.

### TUI experience

Five modes — main (launcher with embedded model list), run (status header + log viewport with search), configuration (three-pane master/detail editor with type-aware param picker), storage (cache scan + download manager), and browse (Hugging Face search). A preferences screen (`p`) re-themes the app live from 23 curated palettes. Modal dialogs overlay the existing screen rather than blanking it, so you never lose context. The wordmark adapts to terminal width; status indicators use colour but respect `NO_COLOR`, and the subtle animations can be toggled off (`a` in run mode or in Preferences).

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
  - [Router mode (multi-model)](#router-mode-multi-model)
- [TUI modes](#tui-modes)
  - [Main mode](#main-mode)
  - [Run mode](#run-mode)
  - [Configuration mode](#configuration-mode)
  - [Preferences (`p`)](#preferences-p)
  - [Storage & Downloads manager (`s`)](#storage--downloads-manager-s)
  - [Hugging Face browser (`b`)](#hugging-face-browser-b)
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
- **Width**: 80 columns minimum; 90+ enables wide mode in run mode (wordmark + live band with `llama-server` and `Hardware` panels). Below 90 cols, run mode collapses to a compact identity strip.
- **Locale**: a UTF-8 locale (`LANG=*.UTF-8`).
- **Clipboard (optional)**: `wl-clipboard` (Wayland) or `xclip` (X11) for the `c` "copy launch command" shortcut. Without either, the shortcut becomes a no-op with a brief status flash.
- **`llama-server`**: a working build of llama.cpp's HTTP server on `PATH` or under one of the standard prefixes (`/usr/local/bin`, `/usr/local/llama.cpp/bin`, `/opt/llama.cpp/bin`). See [Compatibility](#compatibility).
- **Router mode (optional)**: a llama.cpp build with `--models-preset` support (the model-presets feature, Dec 2025 or later). Single-model mode works with any supported build; on an older binary, Router mode shows a clear version-gate error instead of failing to spawn.

## Install

### Prerequisites

llamaman is a launcher, not a runtime — you also need `llama-server` itself. Build llama.cpp from source ([upstream instructions](https://github.com/ggml-org/llama.cpp#building)) or install a packaged build for your distro. Once the binary is reachable, llamaman's first-run flow autodetects it.

The run-mode `Hardware` panel shows live NVIDIA GPU stats via NVML — install `nvidia-utils` (or your distro's equivalent providing `libnvidia-ml.so`) to see GPU rows. The binary works without it; the panel just shows CPU rows only.

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

> **AUR temporarily unavailable**  
If the AUR is down or the automated push is paused, the release workflow still generates a PKGBUILD and publishes it as a GitHub Release asset `llamaman-bin-<version>-aur.tar.gz`. You can build the package manually:
```sh
# Download the asset from the releases page, e.g.
# https://github.com/cmoro-deusto/llamaman/releases
wget -O llamaman-bin-aur.tar.gz https://github.com/cmoro-deusto/llamaman/releases/download/<tag>/llamaman-bin-<tag>-aur.tar.gz
tar -xzf llamaman-bin-aur.tar.gz
makepkg -si
```
The tarball contains the generated `PKGBUILD` and `.SRCINFO` for `llamaman-bin`. This is a temporary fallback while AUR publishing is disabled.

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
llamaman -l             # list configured models and router sources
llamaman -p <alias>     # list presets for a model
llamaman -i models.ini  # run a my-models.ini in router mode
llamaman import x.ini   # ingest a my-models.ini as config presets
llamaman export         # write the config as my-models.ini (stdout)
```

The first invocation with no config triggers a setup flow that writes `${XDG_CONFIG_HOME:-~/.config}/llamaman/config.json` with autodetected defaults. Router sources are registered in `globals.models-files`; when unset, the derived `<config-dir>/models.ini` — auto-written on every config save — is the default source, so Router mode works out of the box.

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

### Router mode (multi-model)

llama.cpp's model presets let **one** `llama-server` host every model in a `my-models.ini` file, routing requests by model id. llamaman treats the file — not an individual section — as the run target.

1. **Register a source.** Main mode → `c` → Globals → *models files* (one `my-models.ini` path per line). When the list is empty, the derived `<config-dir>/models.ini` is used by default, so there's nothing to set up. For one-off runs: `llamaman -i models.ini`.
2. **Switch to Router mode.** In main mode, press `tab` to toggle **Single Model ↔ Router**; the list now shows your router sources with their section counts.
3. **Launch.** `Enter` on a source. Run mode's left panel becomes the **router models list** — every model in the file with its live state (`loaded` / `loading` / `unloaded`).
4. **Manage models.** `tab` switches which panel the arrows control. In the models panel: `↑`/`↓` select, `l` loads, `u` unloads (confirmed), `Enter` opens an action menu (Load / Unload / Statistics / Info), `s` shows that model's full seven-row statistics panel (Esc closes it), `p` hides cache-only leftovers.

Models you ran outside llamaman (via `llama-cli` / `llama-server -hf`) show up here too, because the router lists HF-cache downloads as well as ini sections — they're tagged `(cache)`, and the ini-only filter (`p`) hides them. Exported files are router-valid by construction: each preset becomes its own section with a unique alias.

## TUI modes

### Main mode

A centred window: the figlet "llamaman" wordmark, version line, and an inline single-row-per-model selection list (when at least one model is configured). Pre-selected: the first model. Each row shows the alias with a leading **source tag** (`local` for `.gguf` paths, `hf` for Hugging Face ids, `router` for `my-models.ini` sources), the preset count, and an optional `(running)` marker. The highlighted row previews its preset names when a model has 2+ presets.

If a session is running, an extra line above the list reads `▶ Detached: <alias>/<preset> listening on :<port> — press a to attach`. In Router mode (toggle with `tab`), the list shows your `my-models.ini` sources instead — see [Router mode (multi-model)](#router-mode-multi-model).

If **no** models are configured (e.g. immediately after first-run setup), the list is hidden so users aren't confronted with an empty box.

| Key | Action |
|---|---|
| `↑` / `↓` | Move selection in the inline list (only when models exist) |
| `tab` | Toggle **Single Model ↔ Router** mode (Router shows `my-models.ini` sources) |
| `Enter` | Run the selected model. With 2+ presets, the box pivots to a preset sub-list (Enter to confirm, Esc to back out) |
| `Esc` | Back out of the preset sub-list to the model list |
| `s` | Storage & Downloads manager |
| `b` | Hugging Face browser |
| `c` | Configuration mode |
| `p` | Preferences (theme, animations, log colours, models dir) |
| `t` / `T` | Cycle the theme forward / backward |
| `a` | Attach to running session (only shown when a session is running) |
| `?` | Help overlay |
| `q` | Quit |

Row order follows the configuration file's `models[]` order; reorder via `Shift+↑`/`Shift+↓` in configuration mode. There is no separate "selection mode" — model selection lives in main mode.

### Run mode

```
╭─────────────────────────────────────────────────────────────────────────────╮
│ ▜  ▜                                                                        │
│ ▐  ▐  ▝▀▖ ...   Alias: alpha    Server: 8994     Context Size: 8192         │
│ ▐  ▐  ▞▀▌ ...   Preset: fast    Uptime: 00:01:30   [READY]                  │
│  ▘  ▘ ▝▀▘ ...                                                               │
╰─────────────────────────────────────────────────────────────────────────────╯
╭── llama-server ──────────────────────╮╭── Hardware ────────────────────────╮
│ Tokens <spark>   80.0 /  60.0 tps   ││ [0] AMD Ryzen 9 7950X              │
│ Prompt <spark> 2331.0 /2300.0 tps   ││     Util  <spark>          23.4%   │
│                          Busy  2/4  ││     RAM   <bar> 41.0G/64.0G  65.2% │
│                          Queued 1   ││     Power <bar> 32W / 125W   25.6% │
│                                     ││     Temp  <bar> 68°C/100°C   68.0% │
│                                     ││ [0] NVIDIA GeForce RTX 4090        │
│                                     ││     Util  <spark>          89.0%   │
│                                     ││     VRAM  <bar> 21.1G/24.0G  87.9% │
│                                     ││     Power <bar> 320W/450W    71.1% │
│                                     ││     Temp  <bar> 72°C/83°C    86.7% Fan 65% │
╰─────────────────────────────────────╯╰────────────────────────────────────╯
╭─ output (tailing) ──────────────────────────────────────────────────────────╮
│ main: HTTP server is listening, hostname: 127.0.0.1, port: 9080             │
│ ▼                                                                           │
╰─ q: quit  k: kill  r: restart  c: copy  i: info  /: search  ?: help ────────╯
```

Two-section header (DESIGN.md §7.4): a **top strip** with identity cells (alias, `llama-server` version, ctx size, preset, uptime, status badge) and a **live band** of two side-by-side panels — `llama-server` (tokens/s + prompt-eval rates as 40-cell sparklines, busy/queued counts) and `Hardware` (per-CPU + per-GPU rows with util sparkline, RAM/VRAM bar with bytes, Power bar, Temp bar; values colored across blue/green/yellow/red zones). Press `i` for an overlay with the model + every preset param in source order. Two-state width machine: at <90 cols, the wordmark and live band drop and only the identity strip remains. Hardware polling starts at run-mode birth — values appear immediately, no need to wait for the server to be ready.

Status state machine: `starting → ready → exited|error`. `ready` is detected by matching `server is listening` in stdout. Auto-scroll is locked to the bottom unless you've scrolled up; when scrolled up, a `↓ N new lines` indicator appears, and `G` returns to live tail.

| Key | Action |
|---|---|
| `q` / `Ctrl+C` | Quit prompt: `(k)ill` returns to main, `(d)etach` exits llamaman leaving `llama-server` running, `(c)ancel` stays put |
| `esc` | Back to main — detach (the server keeps running) |
| `o` | Toggle log line-kind colours (persists to preferences) |
| `a` | Toggle subtle animations (persists to preferences) |
| `k` | Direct kill (with `(y)es / (n)o` confirm). Stops the server, removes log + session, returns to main; llamaman stays open |
| `r` | Restart the server (confirm if status is `ready`) |
| `c` | Copy the full launch command to clipboard (`wl-copy` → `xclip` fallback) |
| `i` | Show model & preset detail overlay — alias + Source/HF + every preset param in source order. Any key closes |
| `/` | Search forward — matches highlighted live (reverse video) as you type, persist after `Enter` for `n`/`N` |
| `Esc` | Clear the active search and remove highlights |
| `n` / `N` | Next / previous match |
| `g` / `G` | Jump to top / bottom |
| `Space` / `b` | Page down / up |
| `↑` / `↓` / wheel | Scroll one line — `j`/`k` are deliberately not bound here so `k` is free for kill |
| `?` | Help overlay |

ANSI colour codes from `llama-server` are passed through. Scrollback is unlimited and sourced from the on-disk log file.

### Router mode (run)

In a router run, the left live-band panel becomes the **router models list** instead of the single-model stats, and the whole UI splits into two panels with **panel-scoped shortcuts**: `tab` moves the ↑/↓ arrows between the log and the models panel (the focused panel's border lights up, and the footer shows the active panel's keys). The orange border marks whichever panel the arrows control.

| Key | Panel | Action |
|---|---|---|
| `tab` | global | Switch which panel ↑/↓ control (log ↔ models) |
| `↑` / `↓` | models | Move the selection (list scrolls to keep it visible) |
| `Enter` | models | Action menu: Load / Unload / Statistics / Info (inapplicable entries grayed) |
| `s` | models | Show the selected model's full stats panel (Esc closes) |
| `l` / `u` | models | Load / unload the selected model (`u` confirms) |
| `p` | models | Toggle ini-only filter — hide `(cache)` leftovers from external `llama-cli` runs |
| `d` | log | Toggle denoise (hides llama.cpp's per-request "proxying request to model …" chatter at render time; default on) |
| `Esc` | global | Layered: close the action menu → close stats → clear search |

The models panel rows show `●`/`○`/`◐` load state, context usage and tokens/s for loaded models, and a dimmed `(cache)` tag for HF-cache leftovers. The stats panel (`s`) is the same seven-row panel as single-model mode — rates with sparklines, Process/Context/Breakdown/Cache/Gen bars, TTFT — fed per model from `/slots?model=` and `/metrics?model=` (fetched with `autoload=false`, so monitoring can never reload an unloaded model). Press `i` on a selection for its launch argv.

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
└── Tab: pane ─ e: edit ─ c: dup ─ k: clone-to ─ d: del ─ s: save ─ Esc: back ───┘
```

| Key | Action |
|---|---|
| `Tab` / `Shift+Tab` | Cycle focus across panes |
| `←` / `→` (or `h` / `l`) | Same as Tab/Shift+Tab |
| `↑` / `↓` | Navigate within the focused pane |
| `Shift+↑` / `Shift+↓` | Reorder the focused row (models, presets, or params) |
| `e` | Edit the highlighted item |
| `n` | New item (model / preset / param, depending on focused pane) |
| `p` | Paste a llama-server command line (Models pane — imports a model + preset) |
| `c` | Duplicate (models and presets, within the same parent) |
| `k` | Clone preset to another model (Presets pane only — opens a target-model select + new-name input) |
| `d` | Delete (with confirm where lossy) |
| `g` | Open the Globals form (binary path, host, port) |
| `s` | Save (atomic, with `.bak`) |
| `Esc` | Back. With unsaved changes, prompts Save / Discard / Cancel |
| `?` | Help overlay |

The new-param picker shows every flag's bare name + parsed help description. Just start typing to filter — there's no `/`-then-prompt step. The value editor is type-aware: booleans are yes/no toggles, numerics use a numeric input, parsed enums (e.g. `ctk`/`ctv`'s known set, or any `[a|b|c]` placeholder in `--help`) are pickers, everything else is a text input.

Form behaviour: forms are `huh` instances mounted via the Bubble Tea message loop. Modal dialogs (kill confirm, restart confirm, save/discard prompt, help) overlay the existing screen rather than blanking it.

The **location** (`location: "~/models/foo.gguf"`) and **HF** (`hf: "org/repo:QUANT"`) fields of the model form carry a **`ctrl+o` picker**: the local picker is a standard file browser (`↑`/`↓` navigate, `enter` selects, `.` toggles hidden files) and the HF picker is a live type-to-filter list of cached repos with `(cached)` markers. Typing an HF id and pressing Enter runs a **typed-repo check** against the hub; a repo that exposes quants is offered the shared quant chooser (`Tag — size`, `(cached)` markers), so `hf: "org/repo"` becomes `hf: "org/repo:Q4_K_M"` in one step — the same chooser the Storage manager uses.

**Paste a command line (`p`, Models pane).** Paste a llama-server command line — with or without the `llama-server` binary name — and it is tokenized, validated against the installed server's flags, and turned into config: a **model + preset** (new model, alias derived from `--alias` > file basename > repo name, editable in the confirm step), a **preset on an existing entry** (matched by the model path or the full `org/repo:quant` id — a different quant is a different model), or, when the command line has no model flag, a **preset on a model you pick** (existing models or "＋ create new model…"). A preset is always created (name defaults to `pasted`). Quotes and escapes are honoured; `$VAR`/`~`/globs are stored literally (the model path is expanded at load like any other config path). Malformed flags (missing/empty values, non-numeric numerics, both `-m` and `-hf`, an invalid HF id) block the import with the offending tokens; unknown flags and overwritten repeats import with warnings. A bare `-hf org/repo` chains the shared quant chooser before committing.

### Preferences (`p`)

A preferences form applied and persisted immediately:

| Setting | Meaning |
|---|---|
| **theme** | One of **23 curated palettes** plus `auto` (the default — picks dark/light from your terminal background). Every palette shows its dark and light variant where applicable, and the whole app re-themes live as you move through the list. |
| **animations** | Subtle transitions (dot pulse, badge breathing). Also toggled with `a` in run mode. |
| **log colours** | Render-time line-kind colouring of the run-mode log. Also toggled with `o` in run mode. |
| **models directory** | The cache root for HF models and downloads. Default: `$LLAMA_CACHE` → `~/.cache/huggingface/hub`, shared with `llama-cli`. |

`t` / `T` on the main screen cycle the theme without opening preferences; a mismatched palette (light palette on a dark terminal, say) still applies but flashes a warning.

### Storage & Downloads manager (`s`)

Everything that touches disk, in one place: local models, HF cache repos, and downloads — grouped with `(cached)` markers on quant rows wherever you look. `↑`/`↓` move, `enter` opens a context action menu (download a repo's quant, delete files, open the containing folder, remove a config entry), `d` starts a new download (type `org/repo`, then pick the quant), `esc` backs out.

Downloads run in the manager with **pause / resume / cancel** — resume reuses the partial file via HTTP `Range` — and **SHA-256 verification** against the hub's declared hashes. Progress shows bytes/total and speed; completed files land in the models directory and show up as `(cached)` everywhere else.

| Key | Action |
|---|---|
| `↑` / `↓` | Select a row |
| `enter` | Action menu (download / pause / resume / cancel / delete / open folder — depends on the row) |
| `d` | New download: type `org/repo`, then pick a quant from the chooser |
| `esc` | Back to main |

### Hugging Face browser (`b`)

Search or browse the hub without leaving the TUI. An empty query **browses** by rank; `s` cycles the sort (**trending → downloads → likes → newest → updated**). `l` / `L` / `k` / `m` filter by **language, license, task**, and **params size** (params is a name-derived estimate, filtered client-side over the current page). The right column auto-follows the selected repo: **model info** (params, downloads, likes, license, task), the **quants list** with real sizes and `(cached)` markers, and the **rendered model card** — markdown with headings, tables, code blocks and ctrl+clickable links.

`tab` cycles the three zones (search bar ↔ results ↔ quants). `↑`/`↓` navigate the results (the right column follows) or select a quant; `enter` in the quants list hands the picked repo+quant off — **add to config** (the new-model form opens pre-filled) or **download now** (the Storage manager starts it). `pgup` / `pgdn` scroll the model card from any zone.

| Key | Action |
|---|---|
| `enter` | Run the search (empty query = browse) |
| `s` | Cycle sort: trending → downloads → likes → newest → updated |
| `l` / `L` / `k` / `m` | Filter: language / license / task / params size |
| `tab` / `Shift+tab` | Cycle zones: search bar ↔ results ↔ quants |
| `↑` / `↓` | Results: navigate (right column follows) · Quants: select a quant |
| `enter` | Quants: hand off the selected quant (add to config / download now) |
| `pgup` / `pgdn` | Scroll the model card (any zone) |
| `esc` | Back one step, then to main |

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
    "port":             <integer 1..65535>,        // REQUIRED
    "models-files":     ["<path to my-models.ini>"]  // OPTIONAL router sources; unset → derived <config-dir>/models.ini
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
| Derived `my-models.ini` (auto-written on every config save) | `${XDG_CONFIG_HOME:-~/.config}/llamaman/models.ini` |
| HF cache / downloads (Storage manager + browser `(cached)` markers) | `preferences.models-dir` (default `$LLAMA_CACHE` → `~/.cache/huggingface/hub`) |
| Config backup (rolling) | `${XDG_CONFIG_HOME:-~/.config}/llamaman/config.json.bak` |

Set `LLAMAMAN_DEBUG=1` to raise the debug log level to `DEBUG`.

The session record and `llama-server` log live under `XDG_RUNTIME_DIR` and are wiped at reboot — that's intentional. The debug log rotates at 10 MB and keeps one prior file.

## Compatibility

llamaman targets a recent build of llama.cpp's `llama-server`. Because flags are discovered dynamically from `llama-server --help`, most upstream changes are absorbed automatically — a flag rename surfaces as an "unknown flag" warning in the next launch's run-mode header rather than a crash, and existing config keeps working until you update the param to its new name. Brand-new flags are usable in presets immediately, with no llamaman release required.

The minimum tested `llama-server` build is **`build 8994 (aab68217b)`**. Older builds will probably work for the common-case flags; very old builds (predating dynamic `--help` machine-readable structure) may fall back to llamaman's hard-coded short-form set.

**Router mode** needs the model-presets feature (`--models-preset`), added in llama.cpp in Dec 2025. On older builds the spawn is blocked with a clear version-gate error; single-model mode is unaffected.

## Keybindings cheat sheet

> Reference table consolidating every binding from the per-mode tables above. The per-mode sections remain the source of truth — when in doubt, check there.

| Mode | Key | Action |
|---|---|---|
| Main | `↑` / `↓` | Move selection in inline list |
| Main | `tab` | Toggle Single Model ↔ Router mode |
| Main | `Enter` | Run selected model (pivot to preset sub-list if 2+ presets) |
| Main | `Esc` | Back out of preset sub-list |
| Main | `s` | Storage & Downloads manager |
| Main | `b` | Hugging Face browser |
| Main | `c` | Open configuration mode |
| Main | `p` | Preferences (theme, animations, log colours, models dir) |
| Main | `t` / `T` | Cycle theme forward / backward |
| Main | `a` | Attach to running session |
| Main | `?` | Help overlay |
| Main | `q` | Quit |
| Run | `esc` | Back to main — detach (server keeps running) |
| Run | `o` | Toggle log line-kind colours (persists) |
| Run | `a` | Toggle animations (persists) |
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
| Router | `tab` | Switch which panel ↑/↓ control (log ↔ models) |
| Router | `↑` / `↓` | Models panel: move selection (list scrolls) |
| Router | `Enter` | Models panel: action menu (Load / Unload / Statistics / Info) |
| Router | `s` | Models panel: show selected model's stats (Esc closes) |
| Router | `l` / `u` | Models panel: load / unload selected model (`u` confirms) |
| Router | `p` | Models panel: toggle ini-only filter (hide `(cache)` leftovers) |
| Router | `d` | Log panel: toggle denoise (default on) |
| Storage | `↑` / `↓` | Select a row |
| Storage | `enter` | Action menu (download / pause / resume / cancel / delete / open folder) |
| Storage | `d` | New download: type `org/repo`, then pick a quant |
| Storage | `esc` | Back to main |
| Browser | `enter` | Search (empty query = browse) |
| Browser | `s` | Cycle sort: trending → downloads → likes → newest → updated |
| Browser | `l` / `L` / `k` / `m` | Filter: language / license / task / params size |
| Browser | `tab` / `Shift+tab` | Cycle zones: search ↔ results ↔ quants |
| Browser | `↑` / `↓` | Results: navigate (right column follows) · Quants: select a quant |
| Browser | `enter` | Quants: hand off selected quant (add to config / download now) |
| Browser | `pgup` / `pgdn` | Scroll the model card (any zone) |
| Config | `Tab` / `Shift+Tab` | Cycle panes (also `←` / `→`, `h` / `l`) |
| Config | `↑` / `↓` | Navigate within pane |
| Config | `Shift+↑` / `Shift+↓` | Reorder rows |
| Config | `e` | Edit highlighted item |
| Config | `n` | New item (model / preset / param) |
| Config | `c` | Duplicate (models, presets) |
| Config | `k` | Clone preset to another model (Presets pane) |
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
- **No config schema migrations *yet*.** Schema version is `1` and evolves additively — new optional fields don't break old configs, so no migration is needed today. Automatic in-place migration (with a `.bak` of the prior file) is planned for whenever a future release introduces `version: 2` or higher.
- **Browser metadata heuristics.** The params count is derived from the repo name (the search API exposes no params field) and the params-size filter applies to the current page only — treat both as estimates, not facts.
- **Gated / private models** need an `HF_TOKEN` (read from the environment); without one, downloads fail with a clear message rather than guessing.

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

### Why do models I never configured show up in Router mode?

llama.cpp's router lists models from three places: your `my-models.ini` sections, files in `--models-dir`, and the **HF download cache** — anything you've ever run with `llama-cli` / `llama-server -hf` (or llamaman itself) gets cached and enumerated by the router. Those leftovers appear in the models panel tagged `(cache)`, and the ini-only filter (`p` in the models panel) hides them. The router can delete cache-only entries via `DELETE /models?model=<id>`; llama.cpp never writes to your ini.

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
- **In-process TUI snapshot tests** (a stub spawner plus a custom `drainCmds` harness) — render every mode without a terminal and catch layout regressions across the modes.

The `llama-server` CLI surface is the inspiration for llamaman's translation layer. Many small UI choices (the embedded model list, the `c`-to-copy shortcut, the live-highlighted search) were shamelessly inspired by terminal tools that have nothing to do with LLMs but a lot to do with not getting in your way.

## Disclaimer

llamaman is an independent open-source project. It is **not** affiliated with, endorsed by, sponsored by, or otherwise associated with the [llama.cpp](https://github.com/ggml-org/llama.cpp) project, its maintainers, or its contributors. llamaman is a third-party launcher that shells out to `llama-server`; the runtime, the model formats, and the network behaviour are all upstream's work, not ours.

Likewise, llamaman is not affiliated with Meta Platforms, Inc. (the originators of the Llama model family and the "Llama" trademark), with Hugging Face, Inc. (referenced for the `-hf` model source), or with any model author whose weights you may run through this tool. All trademarks, model names, and brand references are the property of their respective owners and are used here for descriptive interoperability purposes only.

If you encounter a bug while running a model under llamaman, please reproduce it against `llama-server` directly before filing it upstream — many issues that surface in the run-mode log come from llama.cpp itself or from the model weights, and the upstream maintainers shouldn't be asked to debug llamaman's argv translation on their behalf.

## License

[MIT](LICENSE).
