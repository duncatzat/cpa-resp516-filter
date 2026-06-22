# CPA RespFilter

> A [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) C ABI plugin that detects and filters OpenAI reasoning degradation — the **"516 problem"** — through a fully configurable, hot-reloadable rule engine.

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go 1.21+](https://img.shields.io/badge/Go-1.21+-00ADD8.svg)](https://go.dev/)
[![Platform: Linux · macOS · Windows](https://img.shields.io/badge/Platform-Linux·macOS·Windows-blue.svg)](#build-from-source)
[![中文文档](https://img.shields.io/badge/文档-中文-red.svg)](README_CN.md)
[![Release](https://img.shields.io/github/v/release/duncatzat/cpa-resp516-filter?display_name=tag&sort=semver)](https://github.com/duncatzat/cpa-resp516-filter/releases)

---

## Table of Contents

- [Overview](#overview)
- [The 516 Problem](#the-516-problem)
- [Architecture](#architecture)
- [Features](#features)
- [Quick Start (Ubuntu)](#quick-start-ubuntu)
- [Build from Source](#build-from-source)
- [Configuration](#configuration)
- [Management API](#management-api)
- [How It Works](#how-it-works)
- [Troubleshooting](#troubleshooting)
- [File Structure](#file-structure)
- [License](#license)

---

## Overview

`CPA RespFilter` is a standard dynamic library plugin for CLIProxyAPI. It inspects model responses (both streaming and non-streaming) for evidence of silent reasoning degradation, and takes configurable action when degradation is detected — replacing the response with an error, retrying the request, or simply logging the event.

The plugin requires **no CLIProxyAPI SDK dependency** — it implements the C ABI directly and ships its own type definitions, making it lightweight and version-stable across CPA releases.

---

## The 516 Problem

When OpenAI silently degrades requests from third-party clients (CLIProxyAPI, OpenCode, Codex SDK, etc.), the response always reports a fixed `reasoning_tokens` value — commonly **516** — regardless of the requested reasoning effort (`low`/`medium`/`high`/`xhigh`).

**Observable symptoms:**

| Metric | Normal | Degraded |
|--------|--------|----------|
| `reasoning_tokens` | Varies by effort level | Fixed at 516 |
| Answer quality | Correct for complex reasoning | Noticeably worse |
| Token cost | Full | Still charged at full rate |
| UI thinking display | Visible reasoning summary | Summary present but truncated |

**Source:** [CLIProxyAPI Discussion #3937](https://github.com/router-for-me/CLIProxyAPI/discussions/3937)

This plugin turns that fixed signature into a detection signal: if `reasoning_tokens` matches a configured value (default 516), the response is treated as degraded and handled according to your rules.

---

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                     CLIProxyAPI Host                            │
│                                                                 │
│  Client Request ──► Auth Selection ──► Upstream Execution      │
│                                              │                  │
│                                              ▼                  │
│                                    ┌──────────────────┐         │
│                                    │  Response Pipeline│         │
│                                    │                  │         │
│  ┌────────────────────────────────┤  response.       │         │
│  │     RespFilter Plugin          │  normalize_after  ├─► Client│
│  │                                │                  │         │
│  │  ┌─────────┐  ┌─────────────┐ │  response.       │         │
│  │  │ Rules   │  │ Detectors   │ │  intercept_       │         │
│  │  │ (order) │──│ (operators) │ │  stream_chunk     │         │
│  │  └────┬────┘  └──────┬──────┘ │                  │         │
│  │       │              │        └──────────────────┘         │
│  │       ▼              │                                       │
│  │  ┌─────────┐         │              ┌──────────────┐        │
│  │  │ Action  │◄────────┘              │  usage.handle│        │
│  │  │ Engine  │                        │  (stats/log) │        │
│  │  └────┬────┘                        └──────────────┘        │
│  │       │                                                     │
│  │       ├── error ──► inject error response                   │
│  │       ├── retry ──► host.model.execute (re-entry guarded)   │
│  │       └── log   ──► host.log callback                       │
│  │                                                               │
│  │  Config: atomic.Pointer (hot-reload, lock-free reads)       │
│  └─────────────────────────────────────────────────────────────┘
│                                                                 │
│  Management API: /v0/resource/plugins/respfilter/status        │
└─────────────────────────────────────────────────────────────────┘
```

---

## Features

| Feature | Description |
|---------|-------------|
| **Multi-rule engine** | Define multiple named detection rules; first match wins |
| **7 match operators** | `exact`, `any_of`, `lte`, `gte`, `range`, `not_in`, `always` |
| **Configurable JSON paths** | gjson syntax — adapt to upstream API changes without a new plugin version |
| **Wildcard model filtering** | `gpt-5*`, `*-codex`, `*flash*`, etc. |
| **Templatable responses** | Go `text/template` for error bodies, stream errors, and log messages |
| **Three action modes** | `error` (inject error), `retry` (re-execute via host callback), `log` (observe only) |
| **Hot reload** | Config changes via `plugin.reconfigure` — no restart, no request blocking |
| **Stream optimization** | Keyword pre-filter skips SSE chunks without usage data |
| **Management API** | Browser-navigable `/status` endpoint with live rules and detection statistics |
| **Atomic config** | `atomic.Pointer` for lock-free reads; template precompilation at config time |
| **Zero SDK dependency** | Implements C ABI directly; no `go get` of CLIProxyAPI required |

---

## Quick Start (Ubuntu)

### Option A — Download pre-built binary (recommended)

Skip the build step entirely. Download the pre-compiled plugin from [GitHub Releases](https://github.com/duncatzat/cpa-resp516-filter/releases):

```bash
# Check your architecture
uname -m
# x86_64 → amd64    aarch64 → arm64

# Download the latest release for your platform
# Linux x86_64 (most servers, AVX2+):
wget https://github.com/duncatzat/cpa-resp516-filter/releases/latest/download/respfilter_linux_amd64-v3.tar.gz

# Linux x86_64 (older CPUs without AVX2):
wget https://github.com/duncatzat/cpa-resp516-filter/releases/latest/download/respfilter_linux_amd64.tar.gz

# Linux ARM64 (Raspberry Pi, Graviton, ARM servers):
wget https://github.com/duncatzat/cpa-resp516-filter/releases/latest/download/respfilter_linux_arm64.tar.gz

# Extract into your CLIProxyAPI installation
tar xzf respfilter_linux_amd64-v3.tar.gz -C /opt/cliproxyapi/

# The archive already contains the correct plugin directory structure:
#   plugins/linux/amd64-v3/respfilter.so
#   config.snippet.yaml
```

Then proceed to [Step 3 — Configure](#step-3--configure).

### Option B — Build from source

### Prerequisites

```bash
# Go 1.21+
sudo apt-get install -y golang-go

# C compiler (gcc) and build tools
sudo apt-get install -y build-essential

# Verify
go version
gcc --version
```

### Step 1 — Build the plugin

```bash
git clone <your-repo-url> respfilter
cd respfilter

# Build as a shared library for Linux
CGO_ENABLED=1 go build -buildmode=c-shared -o respfilter.so .
```

You should see `respfilter.so` (and a `respfilter.h` header you can delete):

```bash
ls -la respfilter.so
# -rwxr-xr-x 1 user user 6.6M ... respfilter.so

rm -f respfilter.h  # not needed at runtime
```

### Step 2 — Install into CLIProxyAPI

```bash
# Locate your CLIProxyAPI installation. Common paths:
#   /opt/cliproxyapi/        (manual install)
#   ~/cliproxyapi/           (home directory)
#   /usr/local/bin/          (binary only — you need the working directory)

# Create the plugin directory structure (CPA scans these in priority order):
mkdir -p /opt/cliproxyapi/plugins/linux/amd64

# Copy the plugin (use 'cp', NOT symlinks — CPA skips symlinks)
cp respfilter.so /opt/cliproxyapi/plugins/linux/amd64/

# Set permissions (dlopen requires read+execute)
chmod 755 /opt/cliproxyapi/plugins/linux/amd64/respfilter.so
chmod +x /opt/cliproxyapi/plugins /opt/cliproxyapi/plugins/linux /opt/cliproxyapi/plugins/linux/amd64
```

> **Directory priority:** CPA scans `plugins/linux/amd64-v3/` → `plugins/linux/amd64/` → `plugins/` (first match wins). Use the architecture-specific subfolder for multi-arch deployments, or `plugins/` directly for simplicity.

### Step 3 — Configure

Edit your CLIProxyAPI `config.yaml`:

```yaml
plugins:
  enabled: true                          # Master switch — REQUIRED
  configs:
    respfilter:                          # MUST match the filename (respfilter.so → "respfilter")
      enabled: true                      # Per-plugin switch — REQUIRED
      priority: 1
      # Minimal: uses built-in defaults (detect 516, action: error, all models)
```

See [Configuration](#configuration) for the full rule-based config.

### Step 4 — Start and verify

```bash
# Start CLIProxyAPI from its working directory (plugins/ is relative to CWD)
cd /opt/cliproxyapi
./cli-proxy-api --config config.yaml &

# Check logs for successful load
journalctl -u cliproxyapi -f 2>/dev/null | grep pluginhost
# or check stdout directly if running in foreground
```

Expected log output:

```
pluginhost: plugin loaded  plugin_id=respfilter path=plugins/linux/amd64/respfilter.so
```

**Verify via Management API** (requires `remote-management.secret-key` in config):

```bash
# List registered plugins
curl -s -H "Authorization: Bearer <your-secret-key>" \
  http://localhost:8317/v0/management/plugins | jq

# View respfilter status (rules + stats)
curl -s http://localhost:8317/v0/resource/plugins/respfilter/status | jq
```

### Step 5 — Test detection

Send a request that triggers degradation. When 516 is detected, you'll see:

- **`error` mode:** Client receives an HTTP 503 with a JSON error body
- **`retry` mode:** Plugin re-executes the request; logs show retry attempts
- **`log` mode:** Response passes through; log shows the detection event

Check detection statistics:

```bash
curl -s http://localhost:8317/v0/resource/plugins/respfilter/status | jq .stats
```

---

## Build from Source

### Linux (Ubuntu/Debian)

```bash
sudo apt-get install -y golang-go build-essential
CGO_ENABLED=1 go build -buildmode=c-shared -o respfilter.so .
rm -f respfilter.h
```

### Cross-compilation

```bash
# For a different architecture (e.g., arm64)
GOOS=linux GOARCH=arm64 CGO_ENABLED=1 CC=aarch64-linux-gnu-gcc \
  go build -buildmode=c-shared -o respfilter.so .

# For Windows from Linux (requires mingw-w64)
GOOS=windows GOARCH=amd64 CGO_ENABLED=1 CC=x86_64-w64-mingw32-gcc \
  go build -buildmode=c-shared -o respfilter.dll .

# For macOS from Linux (requires osxcross)
GOOS=darwin GOARCH=amd64 CGO_ENABLED=1 CC=o64-clang \
  go build -buildmode=c-shared -o respfilter.dylib .
```

### Using Makefile

```bash
make build    # Build for current platform
make clean    # Remove build artifacts
make test     # Run go vet
```

### Dependencies

The plugin has minimal external dependencies:

| Dependency | Purpose |
|------------|---------|
| `github.com/tidwall/gjson` | JSON path extraction (`usage.reasoning_tokens`, etc.) |
| `gopkg.in/yaml.v3` | YAML config parsing |
| Go standard library | `text/template`, `encoding/json`, `sync/atomic`, `sync`, `unsafe`, `C` |

**No CLIProxyAPI SDK dependency** — the plugin implements the C ABI directly with local type definitions.

---

## Configuration

The plugin uses a **rule-based** config system. Rules are evaluated in order — the first matching rule determines the action.

### Minimal config (sensible defaults)

```yaml
plugins:
  enabled: true
  configs:
    respfilter:
      enabled: true
      priority: 1
```

This uses built-in defaults: detect `reasoning_tokens == 516`, action `error`, all models, standard JSON paths. No `rules` or `defaults` blocks needed.

### Full config with custom rules

```yaml
plugins:
  enabled: true
  configs:
    respfilter:
      enabled: true
      priority: 1

      # ── Detection rules (first match wins) ──────────────────────────
      rules:
        # Rule 1: Classic 516 degradation on GPT-5.x models
        - name: "openai-516"
          paths:
            - "usage.reasoning_tokens"
            - "usage.completion_tokens_details.reasoning_tokens"
            - "usage.output_tokens_details.reasoning_tokens"
            - "reasoning_tokens"
          stream_paths:
            - "response.usage.output_tokens_details.reasoning_tokens"
            - "response.usage.reasoning_tokens"
          operator: "exact"
          values: [516]
          models: ["gpt-5*"]
          action: "error"
          error_status: 503
          error_body: '{"error":{"message":"Reasoning degradation (tokens={{.Tokens}}).","type":"degradation_filter","code":"reasoning_516"}}'
          stream_error: |
            data: {"error":{"message":"Reasoning degradation.","type":"degradation_filter","code":"reasoning_516"}}

            data: [DONE]
          log: true
          log_level: "warn"
          log_msg: 'respfilter[{{.Rule}}]: {{.Model}} tokens={{.Tokens}} path={{.Path}}'

        # Rule 2: Watch for very low reasoning — log only
        - name: "low-reasoning-watch"
          operator: "lte"
          values: [10]
          action: "log"

        # Rule 3: Retry mode for a specific model
        - name: "gpt-5-codex-retry"
          operator: "any_of"
          values: [516, 512, 508]
          models: ["gpt-5-codex"]
          action: "retry"
          retry_max: 2
          retry_backoff_ms: 500

      # ── Defaults (applied to rules that omit a field) ───────────────
      defaults:
        operator: "exact"
        values: [516]
        action: "error"
        error_status: 503

      # ── Stream optimization ─────────────────────────────────────────
      stream:
        keywords: ["usage", "reasoning"]

      # ── Model matching mode ─────────────────────────────────────────
      model_matching: "wildcard"   # exact | wildcard
```

### Top-level fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `rules` | array | one default rule | Detection rules, evaluated in order. First match wins. |
| `defaults` | object | built-in defaults | Default values for rules that omit a field. |
| `stream.keywords` | array | `["usage", "reasoning"]` | Only parse SSE chunks containing these keywords. Empty `[]` = parse all. |
| `model_matching` | enum | `wildcard` | `exact` (case-insensitive string match) or `wildcard` (supports `*`). |

### Rule fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `name` | string | `"unnamed"` | Rule name for logging and stats. |
| `paths` | array | see defaults | JSON paths (gjson syntax) for non-streaming responses. First existing path wins. |
| `stream_paths` | array | see defaults | Additional JSON paths for SSE stream chunks (e.g. Responses API `response.usage.*`). |
| `operator` | enum | `exact` | Match operator — see table below. |
| `values` | array | `[516]` | Match values. For `range`: `[min, max]`. |
| `models` | array | `[]` (all) | Model name filter. Supports wildcards when `model_matching: wildcard`. |
| `action` | enum | `error` | `error`, `retry`, or `log`. |
| `error_status` | int | `503` | HTTP status code in error response body. |
| `error_body` | string | template | Go `text/template` for error response body. |
| `stream_error` | string | template | Go `text/template` for SSE error chunk. |
| `retry_max` | int | `1` | Maximum retry attempts (`action: retry`). |
| `retry_backoff_ms` | int | `0` | Backoff between retries in milliseconds. |
| `log` | bool | `true` | Log detection events via `host.log`. |
| `log_level` | string | `warn` | Log level: `debug`, `info`, `warn`, `error`. |
| `log_msg` | string | template | Go `text/template` for log messages. |

### Match operators

| Operator | Condition | Example |
|----------|-----------|---------|
| `exact` | `tokens == values[0]` | `[516]` |
| `any_of` | `tokens in values` | `[516, 512, 508]` |
| `lte` | `tokens <= values[0]` | `[10]` |
| `gte` | `tokens >= values[0]` | `[1000]` |
| `range` | `values[0] <= tokens <= values[1]` | `[0, 100]` |
| `not_in` | `tokens not in values` | `[0, 1, 2]` |
| `always` | Always matches (for logging) | (ignored) |

### Template variables

Available in `error_body`, `stream_error`, and `log_msg`:

| Variable | Description |
|----------|-------------|
| `{{.Rule}}` | Rule name |
| `{{.Model}}` | Model name from the request |
| `{{.Tokens}}` | Detected reasoning token count |
| `{{.Path}}` | JSON path where tokens were found |
| `{{.Attempt}}` | Retry attempt number (0 for initial detection) |

### Hot reload

When `config.yaml` changes, CLIProxyAPI calls `plugin.reconfigure` with the new `config_yaml`. The plugin:

1. Parses the new YAML
2. Precompiles all `text/template` instances
3. Atomically swaps the config pointer via `atomic.Pointer`

No requests are blocked during the swap. Statistics persist across reloads (keyed by rule name).

---

## Management API

The plugin registers a browser-navigable resource:

```
GET /v0/resource/plugins/respfilter/status
```

**No authentication required** (resource routes are public). Returns JSON:

```json
{
  "plugin": "respfilter",
  "version": "2.0.0",
  "enabled": true,
  "model_matching": "wildcard",
  "stream_keywords": ["usage", "reasoning"],
  "rules": [
    {
      "name": "openai-516",
      "operator": "exact",
      "values": [516],
      "action": "error",
      "models": ["gpt-5*"],
      "paths": ["usage.reasoning_tokens", "..."],
      "stream_paths": ["response.usage.output_tokens_details.reasoning_tokens", "..."],
      "retry_max": 1,
      "retry_backoff": 0,
      "log": true
    }
  ],
  "stats": {
    "openai-516": {
      "matches": 42,
      "retries": 0,
      "retry_successes": 0
    }
  }
}
```

Open in a browser:

```
http://localhost:8317/v0/resource/plugins/respfilter/status
```

---

## How It Works

### Registered capabilities

| Capability | RPC Method | Purpose |
|------------|------------|---------|
| `response_after_translator` | `response.normalize_after` | Inspect non-streaming responses after translation |
| `response_stream_interceptor` | `response.intercept_stream_chunk` | Inspect each SSE stream chunk |
| `usage_plugin` | `usage.handle` | Observe completed usage records |
| `management_api` | `management.register` / `management.handle` | Status endpoint |

### Detection flow

**Non-streaming:**
1. Response is translated to the client's protocol format
2. Plugin checks all configured JSON paths for reasoning tokens (first existing path wins)
3. Token value is tested against each rule's operator in order
4. First matching rule's action is executed

**Streaming:**
1. Each SSE chunk is pre-filtered by keyword (e.g. `"usage"`, `"reasoning"`)
2. Only chunks containing keywords are JSON-parsed
3. When degradation is found in a usage chunk, an error SSE event is injected

**Retry:**
1. Plugin calls `host.model.execute` to re-execute the original request
2. Host's re-entrancy guard skips this plugin's interceptors on the retry (no infinite loop)
3. Retry response is checked for the same degradation signature
4. First non-degraded retry is returned; exhausted retries fall through to error mode

### Wire protocol notes

- All `[]byte` fields are base64-encoded in JSON transit (Go `encoding/json` default)
- Plugin type field names match `pluginapi` Go struct field names (PascalCase, no JSON tags) for request/response types
- RPC envelope: `{"ok": true, "result": {...}}` or `{"ok": false, "error": {"code": "...", "message": "..."}}`

---

## Troubleshooting

### Plugin not loading

| Symptom | Cause | Fix |
|---------|-------|-----|
| No log line for `respfilter` | `plugins.enabled: false` in config | Set `plugins.enabled: true` |
| No log line for `respfilter` | `configs.respfilter.enabled` missing or `false` | Add `enabled: true` under the plugin's config block |
| `plugin not found` | Wrong filename or directory | File must be `respfilter.so` in `plugins/`, `plugins/linux/amd64/`, or `plugins/linux/amd64-v3/` |
| `missing cliproxy_plugin_init` | Build failed or wrong build mode | Rebuild with `CGO_ENABLED=1 go build -buildmode=c-shared` |
| `plugin ABI version N is not supported` | Built against different CPA version | Rebuild from source; this plugin hardcodes ABI version 1 |
| `cannot open shared object file` | Missing libc dependencies | `sudo apt-get install -y libc6 libgcc-s1`; run `ldd respfilter.so` to check |
| `wrong ELF class` | Architecture mismatch | Build with correct `GOARCH` matching your host |
| Symlink not loaded | CPA skips symlinks | Use `cp` instead of `ln -s` |

### Config key case sensitivity

The plugin ID is derived from the **filename** (case-preserved). `RespFilter.so` → ID `RespFilter`, so your config key must be `configs.RespFilter:` — not `configs.respfilter:`.

### Plugin loaded but not detecting

1. **Check `enabled`** — both `plugins.enabled: true` and `configs.respfilter.enabled: true`
2. **Check model filter** — if `models: ["gpt-5*"]`, ensure your request uses a matching model name
3. **Check JSON paths** — verify the response actually contains the configured paths. Use `debug: true` in CPA config and check request logs
4. **Check stream keywords** — if streaming, ensure `stream.keywords` includes a substring present in the usage chunk
5. **Check the status endpoint** — `curl http://localhost:8317/v0/resource/plugins/respfilter/status` shows active rules

### Checking shared library dependencies

```bash
# List dynamic dependencies
ldd respfilter.so

# Verify exported symbols
nm -D respfilter.so | grep cliproxy
# Expected:
#   cliproxyPluginCall
#   cliproxyPluginFree
#   cliproxyPluginShutdown
#   cliproxy_plugin_init
```

### Running CPA in debug mode

```yaml
# config.yaml
debug: true
```

This enables verbose logging including plugin registration details and RPC calls.

---

## File Structure

```
respfilter/
├── main.go                 C ABI exports + method dispatch (thin — only file with import "C")
├── config.go               Config types, YAML parsing, defaults merging, template precompilation
├── detect.go               Match operators, wildcard model matching, JSON path extraction
├── handlers.go             Response/stream/usage handlers + rule matching engine + template rendering
├── retry.go                Retry logic via host.model.execute
├── management.go           Management API /status endpoint
├── go.mod                  Module definition (gjson + yaml.v3 only)
├── go.sum                  Dependency checksums
├── Makefile                Cross-platform build
├── config.snippet.yaml     Full annotated config example
├── README.md               This file (English)
└── README_CN.md            Chinese documentation
```

---

## License

MIT
