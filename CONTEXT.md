# aws-green

A terminal dashboard that shows live AWS CodePipeline health across multiple accounts and regions, updating automatically via polling.

## Language

**Account**: An AWS account being monitored. Identified by a named AWS profile. Each Account has a region.
_Avoid_: org, workspace

**Pipeline**: The primary display unit. A named CodePipeline within an Account (e.g. `my-deploy-pipeline`).
_Avoid_: workflow, project

**Execution**: A single run of a Pipeline, triggered by a source change or manual start. Has a status (InProgress, Succeeded, Failed, Stopped, Superseded).
_Avoid_: run, build

**Stage**: A named phase within a Pipeline (e.g. `Source`, `Build`, `Deploy`). Drill-down target below Pipeline.
_Avoid_: job, step (Step is a lower-level concept within a Stage)

**Action**: The lowest-level unit within a Stage. Not a primary display target.
_Avoid_: task

**Config file**: The user-managed file at `~/.config/aws-green/config.toml` that lists which Pipelines to watch and per-Account AWS profile/region.

## Relationships

- A **Pipeline** belongs to exactly one **Account**
- A **Pipeline** has many **Executions**; the dashboard shows only the latest
- An **Execution** has one or more **Stages**
- A **Stage** has one or more **Actions**

## Stoplight

The visual health indicator for a Pipeline. Derived from the latest Execution status.

| Color | Meaning | CodePipeline statuses |
|---|---|---|
| 🟢 Green | Healthy | `Succeeded` |
| 🔴 Red | Broken or blocked | `Failed`, `Stopped` |
| 🟡 Yellow | In progress | `InProgress` |
| ⚪ Grey | No signal | `Superseded`, no executions yet |

_Avoid_: badge, indicator, light

## Active-first sorting

Pipelines are sorted by Stoplight priority so the most actionable items appear at the top: 🟡 in-progress → 🔴 failing → 🟢 passing → ⚪ no signal. Order is stable within each tier.
_Avoid_: bubbling, floating

## Dashboard tree

```
▶ 🔴  production / my-deploy-pipeline
▼ 🟡  production / my-build-pipeline
      ● Source      Succeeded
      ● Build       InProgress
      ○ Deploy      —
```

- **Pipeline row**: expand/collapse with `enter`/`space`. When expanded shows Stage rows.
- **Stage row**: non-navigable; shows current stage status for latest Execution.

_Avoid_: detail view, drill-down screen

## Polling

The mechanism by which the TUI fetches fresh data from the AWS CodePipeline API. Runs on a configurable interval (default: 30 seconds). On API failure, the last known status is retained and shown with a staleness indicator.

Each poll cycle makes the following API calls per Pipeline:
- 1 × `GetPipelineState` (stage/action statuses for latest execution)

_Avoid_: refresh, sync, watch

## Keybindings

| Key | Action |
|---|---|
| `↑` / `k` | Navigate up |
| `↓` / `j` | Navigate down |
| `enter` / `space` | Expand/collapse Pipeline row |
| `r` | Force refresh |
| `o` | Open pipeline in AWS Console |
| `q` | Quit |
| `?` | Toggle help overlay |

## Flagged ambiguities

- "account" vs "profile" — resolved: Account is the logical concept; profile is the credential mechanism
- "manual approval" — open: whether to show approval gates as a special Stoplight state (e.g. ⏸) is not yet decided
