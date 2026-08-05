# llamaman — Agent Guide

## What it is

`llamaman` is a single-binary Go CLI + TUI that manages `llama-server` (the HTTP server bundled with [llama.cpp](https://github.com/ggml-org/llama.cpp)). Users define models and launch presets in a JSON config, pick them from a Bubble Tea TUI, follow logs live, detach/reattach across terminal sessions, and edit config through a three-pane editor — without memorising 60-flag command lines.

**Repository:** `github.com/cmoro-deusto/llamaman`
**License:** MIT
**Platform:** Linux only (amd64, arm64)

---

## Architecture

```
main.go                          CLI entry — Kong parser, dispatch, TUI bootstrap
completion.go                    Shell completion scripts (bash/zsh/fish)
├── internal/config/             JSON config load/save/validate
├── internal/flags/              llama-server --help parser, short/long form registry, caching
├── internal/translate/          Config → argv translation (param ordering, overrides)
├── internal/server/             Process spawn (setsid), session.json (flock), log management
├── internal/tui/                Bubble Tea TUI (root, main, run, config, first-run modes)
├── internal/llamaapi/           HTTP client for llama-server /props endpoint
├── internal/hwinfo/             CPU + NVIDIA GPU stats (NVML) for run-mode Hardware panel
├── internal/paths/              XDG-compliant path resolution
├── internal/logging/            slog → file setup
└── cmd/llamaman-fakeserver/     Fake llama-server for integration tests
```

### Key design decisions

- **Single source of truth:** `~/.config/llamaman/config.json` (XDG-compliant). Edits only via TUI config mode.
- **Detach/reattach:** `llama-server` is spawned with `setsid(2)` so it survives `llamaman` exit. Session state in `session.json` (flock-protected) lets new invocations reattach.
- **Flag registry:** `llama-server --help` is parsed at startup and cached by binary mtime. Drives short-vs-long flag form and unknown-flag warnings. Falls back to hard-coded set if unavailable.
- **Param order preserved:** Custom JSON unmarshaler keeps preset param key order → argv order.
- **Non-blocking warnings:** Unknown flags, missing model files, missing binary → warnings, not errors. Forward-compatible with new llama-server versions.

### TUI modes

| Mode | File | Purpose |
|---|---|---|
| Root | `internal/tui/root.go` | Top-level Bubble Tea model — owns and dispatches to sub-modes |
| Main | `internal/tui/main.go` | Centered launcher with embedded model list |
| Run | `internal/tui/run.go` | Status header + log viewport with search, scrollback, hardware panel |
| Config | `internal/tui/config.go` | Three-pane master/detail editor (models → presets → params) with `huh` forms |
| FirstRun | `internal/tui/firstrun.go` | Guided setup when no config exists |

---

## Code conventions

### Go module
- **Module:** `github.com/cmoro-deusto/llamaman`
- **Go version:** 1.26.2 (use `go stable` in CI)
- **CGO:** required for NVML GPU stats (`github.com/NVIDIA/go-nvml`). Build with `CGO_ENABLED=1`. The fakeserver test binary is pure Go (`CGO_ENABLED=0`).

### Dependencies
| Package | Role |
|---|---|
| `charmbracelet/bubbletea` | TUI framework |
| `charmbracelet/bubbles` | Widgets (list, viewport, textinput) |
| `charmbracelet/huh` | Forms (config mode) |
| `charmbracelet/lipgloss` | Styling |
| `alecthomas/kong` | CLI parsing |
| `fsnotify/fsnotify` | Log file watching |
| `NVIDIA/go-nvml` | GPU stats (CGO) |
| `shirou/gopsutil/v3` | CPU/memory stats |

### Naming and structure
- Internal packages are flat under `internal/` — no sub-packages.
- Each package groups related `.go` files by concern (e.g., `config/load.go`, `config/save.go`, `config/validate.go`).
- Tests co-located: `*_test.go` alongside source.
- TUI snapshot tests render the Bubble Tea model in-process with a stub spawner — no `teatest` dependency (`internal/tui/snapshot_test.go`); teatest is NOT in go.mod despite mentions in README/DESIGN.
- Exit codes are documented constants in `main.go` (§4.4 of DESIGN.md): 0=OK, 1=generic, 2=config, 3=prereq, 4=port-in-use, 130=interrupted.

### Config schema
```json
{
  "version": 1,
  "globals": {
    "llama-server-bin": "/usr/local/bin/llama-server",
    "ip_address": "127.0.0.1",
    "port": 9080
  },
  "models": [
    {
      "alias": "my-model",
      "location": "~/models/model.gguf",
      "presets": [
        {
          "preset": "default",
          "description": "balanced",
          "params": { "ngl": 99, "ctx-size": 8192 }
        }
      ]
    }
  ]
}
```
Each model has exactly one of `location` (local `.gguf`) or `hf` (Hugging Face ID). Never both.

### CLI synopsis
```
llamaman [options] [<alias> [<preset>]]
  -l, --list              List models
  -p, --presets           Print presets for <alias>
  -c, --config PATH       Alternate config file
  --completion SHELL      Print completion script (bash, zsh, fish)
  --version               Print version
```

---

## Development workflow

### Prerequisites
- Go 1.26+ (or `go stable`)
- GCC (for CGO/NVML)
- `gcc-aarch64-linux-gnu` (for cross-compile arm64)
- `llama-server` binary (for manual testing; not required for unit tests)

### Build
```bash
go build -o bin/llamaman .
```

### Test
```bash
go vet ./...
go test ./...
```
The CI also builds `cmd/llamaman-fakeserver` (a fake llama-server for integration tests) and runs it alongside the test suite.

### Run
```bash
./bin/llamaman              # TUI (first-run if no config)
./bin/llamaman <alias>      # Start specific model
./bin/llamaman <alias> <preset>  # Start specific preset
./bin/llamaman -l           # List models (no TUI)
```

### Release
Releases are automated via GoReleaser on `v*` tags pushed to `main`. See `.goreleaser.yaml` and `.github/workflows/release.yml`. Produces:
- `linux/amd64` and `linux/arm64` tarballs with checksums
- GitHub Release with changelog
- AUR package (`llamaman-bin`)

---

## Key files for changes

| Task | Files |
|---|---|
| Add a new CLI flag | `main.go` (CLI struct) |
| Change config schema | `internal/config/types.go`, `internal/config/validate.go`, `internal/config/save.go` |
| Modify TUI main mode | `internal/tui/main.go` |
| Modify TUI run mode | `internal/tui/run.go` |
| Run-mode helpers (live log bar, /props fetch, overlay, zones) | `internal/tui/livebar.go`, `fetcher.go`, `overlay.go`, `zones.go` |
| Modify TUI config mode | `internal/tui/config.go` |
| Change param translation | `internal/translate/translate.go` |
| Change flag parsing from --help | `internal/flags/parser.go`, `internal/flags/fallback.go` |
| Change spawn/session logic | `internal/server/spawn.go`, `internal/server/session.go` |
| Change shell completions | `completion.go` |
| Add hardware info | `internal/hwinfo/` |
| Update DESIGN.md first | `DESIGN.md` is the canonical design reference; code should match it |

---

## Testing notes

- Unit tests cover config load/save/validate, flag parsing, translation, paths, server session, and TUI snapshot rendering.
- Integration tests use `cmd/llamaman-fakeserver` — a minimal HTTP server mimicking llama-server's `/props` endpoint.
- TUI snapshot tests render models in-process (stub spawner) plus spawn-the-fakeserver integration tests for run-mode tail/reattach.
- Run `go test ./...` to execute the full suite. Fakeserver-dependent tests `t.Skipf` when `bin/llamaman-fakeserver` is absent, so build it first for full coverage (`CGO_ENABLED=0 go build -o bin/llamaman-fakeserver ./cmd/llamaman-fakeserver`).

---

## File paths (runtime)

| File | XDG path | Purpose |
|---|---|---|
| Config | `$XDG_CONFIG_HOME/llamaman/config.json` | Model/preset definitions |
| Session | `$XDG_RUNTIME_DIR/llamaman/session.json` | Live PID, alias, argv |
| Log | `$XDG_RUNTIME_DIR/llamaman/llama-server.log` | llama-server stdout+stderr |
| Flag cache | `$XDG_CACHE_HOME/llamaman/flags-<mtime>.json` | Parsed --help output |
| App log | `$XDG_STATE_HOME/llamaman/llamaman.log` | llamaman's own slog output |
