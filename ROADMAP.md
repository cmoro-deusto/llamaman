# llamaman — Roadmap

**Status:** agreed work document (August 2026). This file is the *agreement*: scope,
decisions, constraints, risks, and sequencing for the next releases.
**Design capture:** `DESIGN.md` §14 holds the same roadmap in condensed,
decision-focused form. Per project convention, update `DESIGN.md` *before*
implementing any item; `ROADMAP.md` is the working copy.

All items below were decided in conversation with the project owner. Every
explicitly rejected or deferred option is recorded in §7 so it is not
re-proposed by accident.

---

## 1. Guiding principles

This is the contract every release must respect. Sharpened in an owner
interview (August 2026) — each principle is a decision, not prose.

- **P1 — Visual contract.** Every visual feature defines behavior at each
  terminal capability level: truecolor, 256-color, 8-color (degrades to
  bold/dim), and NO_COLOR (honored automatically via lipgloss/termenv).
  Palette hexes must map to good 256-color indices (existing DESIGN §10.4
  discipline). Palettes declare a background mode; the theme picker offers
  both variants of every family (owner decision), and an incompatible
  choice warns and **applies** — only an unknown value falls back to
  `auto` (P3).
- **P2 — Additive v1 schema with trigger + field-arrival contract.** Config
  stays additive `version: 1`; older binaries reject new fields with
  `json: unknown field` (existing behavior). Additive v1 ends only when a
  change requires renaming, re-semanticizing, restructuring, or rejecting
  previously-valid configs — then DESIGN §12.1 migration machinery is
  mandatory, nothing hand-rolled. Every new config field ships with: JSON
  tag, validation rule, editor support (config or Settings mode), and a
  documented default — in the same change.
- **P3 — Severity taxonomy.** Every condition is Info, Warning, or Block.
  Block only when the launch or the requested operation cannot proceed —
  documented exit codes: 2 config, 3 prereq, 4 port-in-use. The TUI offers a
  recovery path whenever one exists (e.g. next free port); the CLI exits with
  the documented code. Unknown/invalid values degrade to a sensible default
  plus a warning.
- **P4 — Detach/reattach contract.** (1) Reattach restores the session view
  as if never detached. (2) The spawned-process contract (`setsid`, argv,
  `session.json`, log path — DESIGN §5) is stable; no feature may require the
  server to share the TUI's lifetime. (3) `session.json` evolves additively;
  old session files always load.
- **P5 — Scoped design-first.** Features and user-visible behavior changes
  get a DESIGN.md note in the same change (lightweight, a few lines suffice).
  Bug fixes that restore documented behavior skip design; a bug fix that
  contradicts DESIGN.md updates the doc in the same PR.
- **P6 — llama.cpp minimum-version contract.** llamaman documents a minimum
  supported llama.cpp build (the `--models-preset` era). Below the floor:
  single-model use stays tolerant (warn, not block); version-dependent
  features (router) keep hard gates with clear errors (existing behavior).
  CI tests span floor → latest. Parsers of llama.cpp output are tolerant of
  format drift and degrade gracefully — never crash on unknown output.
- **P7 — Network & privacy.** llamaman makes network requests only for
  explicit user actions (search, metadata fetch, download, validation), only
  to user-specified hosts (huggingface.co). No telemetry, no analytics, no
  implicit update checks.
- **P8 — Single source of truth.** config.json is the only definition of
  models, presets, globals, and preferences. llamaman mutates it only through
  the config editor, the Settings mode, explicit CLI actions, or documented
  derivations (`models.ini`). File-system state is derived and never silently
  reconciled — disagreement warns.
- **P9 — Determinism & testability.** UI behavior is testable in-process via
  snapshot tests with injectable clock, fetcher, and spawner. No feature may
  require a real llama-server or a real terminal for its unit tests
  (integration tests use `cmd/llamaman-fakeserver`).
- **P10 — User control.** Anything that can cost performance or battery
  (animations, polling-heavy views) or changes runtime behavior
  (auto-restart) is user-toggleable via the Settings mode, backed by the
  `preferences` object. Animations default **on** (subtle, transitional-only,
  ≤ 60 fps, gated by `preferences.animations`, and can be turned off.

**Consequential decisions recorded with the interview** (owned by the
owner, so they are not relitigated):

- **`preferences` is a new top-level config object** (`theme`, `animations`,
  later `models-dir`, auto-restart), separate from `globals`. `globals`
  stays launch-param-only (host, port, binary, models-files); preferences
  are user preferences of a different nature. Additive v1, so older binaries
  reject them with `unknown field` (accepted contract).
- **Settings mode** — a dedicated TUI mode (new Bubble Tea mode under Root),
  reachable from Main, editing exactly the `preferences` object. Quick keys
  (theme cycle in Main, animation toggle in run mode) write the same
  preferences; they are shortcuts, not a second source of truth.

---

## 2. Release 1 — "Polish" (Theme 4)

**Goal:** make llamaman *feel* finished. Self-contained, high-visibility,
no new llama-server interaction.

### 2.1 Multi-palette theme system

- **What:** the hard-coded light/dark pair in `internal/tui/common.go`
  (`Theme`, `CurrentTheme()`) becomes a palette table (owner-approved
  set, DESIGN §15.1): the original theme named `llamaman` (adaptive),
  plus 11 dark palettes (Catppuccin Mocha, Tokyo Night, Dracula,
  Gruvbox Dark, Solarized Dark, Nord, One Dark, Kanagawa, Monokai,
  Rosé Pine, Night Owl) with their official light counterparts
  (Catppuccin Latte, Tokyo Night Day, Dracula Light/Alucard, Gruvbox
  Light, Solarized Light, Nord Light, One Dark Light, Kanagawa Lotus,
  Monokai Light, Rosé Pine Dawn, Light Owl) plus the `auto` default
  value (resolves to `llamaman`).
- **Config:** new additive v1 field `preferences.theme` (string). Unknown
  value → warning + fall back to `auto` (non-blocking). See §2.6 for the
  `preferences` object and the Settings mode.
- **TUI:** the Settings mode (§2.6) offers the palette picker; `t` /
  `shift+t` in Main mode cycles palettes live (backward/forward). Both
  write `preferences.theme` — the quick key is a shortcut, not a
  second source of truth.
- **Constraints:** every palette keeps the existing named-color mapping so
  256-color SSH renders correctly; snapshot tests assert specific colors.
- **Non-goal:** user-defined arbitrary colors (no color picker in v1).

### 2.2 Log & status readability

- **What:** the run-mode log viewport colorizes llama-server output by line
  kind: ERROR (red), WARN (yellow), TIMING (dim), INFO (default), and
  highlights the readiness marker (`listening on`).
- **What else:** unicode status glyphs (e.g. `●`/`◌` dots, ✓ markers) and a
  terminal-title update via OSC escape (`llamaman — <alias> [READY]`).
- **Constraints:** colorizing is render-time only; search, jump-to-match,
  scrollback positions, and the denoise toggle are unaffected; lines stay
  byte-identical on disk (full log is unchanged).
- **Risk:** line-kind heuristics misclassify; mitigation = conservative
  regexes (worst case = uncolored line, never wrong color on a critical line
  misread as INFO).

### 2.3 Load-progress indicator

- **What:** while a model loads, show a live phase/progress line parsed from
  llama-server stderr: `loading model file` → `offloading N layers to GPU` →
  HF download progress (when applicable) → `listening`.
- **Hard constraint (owner decision):** this indicator is **separate** from
  the `[STARTING]` badge. No spinner or progress element replaces
  `[STARTING]`; the badge stays exactly as-is.
- **Design:** tolerant line classifiers; unknown phase → static text
  ("loading…"); HF download percentage lines map to the same progress bar.
  If parsing yields nothing, the UI is identical to today.
- **Risk:** llama.cpp log formats change between builds. Mitigation: version
  gates are not used — classifiers simply stop matching and the feature
  degrades to today's behavior. Revisit with new llama.cpp releases.

### 2.4 Subtle color animation

- **Scope (owner decision):**
  1. Load-progress line: animated fill + slow breathing color (only while
     loading).
  2. `[STARTING]` badge: slow color breathing (yellow ↔ gold). Text unchanged.
  3. Status dot in run mode: pulses while a request is generating.
  4. Main-screen + run-header wordmark highlight sweep (§15.5a): a
     specular band
     travels the llamaman logo bottom-left → top-right once per visit
     (`preferences.logo-effect: "once"`, the default) or continuously
     (`"loop"`, also on the run header while a session runs);
     `preferences.animations` off disables it entirely.
     **No** animation in steady-state READY idle (except the loop-mode
     sweep, owner-approved); **no** desktop
     notifications.
- **Mechanics:** `tea.Tick` at 60 fps (owner decision; runtime
  override via `LLAMAMAN_ANIM_FPS`); sine-wave color interpolation between
  two colors. True-color terminals get smooth interpolation; 256-color
  terminals get a 6-step discrete fallback (P1, owner-amended from
  2–3).
- **User control (P10):** gated by `preferences.animations` (default **on**);
  the Settings mode (§2.6) toggles it, and a run-mode quick key flips it
  live. Determinism: snapshot tests freeze a fake clock so rendered frames
  stay stable (P9).
- **Cost note (accepted):** animation forces periodic re-renders only while in
  a transitional/generating state — idle Main/run views stay static.

### 2.5 Main-mode layout rework

- Implement DESIGN.md §12.2 as designed: per-row source kind (local vs HF),
  sticky detached-session header with attach affordance, preset preview on
  highlight, better wide-terminal use. Pure presentation; keybindings
  unchanged; `?` help overlay remains canonical.

### 2.6 Settings mode & `preferences` object

- **What:** a new TUI mode (owned by Root, like Main/Run/Config) reachable
  from Main, editing exactly the top-level `preferences` config object.
  Release 1 fields: `theme`, `animations`. Later releases add
  `models-dir` (R2) and the auto-restart toggle (R3).
- **Why separate from `globals`:** `globals` is launch-time parameters every
  preset needs (host, port, binary, models-files); preferences are
  user-preference of a different nature. Mixing them corrupts `globals`'
  meaning (owner decision, recorded in §1).
- **Design:** a `huh` form like the globals form; changes write
  `preferences` and save via the standard atomic save path (DESIGN §3.4).
  Quick keys (`t`/`shift+t` theme cycle in Main, animation toggle in run
  mode — the latter lands with item 5) write the same object — shortcuts
  only.
- **First-run:** no preferences step in first-run setup; defaults apply.

### 2.7 Suggested order within Release 1

1. Theme system + Settings mode with `preferences` object (foundation for
   every other color change and the toggle)
2. §12.2 layout rework (consumes theme)
3. Log & status readability
4. Load-progress indicator
5. Animation (last — depends on 3 and 4's visual anchors; gated by
   `preferences.animations`)

---

## 3. Release 2 — "Storage Manager" (Theme 2)

**Goal:** remove the biggest setup friction — getting model files onto disk
with confidence and managing them.

### 3.1 Hybrid storage foundation (do first)

- **Decision (owner):** managed downloads write into **llama.cpp's HF cache
  layout by default**, with an optional `preferences.models-dir` override.
- Cache layout to match (verified in llama.cpp `common/hf-cache.cpp`;
  amended per DESIGN §16.1, owner-confirmed):
  - `$LLAMA_CACHE`, else `$HF_HUB_CACHE`, else `$HUGGINGFACE_HUB_CACHE`,
    else `$HF_HOME/hub`, else `$XDG_CACHE_HOME/huggingface/hub`, else
    `~/.cache/huggingface/hub` (first set wins).
  - Repo folder form: `models--<org>--<model>/<file>` via
    `repo_to_folder_name` (HF hub layout). The legacy llama.cpp layout
    (`~/.cache/llama.cpp`, `<org>__<model>` folders / flat
    `<org>__<repo>__<file>` files) is tolerated for reads.
  - **Amendment:** the earlier wording (`~/.cache/llama.cpp` chain,
    `<org>__<model>` folders) described llama.cpp before PR #20775, which
    switched llama.cpp to the standard HF hub layout. The verified current
    layout above is the default; the legacy forms are read-only.
- **Why:** one copy on disk shared with `llama-cli` and `--hf-repo`; the
  router's `(cache)` tags in run mode line up with managed downloads.
- **Risk (accepted):** llama.cpp may change cache layout in a release; the
  reader tolerates both known layouts and warns on unrecognized ones.

### 3.2 Launch path: delegated downloads (amended — owner decision C)

- **Decision (owner):** `hf` models stay **fire-and-forget `--hf-repo`**.
  llama.cpp downloads at server start, cache-first (verified:
  `get_repo_files` API list → `get_cached_files` fallback), with `Range`
  resume, blob dedup by sha256 oid, `mmproj` sidecars, and gated-repo tokens
  (`HF_TOKEN` env / `--hf-token`). Live progress is already visible in the
  run-mode panel via the §15.4 tolerant classifier (llama.cpp's
  `downloading … %` lines).
- **No managed download on the launch path** — no `--model <cached path>`
  takeover, no download overlay in run mode. Cancelling a startup download =
  kill the server; resuming = relaunch (Range makes it cheap).
- **HF API surface** (for implementation; consumed by the §3.4 manager's
  download action, the quant picker, and the browser):
  - File list + sizes + LFS sha256: `GET /api/models/{repo}/tree/main`
  - Repo metadata: `GET /api/models/{repo}`
  - Download: `GET https://huggingface.co/{repo}/resolve/main/{file}`
    (Range-supported)
- **Token:** gated repos via `HF_TOKEN` env — used by llama.cpp when
  delegating and by the manager's download action. A config token
  (`preferences.hf-token`) stays an open question for the §3.4 design note.

### 3.3 Quantization picker

- **What:** before download, choose the quant (Q4_K_M … Q8_0, plus available
  repo files) with **real file sizes** from the HF API; a `fits in VRAM` hint
  per quant (wired to §4.2's estimator — see synergies, §6).
- The chosen quant becomes the `location`/`hf` entry in config.

### 3.4 Storage & Downloads manager

- **What:** a TUI view (new mode from Main) — the **single place** where
  downloads are managed. Lists local models + HF-cache files with sizes, free
  disk space, in-flight download state, and delete-with-confirmation.
- **Download action (owner decision C):** "download now" pre-fetches
  `org/repo:quant` into the cache using the HF API client (§3.2 surface):
  pause/resume (`Range`), sha256 verification, clear failure messages. Launch
  stays delegated (§3.2); this action is prefetch and management only — the
  run-mode panel keeps just the passive §15.4 progress, no download UI there.
- Needs the cache-layout reader from §3.1; respects `preferences.models-dir`.
- Delete only removes files llamaman can account for (cached downloads,
  managed models); never deletes config entries without asking.

### 3.5 HF model browser

- **What:** search/browse Hugging Face inside the TUI: query, filter by
  quant/size, read metadata (params, context, license), and hand the picked
  repo+quant straight into the config or a download.
- **HF API:** search endpoint with library filter (`filter=gguf`).
- **Scope note:** this is the largest single item in the roadmap; if effort
  pressure appears, it can slip to the backlog (§4) — its consumers (config
  editor, downloader) ship without it. (Shipped in Release 2; note kept for
  history.)

### 3.6 Router-mode interaction (design note)

- llama.cpp's router downloads models **internally** (child processes fetch
  via the cache). Manager-only downloads (prefetch into the shared cache,
  §3.4) therefore apply to router and single-model runs alike; llama.cpp's own
  download progress is only *surfaced* (the §15.4 classifier and the manager's
  listing of in-flight state).
- Deferred decision (implementation time): rewrite router presets to point at
  locally managed files vs. leave router downloads to llama.cpp. Not a
  user-visible promise in this release.

### 3.7 Suggested order within Release 2

1. Hybrid storage + cache-layout reader
2. HF API client (shared by the manager's download action, quant picker,
   §3.8b repo check, browser)
3. Quantization picker
4. Model editor integration — step A: local GGUF picker + cached-repo
   list (§3.8; makes the config editor the reader's first consumer)
5. Storage & Downloads manager (its download action is the managed
   downloader)
6. Model editor integration — step B: typed-repo existence check + quant
   offer (§3.8)
7. HF model browser
8. Paste a llama-server command line (§3.9 — owner decision: **next**)

### 3.8 Model editor integration (owner decision)

**Goal.** Replace the free-type `location` / `hf` fields of the config
editor's model form (DESIGN §7.5) with picker-assisted flows, making the
editor a real consumer of the storage layer (§3.1) and the HF API (§3.2
surface). No new config fields — the schema is unchanged (P8).

**Step A — local file + cached-repo selection** (needs §3.1; lands right
after the reader):
- Local branch: a `bubbles/filepicker` overlay (bubbles v1.0.0, already
  a dependency) filtered to `*.gguf`, opening in `preferences.models-dir`
  when set, else the last-used model directory, else `~`. A hotkey in the
  location input opens it; the free-type input stays as the fallback.
- HF branch: on opening, build a `huh.Select` from the cache reader
  (`storage.Scan`, grouped by repo): one option per cached repo, showing
  its cached quants + sizes. Selecting one pre-fills `org/repo[:quant]`
  (quant empty when the repo has several). A "type a new repo…" option is
  always present; an empty cache skips the list.
- Reuses the `paramPicker` overlay pattern (a custom picker outside huh,
  driven by a done message) already in config mode.

**Step B — typed-repo validation + quant offer** (needs items 2 + 3):
- After the user confirms a typed repo id, one async `tree/main` call
  (existence + quant list + sizes + LFS sha256 in a single round-trip).
- On success: offer the quant chooser (shared with §3.3) with real sizes,
  plus the "fits VRAM" hint when the §14.3 estimator exists (sizes-only
  before then). `mmproj` presence is informational only — llama.cpp
  auto-downloads it; presets already have `no-mmproj`.
- Failures non-blocking (P3): distinct messages for not-found vs gated
  (401) vs network; the id can still be saved (llama-server surfaces it
  at launch).

### 3.9 Paste a llama-server command line (owner decision — next)

**What:** in the config editor's Models pane, key **`p`** opens a paste box;
the pasted llama-server command line (the `llama-server` binary name
optional — bare flag lists allowed) is tokenized, validated against the
live `flags.Registry`, and committed — through a **confirm step** (P8) — as
a model + preset, as a preset on an existing entry, or (no model flag
present) as a preset on a model from a selector. The argv-text sibling of
the `modelsini` import (§13); never executes anything.

- **Parsing (`internal/cmdline`):** POSIX-ish tokenizer (whitespace split,
  `'`/`"` quoting, backslash escapes, `--flag=value` / `-m=value`); **no**
  `$VAR`/`~`/glob expansion — values stored literally (the model `Location`
  is expanded later by the config loader; other params pass to llama-server
  literally).
- **Validation vs. the registry** (live `--help` cache, fallback set when
  the binary is missing): **errors** (block) — value-flag missing its
  value, empty `--flag=`, non-numeric value for a known numeric flag,
  `-m`+`-hf` together, same source repeated. **Warnings** (import
  proceeds) — unknown flags (with a "validated against built-in flag set"
  note when the fallback is in use), repeated flag overwritten (last wins),
  `-m` file not found.
- **Model source:** `-m/--model` XOR `-hf/--hf-repo`; `--alias` becomes the
  new model's alias (kept in the preset on the preset-only path). All other
  flags — incl. `--host`/`--port` — become preset params (preset overrides
  win at launch).
- **Outcomes:** new model (alias derived `--alias` > file basename > repo
  name, editable, uniquified); existing model (exact match on expanded
  `Location` / full `org/repo[:quant]` — a different quant is a different
  model) → preset only; no model flag → selector (existing models + "＋
  create new model…"). A preset is **always** created (name field, default
  `"pasted"`, uniquified). Bare `-hf org/repo` chains the §16.6 quant
  chooser before commit.
- **No config-schema changes** (additive v1, P8).

**Design note:** DESIGN §16.8. **File map:** new `internal/cmdline/`
(tokenize + parse + tests), `internal/tui/config.go` (Models-pane key `p`,
new `formKind`, `applyForm` branch), modelpicker overlay reuse.

---

## 4. Backlog — potential items (formerly "Release 3 — Trust & Touch")

**Owner decision (August 2026):** Release 3 is **not** a committed release.
Nothing below is final for implementation — these items are potential scope,
kept in the roadmap so they are not lost. Picking any of them up requires a
fresh owner decision and, per §9, a design note before code. (The current
committed release is Release 2 — §3 — extended by §3.9.)

**Goal if ever picked up:** make llamaman trustworthy in daily use (it
survives crashes, warns before bad launches) and *actionable* (it can drive
a running server).

### 4.1 Crash diagnostics & auto-restart

- **What:** when the server process dies, show a crash view: exit code with
  interpretation (e.g. 137 = OOM-kill, 130 = SIGINT), log tail, and a
  restart action. Optional auto-restart with exponential backoff (1s → 2s →
  4s → … capped, max attempts) **while llamaman is attached**.
- **Default (owner):** auto-restart is **off** — opt-in per launch or config.
- **Explicitly out of scope:** watching a detached server (requires a daemon
  mode; not planned — see §7).
- **Design note:** a server that exits while llamaman is detached is detected
  at next attach/launch (session.json still present, PID dead → clear
  "previous session died" state instead of silent reattach).

### 4.2 VRAM preflight

- **What:** before launch, estimate footprint = model file size × offloaded
  fraction + KV cache + compute buffers, compared against GPU VRAM (NVML is
  already in `internal/hwinfo`). Warn (non-blocking) instead of letting
  llama-server CUDA-OOM.
- **Honesty:** it is a rough estimator (±20%), not llama.cpp's `--fit`.
  Show it as an estimate; precise tuning remains `--fit`'s job.
- **No GPU / no NVML:** skip silently (current behavior).
- **Synergy:** the same estimator powers the quant picker's "fits VRAM" hints
  (§3.3).

### 4.3 Pre-spawn checks

- **Port in use:** probe before spawn; on conflict, offer the next free port
  (never silently change the port). Existing exit code 4 path stays for
  non-interactive CLI.
- **Free disk:** before a download, check space against the target file size
  (via the storage layer, §3.1).
- **HF repo validation:** before delegation or download, verify the repo
  exists via the HF API; replace llama-server's opaque failure with a clear
  message.

### 4.4 Quick test prompt

- **What:** one key in run mode → POST a small prompt to
  `/v1/chat/completions` → overlay shows TTFT and first tokens. Answers "is
  it actually working?" instantly.
- Uses the existing `llamaapi` client; failures surface as an overlay error,
  not a crash.

### 4.5 KV-cache pause/resume

- **What:** save a live conversation's KV cache (`POST /slots/:id?action=save`
  with `--slot-save-path`) from the TUI and restore it later — a pause/resume
  for server state.
- **Design note:** `--slot-save-path` flows through preset params (it is a
  normal flag); the TUI gains save/restore actions and a saved-slots list.
  Default save location decided at implementation (e.g.
  `~/.local/share/llamaman/kv/` or `$XDG_RUNTIME_DIR`).
- **Known llama.cpp bug to design around:** KV save fails for vision models
  (llama.cpp #19466) — disable the action for multimodal presets with a
  note.

### 4.6 Web-UI shortcut

- **What:** a key in run mode opens llama-server's built-in web UI in the
  browser (`xdg-open http://<host>:<port>/`).
- **Failure mode:** no `xdg-open` → warning line, no crash.

### 4.7 Backlog order (if ever picked up)

1. Quick test prompt + Web-UI shortcut (small, immediate value)
2. Crash diagnostics → auto-restart
3. Pre-spawn checks
4. VRAM preflight
5. KV-cache pause/resume

---

## 5. Expected config & code footprint

All additive `version: 1` fields:

- new top-level `preferences` object (per P2/P10, §2.6):
  - `preferences.theme` (string, Release 1)
  - `preferences.animations` (bool, default true, Release 1)
  - `preferences.models-dir` (string, Release 2)
  - auto-restart toggle (backlog §4.1; exact shape TBD — `preferences`
    field and/or CLI flag)

Expected new packages/modes (suggested, not committed):

- `internal/hf/` — HF API client + downloader (reused by browser/quant
  picker/preflight)
- storage/cache-layout reader (inside `internal/hf/` or `internal/storage/`)
- `internal/cmdline/` — llama-server argv tokenizer/parser for the §3.9
  paste feature
- New TUI surfaces: Settings mode (§2.6), palette cycle (Main mode),
  load-progress line (run mode), model-editor pickers (GGUF file picker,
  cached-repo list, quant offer — §3.8), storage & downloads manager
  view, HF browser mode, paste box (config editor, §3.9), crash view,
  saved-slots list

---

## 6. Synergies

- **VRAM estimator** (§4.2) powers the quant picker's "fits VRAM" hints
  (§3.3) — build the estimator once, in whichever release ships first.
- **Load-progress parsing** (Release 1) is the same tolerant classifier the
  download-progress line needs in Release 2 — keep the classifier generic.
- **HF API client** (Release 2) is reused by the browser, quant picker,
  downloader, and repo-validation check (§4.3).
- **Storage layer** (§3.1) serves the storage manager, disk-space preflight,
  and download target resolution.
- **Theme system** (Release 1) must land before §12.2 layout rework so the
  rework can consume palette tokens — both are in Release 1 (§2.7).

---

## 7. Explicitly deferred (do not re-propose without new information)

| Item | Reason |
|------|--------|
| Desktop notifications | Owner decision; not in these releases. |
| Gradient wordmark | lipgloss v1.1.0 has no gradient API; pure decoration. |
| Wordmark breathing animation | Owner decision; steady-state stays static. |
| LoRA hot-swap (`/lora-adapters`) | Largest run-depth item; not selected. |
| Cancel in-flight generation | Not selected. |
| Restart with edited params ("live editing") | Stays out of scope (DESIGN §11). |
| Config schema migrations (§12.1) | No breaking schema change planned; additive v1 covers the roadmap. |
| Health watchdog (process alive, HTTP hung) | Overlaps §4.1; not selected. |
| Daemon mode / detached watchdog | Requires a persistent daemon; not planned. |
| Multiple concurrent sessions | Unchanged from DESIGN §11. |

---

## 8. Risks & watch items

| Item | Impact | Mitigation |
|------|--------|------------|
| llama.cpp default port moving 8080 → 9931 (PR #26508) | Users following llama.cpp docs collide with llamaman's 9080 default only if they override | Keep 9080; document; revisit on llama.cpp release. |
| Router mode is experimental-grade upstream | Router features (download/load/unload APIs) may change | Version-gate router features; tolerate endpoint drift. |
| llama.cpp stderr format drift | Load/download progress parsing breaks | Tolerant classifiers; degrade to static UI; no version gates needed (§2.3). |
| llama.cpp cache layout change | Storage manager misreads cache | Support both known layouts; warn on unrecognized. |
| KV save fails for vision models (#19466) | Pause/resume broken for multimodal | Disable action for multimodal presets (§4.5). |
| HF API rate limits / auth on search | Browser throttled or gated | Cache search results; token support for gated repos. |
| 256-color degradation of themes/animations | SSH users see broken colors | Palette + animation fallbacks mandatory (principle §1). |
| NVML/CGO unavailable | VRAM preflight silent | Skip gracefully, current behavior. |

---

## 9. Working process (owner-agreed, August 2026)

How Release 1 (and subsequent releases) are built:

- **Branching:** one release branch per release, named `release/<n>-<theme>`
  (e.g. `release/1-polish`). Work happens on the branch; **never push** —
  pushing, PR, and merging to `main` are the owner's call.
- **Commits:** conventional messages with scopes (`feat(tui): …`,
  `docs(design): …`), one logical set per item; substantial designs get a
  `docs(design)` commit before the `feat` commit. Commit granularity is the
  implementer's call.
- **Per work unit (one item):**
  1. Design note in DESIGN.md §15 (one subsection per item).
  2. Code + unit tests + snapshot updates.
  3. Verify: build `bin/llamaman-fakeserver` (CGO off), `go vet ./...`,
     `go test ./...` — all green.
  4. Commit (never push).
  5. Summarize all work done and suggest what the owner can try/validate.
     The owner runs a validation pass (manual smoke test against a real
     `llama-server`, review, whatever they choose) and may come back with
     issues or suggestions. Iterate until the owner declares the work unit
     validated. Only then start the next item.
- **Item order** (Release 1, ROADMAP §2.7): theme system + Settings mode →
  §12.2 layout rework → log readability → load-progress → animation.
