# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

PicoClaw is an ultra-lightweight personal AI Assistant written in Go, designed to run on $10 hardware with <10MB RAM. It's inspired by [nanobot](https://github.com/HKUDS/nanobot) and refactored from scratch in Go through an AI-bootstrapped process.

**Key Characteristics:**

- Single self-contained binary across RISC-V, ARM, and x86
- Plugin-based tool and skill system
- Multi-provider LLM support (OpenRouter, Anthropic, OpenAI, Gemini, Zhipu, etc.)
- Multi-channel support (Telegram, Discord, QQ, DingTalk, LINE, Slack, etc.)
- Security sandbox with workspace restrictions

## Build Commands

```bash
# Build for current platform
make build

# Build for all platforms (Linux amd64/arm64/riscv64, Darwin arm64, Windows amd64)
make build-all

# Install to ~/.local/bin
make install

# Run tests
make test

# Static analysis
make vet

# Format code
make fmt

# Full check (deps, fmt, vet, test)
make check

# Run with args
make run ARGS="agent -m 'Hello'"

# Development (no install)
make build && ./build/picoclaw agent -m "Hello"
```

## Running Tests

```bash
# Run all tests
go test ./...

# Run tests in a specific package
go test ./pkg/tools/...

# Run a specific test
go test ./pkg/tools -run TestEditFile

# Run with verbose output
go test -v ./pkg/agent/...
```

## Architecture

### Directory Structure

```
cmd/picoclaw/     # Main entry point (main.go)
pkg/              # Core packages
├── agent/        # Agent loop, context management, memory handling
├── channels/     # Communication channels (Telegram, Discord, QQ, etc.)
├── providers/    # LLM providers (OpenAI, Anthropic, Gemini, Zhipu, etc.)
├── tools/        # Available tools (filesystem, shell, web, cron, etc.)
├── config/       # Configuration management
├── cron/         # Scheduled task execution
├── heartbeat/    # Periodic background tasks
├── skills/       # Extensible skill system
├── bus/          # Message bus for inter-component communication
├── session/      # Session management
├── state/        # Persistent state
├── voice/        # Voice transcription (Groq)
├── auth/         # OAuth/token authentication
├── devices/      # Device event monitoring
├── migrate/      # Migration from OpenClaw
└── logger/       # Structured logging
workspace/        # Embedded workspace template (copied to ~/.picoclaw/workspace)
config/           # Configuration examples
```

### Core Components

**Agent Loop (`pkg/agent/loop.go`)**: The main conversation handler. Key methods:

- `ProcessDirect()`: Process a direct message from CLI
- `ProcessHeartbeat()`: Handle periodic tasks (independent context)
- `Run()`: Main event loop for gateway mode

**Tool Registry (`pkg/tools/registry.go`)**: Manages available tools. Tools implement the `Tool` interface:

- `Name()`, `Description()`, `Parameters()` - for LLM discovery
- `Execute(ctx, args)` - execution logic

**Provider System (`pkg/providers/`)**: Abstracts LLM APIs. `CreateProvider(cfg)` returns the appropriate provider based on configuration.

**Channel Manager (`pkg/channels/manager.go`)**: Orchestrates all communication channels. Each channel implements the `Channel` interface.

**Message Bus (`pkg/bus/`)**: Pub/sub for inter-component communication between agent loop and channels.

### Data Flow

```
User Message → Channel → MessageBus → AgentLoop.ProcessMessage()
                                          ↓
                                 Tool execution (if needed)
                                          ↓
                                 Provider (LLM API call)
                                          ↓
Response ← MessageBus ← AgentLoop ←───────┘
```

### Workspace Structure

Users' workspace at `~/.picoclaw/workspace/`:

```
sessions/      # Conversation history
memory/        # Long-term memory (MEMORY.md)
state/         # Persistent state
cron/          # Scheduled jobs database
skills/        # Custom skills
AGENTS.md      # Agent behavior guide
HEARTBEAT.md   # Periodic task prompts
IDENTITY.md    # Agent identity
SOUL.md        # Agent soul
TOOLS.md       # Tool descriptions
USER.md        # User preferences
```

## Key Patterns

### Adding a New Tool

1. Create a file in `pkg/tools/` implementing the `Tool` interface
2. Register in `pkg/agent/loop.go` via `agentLoop.RegisterTool()`
3. The tool automatically becomes available to the LLM

### Adding a New Channel

1. Create a file in `pkg/channels/` implementing the `Channel` interface
2. Add config struct to `pkg/config/config.go`
3. Register in `pkg/channels/manager.go`

### Adding a New Provider

1. Create a file in `pkg/providers/` implementing the provider interface
2. Add config to `pkg/config/config.go`
3. Update `CreateProvider()` in `pkg/providers/factory.go`

## Configuration

Main config file: `~/.picoclaw/config.json`

Environment variables override config (prefix: `PICOCLAW_`). Examples:

- `PICOCLAW_AGENTS_DEFAULTS_MODEL` - default model
- `PICOCLAW_HEARTBEAT_ENABLED` - enable/disable heartbeat
- `PICOCLAW_CHANNELS_TELEGRAM_TOKEN` - Telegram bot token

## Security Model

- `restrict_to_workspace: true` (default) - Agent can only access files/commands within workspace
- Dangerous commands are blocked even with restrictions disabled (`rm -rf`, `dd`, `shutdown`, etc.)
- SSRF protection for network tools

## CLI Commands

```bash
picoclaw onboard              # Initialize config & workspace
picoclaw agent -m "Hello"     # One-shot message
picoclaw agent                # Interactive mode
picoclaw gateway              # Start gateway (channels + heartbeat + cron)
picoclaw status               # Show status
picoclaw cron list            # List scheduled jobs
picoclaw skills list          # List installed skills
picoclaw auth login --provider openai  # OAuth login
picoclaw rag index            # Build/update RAG index
picoclaw rag search --query … # Search indexed knowledge base
picoclaw rag info             # Show RAG index status & config
picoclaw rag list             # List indexed documents
```
