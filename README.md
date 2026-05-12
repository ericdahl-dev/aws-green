# aws-green

### get your pipelines green

A terminal dashboard for live AWS CodePipeline health across multiple accounts and regions — no browser required.

![aws-green dashboard](docs/screenshot.svg)

## Features

- **Stoplight-per-pipeline** — 🟢 🔴 🟡 ⚪ derived from stage execution status
- **Stage-level expand** — expand any pipeline to see each stage's current status inline
- **Active-first sorting** — in-progress and failing pipelines surface to the top automatically
- **Inline expand/collapse** — navigate with `↑`/`↓`, toggle any row with `enter`/`space`
- **Auto-polling** — refreshes every 30 seconds (configurable); retains last-known status on API errors
- **Multi-account** — per-account AWS profile config with named profiles or environment credentials
- **Single binary** — no runtime, no dependencies

## Install

```bash
go install github.com/ericdahl-dev/aws-green@latest
```

## Config

Create `~/.config/aws-green/config.toml`:

```toml
[settings]
poll_interval_seconds = 30

[[accounts]]
name    = "production"
profile = "prod"
region  = "us-east-1"

[[accounts]]
name    = "staging"
profile = "staging"
region  = "us-west-2"

[[pipelines]]
account = "production"
name    = "my-deploy-pipeline"

[[pipelines]]
account = "staging"
name    = "my-build-pipeline"
```

### Using the default AWS profile

If all your pipelines live in one account, omit `[[accounts]]` and leave `account` blank — aws-green will use your default AWS profile and region:

```toml
[[pipelines]]
name = "my-deploy-pipeline"

[[pipelines]]
name = "my-other-pipeline"
```

### Authentication

aws-green uses the standard AWS credential chain via `aws-sdk-go-v2`. Any of these work:

- Named profiles in `~/.aws/config` (recommended — set `profile` in `[[accounts]]`)
- AWS SSO via `aws sso login`
- Environment variables (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, etc.)
- IAM instance/task roles when running on AWS

## Keybindings

| Key | Action |
|---|---|
| `↑` / `k` | Navigate up |
| `↓` / `j` | Navigate down |
| `enter` / `space` | Expand / collapse pipeline row |
| `r` | Force refresh |
| `o` | Open pipeline in AWS Console |
| `q` | Quit |
| `?` | Help overlay |
