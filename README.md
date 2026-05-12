# aws-green

### get your pipelines green

A terminal dashboard for live AWS CodePipeline health across multiple accounts and regions — no console required.

## Overview

aws-green polls AWS CodePipeline and displays a live stoplight view of all configured pipelines, their stages, and recent executions.

## Installation

```bash
go install github.com/ericdahl-dev/aws-green@latest
```

## Configuration

Create `~/.config/aws-green/config.toml`:

```toml
[settings]
poll_interval_seconds = 30

[[accounts]]
name = "production"
profile = "prod"
region = "us-east-1"

[[pipelines]]
account = "production"
name    = "my-deploy-pipeline"

[[pipelines]]
account = "production"
name    = "my-build-pipeline"
```

## Keybindings

| Key | Action |
|-----|--------|
| `↑` / `k` | Navigate up |
| `↓` / `j` | Navigate down |
| `enter` / `space` | Expand/collapse Pipeline or Execution row |
| `r` | Force refresh |
| `o` | Open pipeline in AWS Console |
| `q` | Quit |
| `?` | Toggle help overlay |
