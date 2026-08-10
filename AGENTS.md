# Repository Guidelines

## Project Overview
`llamaman` is a single-binary Go CLI + TUI that manages `llama-server`. Users define models and launch presets in a JSON config, pick them from a Bubble Tea TUI, follow logs live, detach/reattach across terminal sessions, and edit config without memorising 60-flag command lines.

Module: `github.com/cmoro-deusto/llamaman`
Go: 1.26.2 (CI uses `go-version: stable`)
Platform: Linux only, amd64/arm64
License: MIT

## Architecture & Data Flow
CLI entry → config load/validate/save → flag registry → translate → server spawn/session → TUI

- `main.go` boots Kong CLI parsing, decides entry mode, injects `tui.Spawner`.
- `internal/config` loads XDG config `~/.config/llamaman/config.json`, expands `~/$VAR`, validates, saves atomically with one rolling `.bak` (`save.go`).
- `internal/flags` parses `llama-server --help`, builds registry, caches by binary mtime, falls back to hard-coded set.
- `internal/translate` converts Config → argv preserving param order, handles preset overrides and router mode (`--models-preset` from a `my-models.ini`).
- `internal/server` spawns `llama-server` via `setsid(2)`, tracks PID in `session.json` protected by `flock`, tails logs via `fsnotify`, supports adopt/stop/reattach.
- `internal/tui` Root Bubble Tea model dispatches Main/Run/Config/FirstRun/Settings/Storage/Browser modes. Async via `tea.Cmd` + goroutines for spawn wait and log tail.
- `internal/llamaapi` fetches `/props` for live context.
- `internal/hwinfo` provides CPU/GPU stats via gopsutil + NVML for Run hardware panel.

### Runtime paths (XDG)
| File | Path | Purpose |
|---|---|---|
| Config | `$XDG_CONFIG_HOME/llamaman/config.json` | Model/preset definitions |
| Session | `$XDG_RUNTIME_DIR/llamaman/session.json` | Live PID, alias, argv; `flock`-protected |
| Server log | `$XDG_RUNTIME_DIR/llamaman/llama-server.log` | llama-server stdout+stderr |
| Flag cache | `$XDG_CACHE_HOME/llamaman/flags-<mtime>.json` | Parsed `--help` output |
| App log | `$XDG_STATE_HOME/llamaman/llamaman.log` | llamaman's own slog output (rotates to `.1`) |

Unset XDG vars fall back to `~/.config`, `~/.cache`, `~/.local/state`, and runtime dir `/tmp/llamaman-$UID/llamaman` (`internal/paths/paths.go`).

## Key Directories
- `internal/config/` – schema types, load/save/validate, ordered params unmarshaler
- `internal/flags/` – `--help` parser, registry cache, fallback
- `internal/translate/` – config→argv, router builder
- `internal/server/` – spawn, session, log tail
- `internal/tui/` – Bubble Tea modes: `root.go`, `main.go`, `run.go`, `config.go`, `firstrun.go`, `settings.go`, `storage.go`, `browser.go`; helpers `common.go` (Wordmark embed, Theme), `modelpicker.go`, `param_picker.go`, `hfcheck.go`, `markdown.go`, `theme.go`, `anim.go`, `highlight.go`, `livebar.go`, `fetcher.go`, `overlay.go`, `zones.go`
- `internal/storage/` – llama.cpp HF cache layout resolution + model scan (read-only, never mutates; DESIGN §16.1)
- `internal/hf/` – Hugging Face client: search, quant resolution, refs, model card, download with SHA-256 verification
- `internal/modelsini/` – `my-models.ini` import/export and router-file derivation
- `internal/paths/` – XDG resolution
- `internal/logging/` – slog init
- `internal/llamaapi/` – HTTP client
- `internal/hwinfo/` – CPU/GPU stats
- `cmd/llamaman-fakeserver/` – pure-Go fake llama-server for integration tests

## Development Commands
Build:
```bash
go build -o bin/llamaman .
CGO_ENABLED=0 go build -o bin/llamaman-fakeserver ./cmd/llamaman-fakeserver
```

Lint / vet:
```bash
go vet ./...
```

Test:
```bash
go test ./...
```

CLI synopsis:
```
llamaman [flags] [<alias> [<preset>]]
  -l, --list            List configured models and router sources
  -p, --presets         Print presets for <alias>
  -c, --config PATH     Alternate config file
  -i, --ini PATH        Run a my-models.ini file in router mode (--models-preset)
      --completion SH   Print completion script (bash, zsh, fish)
      --version         Print version
  llamaman import FILE        Import a my-models.ini file into config
  llamaman export [PATH]      Export config as my-models.ini (default: stdout; -o PATH alternative)
```

Release: GoReleaser v2 (`~> v2`) builds `llamaman` (CGO_ENABLED=1, cross-compiles arm64 with `aarch64-linux-gnu-gcc`) and `llamaman-fakeserver` (CGO_ENABLED=0), ldflags embed version/commit/date. CI in `.github/workflows/ci.yml`, release in `.github/workflows/release.yml` (tags only, must be reachable from main).

## Code Conventions & Common Patterns
- Naming: exported `PascalCase`, unexported `camelCase`, constants `UPPER_SNAKE_CASE`. No wildcard imports.
- DI: constructor injection via interfaces, e.g. `tui.Spawner`. No global singletons.
- Errors: wrap with `fmt.Errorf("%w", err)`, check with `errors.Is/As`.
- Async: Bubble Tea `tea.Cmd` for UI, goroutines for spawn wait and `fsnotify` log tailing.
- State: Bubble Tea models hold UI state; config is single source of truth with atomic write+backup.
- Concurrency safety: `flock` on `session.json` for exclusive start/reattach.
- Param order preserved via custom JSON unmarshaler in `internal/config/params.go`.
- Warnings non-blocking for unknown flags, missing model/binary.
- Exit codes (constants in `main.go`, DESIGN §4.4): 0=OK, 1=generic, 2=config, 3=prereq, 4=port-in-use, 130=interrupted.

## Important Files
- `main.go` – CLI entry, Kong parsing, dispatch, exit codes
- `completion.go` – bash/zsh/fish completion
- `internal/config/types.go` – Config schema v1
- `internal/tui/root.go` – top-level Bubble Tea model, mode routing
- `internal/server/session.go` – flock session management
- `internal/flags/parser.go` – `--help` parsing
- `DESIGN.md` – canonical design reference. **Update it before code (ROADMAP §9); a PR that contradicts DESIGN.md updates the doc in the same PR.**
- `README.md` – user docs, config schema, keybindings
- `ROADMAP.md` – principles and sequencing
- `.goreleaser.yaml` – release build matrix

## Runtime / Tooling Preferences
- Runtime: Go 1.26+, Linux amd64/arm64, CGO enabled for NVML, GCC required, `gcc-aarch64-linux-gnu` for cross-compile.
- Package manager: Go modules `go.mod`/`go.sum`. No Makefile.
- Tooling: `go build`, `go vet`, `go test`. GoReleaser for releases. GitHub Actions CI.
- Dependencies: `charmbracelet/bubbletea`, `bubbles`, `huh`, `lipgloss`, `alecthomas/kong`, `fsnotify`, `NVIDIA/go-nvml`, `shirou/gopsutil/v3`.
- Runtime env overrides: `LLAMAMAN_ANIM_FPS` (wordmark animation FPS), `LLAMA_CACHE` / `HF_HUB_CACHE` (HF cache root, see `internal/storage`).

## Testing & QA
- Framework: standard Go `testing` package, co-located `*_test.go`.
- Patterns: table-driven tests, `t.TempDir`, `t.Setenv`, stub spawners, `drainCmds` harness for Bubble Tea snapshot tests.
- Coverage: unit tests for config, flags, translate, paths, server session/spawn, llamaapi, modelsini, hf, hwinfo, storage, tui snapshots. Integration tests use `cmd/llamaman-fakeserver`; skipped via `t.Skipf` when binary absent.
- Run order: build fakeserver first if needed, then `go vet ./...` then `go test ./...`.
- No external test framework, no explicit coverage thresholds.
- **Trap:** DESIGN.md still mentions `teatest`, but it is NOT in go.mod — TUI snapshot tests render in-process via a stub spawner + `drainCmds` (see `internal/tui/snapshot_test.go`). Don't add teatest.
