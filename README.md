# aws-green

### get your projects green

A terminal dashboard for live AWS resource health across multiple accounts and regions — CodePipeline, CloudFormation, and ECS — no browser required.

![aws-green dashboard](docs/screenshot.svg)

## Features

- **Stoplight-per-project** — 🟢 🔴 🟡 ⚪ derived from worst-case across pipeline, CloudFormation stacks, and ECS services
- **Per-resource-type summary** — collapsed row shows `Pipeline 🟡  Stacks 🟢  ECS 🟢` at a glance
- **Stage-level expand** — expand any project to see pipeline stages, stack statuses with elapsed timers, and ECS running/desired task counts
- **Active-first sorting** — in-progress and failing projects surface to the top automatically
- **Inline expand/collapse** — navigate with `↑`/`↓`, toggle any row with `enter`/`space`
- **Auto-polling** — refreshes every 30 seconds (configurable); retains last-known status on API errors
- **CloudFormation monitoring** — maps stack status to stoplight; in-progress stacks show elapsed timer
- **ECS service monitoring** — shows running/desired task counts; flags active deployments
- **Multi-account** — per-account AWS profile config with named profiles or environment credentials
- **In-TUI project management** — add, edit, delete, and enable/disable projects without leaving the terminal
- **Interactive init** — `aws-green init` writes a starter config via a terminal form
- **Single binary** — no runtime, no dependencies

## Install

### Homebrew

```bash
brew install ericdahl-dev/tap/aws-green
```

### Go

```bash
go install github.com/ericdahl-dev/aws-green@latest
```

## First-time config

Run an interactive wizard (writes `~/.config/aws-green/config.toml`):

```bash
aws-green init
```

Use `aws-green init --force` to overwrite an existing file.

## Config

Create `~/.config/aws-green/config.toml` by hand, or start from `aws-green init` and edit:

```toml
[settings]
poll_interval_seconds = 30

[[accounts]]
name    = "production"
profile = "prod"
region  = "us-east-1"

[[projects]]
name    = "my-app"
account = "production"
# enabled = false          # optional; disable without deleting (no API calls made)

  [projects.pipeline]
  name = "my-app-DeploymentPipeline-abc123"

  [[projects.stacks]]
  name = "my-app-cluster"

  [[projects.stacks]]
  name = "my-app-service"

  [[projects.ecs]]
  cluster  = "my-app-FargateCluster-xyz789"
  services = ["my-app-web", "my-app-worker"]
```

### Using the default AWS profile

If all your resources live in one account, omit `[[accounts]]` and leave `account` blank — aws-green will use your default AWS profile and region:

```toml
[[projects]]
name = "my-app"

  [projects.pipeline]
  name = "my-deploy-pipeline"

  [[projects.stacks]]
  name = "my-app-stack"
```

### Authentication

aws-green uses the standard AWS credential chain via `aws-sdk-go-v2`. Any of these work:

- Named profiles in `~/.aws/config` (recommended — set `profile` in `[[accounts]]`)
- AWS SSO via `aws sso login`
- Environment variables (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, etc.)
- IAM instance/task roles when running on AWS

## Keybindings

### Dashboard

| Key | Action |
|---|---|
| `↑` / `k` | Navigate up |
| `↓` / `j` | Navigate down |
| `enter` / `space` | Expand / collapse project row |
| `r` | Force refresh |
| `f` | Smart fix (restart pipeline / force deploy / continue rollback) |
| `o` | Open pipeline in AWS Console |
| `m` | Open project manager |
| `q` | Quit |
| `?` | Toggle help overlay |
| `esc` | Close help overlay |

### Project manager (`m`)

| Key | Action |
|---|---|
| `↑` / `k` | Navigate up |
| `↓` / `j` | Navigate down |
| `t` / `space` | Toggle enable / disable |
| `a` | Add project |
| `e` | Edit project |
| `d` | Delete project |
| `esc` | Back to dashboard |

Changes are written to `config.toml` immediately and the poller reloads automatically. Disabled projects make no AWS API calls.
