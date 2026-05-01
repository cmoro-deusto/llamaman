# llamaman

A modern TUI manager for `llama-server` (llama.cpp). Define models and presets once in a JSON config; pick and launch them from a Bubble Tea interface, with a tailing log view and detach/reattach across invocations.

## Status

v0.1.0 — first release. Linux only.

## Install

### Pre-built binary (GitHub Releases)

```sh
curl -L https://github.com/cmoro-deusto/llamaman/releases/latest/download/llamaman_linux_amd64.tar.gz \
  | tar -xz -C /tmp
sudo install -m 0755 /tmp/llamaman /usr/local/bin/llamaman
```

### From source

```sh
go install github.com/cmoro-deusto/llamaman@latest
```

### Arch / CachyOS

```sh
yay -S llamaman-bin
```

PKGBUILD source lives at `packaging/aur/PKGBUILD`.

(Depends on either Wayland's `wl-clipboard` or X11's `xclip` for the "copy command" shortcut.)

### Shell completion

```sh
llamaman --completion bash > ~/.local/share/bash-completion/completions/llamaman
llamaman --completion zsh  > ~/.zsh/completions/_llamaman
llamaman --completion fish > ~/.config/fish/completions/llamaman.fish
```

## Quick start

```sh
llamaman                # main mode (centered launcher)
llamaman <alias>        # launch a model with its default preset
llamaman <alias> <pre>  # launch with a named preset
llamaman -l             # list configured models
llamaman -p <alias>     # list presets for a model
```

The first invocation with no config triggers a setup flow that writes `~/.config/llamaman/config.json` with autodetected defaults.

## TUI keys

**Main mode** — `s`/`Enter` selection · `c` configure · `a` attach to running session · `?` help · `q` quit

**Selection mode** — `↑↓` navigate · `Enter` run · `e` edit model · `n` new model · `d` delete model · `/` filter · `Esc` back. Multi-preset models open a sub-list with the same keys.

**Run mode** —
- `q`/`Ctrl+C` quit prompt (`(k)ill` returns to main, `(d)etach` exits llamaman, `(c)ancel`)
- `k` kill server (with confirm) and return to main without exiting llamaman
- `r` restart server (confirm if status is `ready`)
- `c` copy launch command to clipboard (`wl-copy` → `xclip` fallback)
- `/` search forward · `n`/`N` next/prev match
- `g`/`G` top/bottom · `space`/`b` page down/up · `↑`/`↓` scroll one line
- `?` help overlay

**Configuration mode** — `Tab`/`Shift+Tab` (or `←`/`→`, `h`/`l`) cycle panes · `↑`/`↓` navigate within a pane · `e` edit · `n` new · `d` delete · `D` duplicate (presets) · `Shift+↑/↓` reorder · `g` globals · `s` save · `Esc` back. The new-param picker shows each flag's bare name + parsed help description; just start typing to filter.

## Config layout

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
            "jinja": true
          }
        }
      ]
    },
    {
      "alias": "qwen-hf",
      "hf": "Qwen/Qwen3-32B-GGUF:Q4_K_M",
      "presets": [{ "preset": "default", "description": "", "params": { "ngl": 99, "fa": "on" } }]
    }
  ]
}
```

A model has **exactly one** of `location` (path to a local `.gguf` file) or `hf` (a Hugging Face identifier in `org/repo[:quant]` form). Local paths are expanded for `~` and `$VAR` at load time; HF identifiers are passed verbatim to llama-server's `-hf`, which downloads and caches the model on first launch.

Param iteration order is preserved on disk and in the resulting argv, so you can group related flags however you like. Numeric values stay verbatim (`0.0` won't become `0`).

## Files

| Purpose | Path |
|---|---|
| Config | `${XDG_CONFIG_HOME:-~/.config}/llamaman/config.json` |
| Live session record | `${XDG_RUNTIME_DIR:-/tmp/llamaman-$UID}/llamaman/session.json` |
| llama-server log (current session) | `${XDG_RUNTIME_DIR:-…}/llamaman/llama-server.log` |
| Flag-name cache | `${XDG_CACHE_HOME:-~/.cache}/llamaman/flags-<mtime>.json` |
| Debug log | `${XDG_STATE_HOME:-~/.local/state}/llamaman/llamaman.log` |

Set `LLAMAMAN_DEBUG=1` to raise the debug-log level to DEBUG.

## License

MIT. See `LICENSE`.
