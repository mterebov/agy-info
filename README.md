**English** | [Русский](./README.ru.md)

# AGY Info

A lightweight, 100% local MCP App plugin for **Google Antigravity** that gives you a live dashboard for your current agent session: context window utilization, active subagents, live execution plan, and token activity chart — all at ~0% CPU and zero LLM token cost.

Works natively in both the **right side pane (`aux_pane`)** and the **left sidebar (`global`)**.

## Features

- **Unified Context Window Meter (`Window` / `Breakdown`)**
  - Real-time context window fill (`current / window`, percentage, free headroom) with color-coded `/compact` advice.
  - Peak usage and compaction (`CHECKPOINT`) markers right on the meter, plus prompt cache hit rate.
  - Toggle between **Window** view and **Breakdown** view (baseline system/rules/summary, tool outputs, assistant & thinking, user & subagents).
- **Live Subagents Inspector**
  - Tracks every subagent launched in the session with live status (`running`, `done`, `idle`, `stopped`), model, token fill, step/tool counters, and current `update_focus` or active tool name.
  - Click any subagent to drill down into its own context/plan view (and jump back to the parent session), or open its transcript log directly.
- **Active Plan Checklist & Focus**
  - Displays the live `update_steps` checklist and current `update_focus` badge.
  - Automatically falls back to root markdown checklists (`task.md`, `implementation_plan.md`, `plan.md`) with a quick link to open the artifact.
- **Activity & Token Growth Graph**
  - Dedicated view with an interactive step-by-step token timeline highlighting compaction boundaries, cumulative billed token totals, cache efficiency, and session counters.
- **1-Second Live Polling at ~0% CPU**
  - File `mtime`/`size` caching, 5-second directory scan TTL, and revision hashing return a ~159-byte `unchanged: true` tick whenever nothing moved.
  - Automatically pauses polling when the panel tab is hidden.
- **Built-in EN / RU Localization**
  - Instant UI language switcher between English and Russian.
- **100% Local & Private**
  - Single-file bundled UI (`assets/app.html`) with zero external network calls, zero telemetry, zero external Go dependencies, and zero LLM tokens spent on monitoring.

## Quick Installation

### Option 1: Clone directly into Antigravity's plugins folder (fastest)

```bash
git clone https://github.com/mterebov/agy-info.git ~/.gemini/config/plugins/agy-info
```

On first launch, `scripts/run.sh` automatically compiles `bin/agy-info` using Go 1.22+ (no extra dependencies), or downloads a prebuilt binary from [GitHub Releases](https://github.com/mterebov/agy-info/releases) if Go is not installed.

### Option 2: Clone anywhere and run the installer

```bash
git clone https://github.com/mterebov/agy-info.git
cd agy-info
./scripts/install.sh
```

For active local development, install as a symlink instead of copying:

```bash
./scripts/install.sh --symlink
```

### Opening AGY Info in Antigravity

1. Enable **AGY Info** in Antigravity's **Plugins Manager** (or reload Antigravity).
2. Open the panel via the **`+`** button in the right side pane or from the left sidebar under **AGY Info**.
3. You can also ask the agent directly: *"Open AGY Info for the current session"*.

> **No Go installed?** `scripts/run.sh` automatically downloads the matching binary from [GitHub Releases](https://github.com/mterebov/agy-info/releases) on first start, or you can place it manually at `~/.gemini/config/plugins/agy-info/bin/agy-info`.

## Development & Building

Requirements: **Go 1.22+** (server) and **Node.js 20+** (UI).

```bash
make build          # Compile Go MCP server -> bin/agy-info
make test           # Run Go unit & race-detector tests
make vet            # Run go vet
make ui             # Build single-file UI bundle -> assets/app.html
make install        # Install plugin into ~/.gemini/config/plugins/agy-info
make release-bins   # Cross-compile for macOS, Linux, and Windows -> dist/
make clean          # Remove bin/ and dist/
```

## Architecture

```text
Antigravity Host (aux_pane / global sidebar)
   │  stdio JSON-RPC 2.0 (MCP)
   ▼
scripts/run.sh ──exec──▶ bin/agy-info (Go stdlib MCP server)
   ├── Reads ~/.gemini/antigravity/brain/<conversation-id>/
   │     ├── .system_generated/logs/transcript[_full].jsonl
   │     └── task.md / implementation_plan.md / plan.md
   ├── Optional live run-state lookup via local LS (when env vars are set)
   ├── Exposes MCP tools:
   │     ├── open_agy_info          (UI entrypoint, always full snapshot)
   │     └── get_context_snapshot   (app-only polling, unchanged short-circuit)
   └── Serves UI resource:
         └── ui://agy-info/app.html ──▶ assets/app.html (self-contained MCP App)
```

## License

[MIT](./LICENSE) © 2026 Maksim Terebov
