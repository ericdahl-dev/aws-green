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

Each poll cycle makes the following API calls per Project:
- 1 × `GetPipelineState` per Pipeline (stage/action statuses for latest execution)
- 1 × `DescribeStacks` per Stack
- 1 × `DescribeServices` per ECS cluster (covers every Service in it)
- 2 × (`ListTasks` + `DescribeTasks`) per **unhealthy** Service — see Task detail

All three clients poll on the same tick, so a cycle reaches AWS as a burst.
The SDK clients are built by `internal/awscfg` in **adaptive retry mode**, which
keeps a client-side rate limiter that learns from throttle responses and slows
outgoing calls before AWS has to reject them. AWS publishes no remaining-quota
header, so there is no budget to pace against — the limiter infers one.

_Avoid_: refresh, sync, watch

## Task detail

The stopped-task lookup behind a Service's `FailingTaskCount` and
`StoppedReason`. It costs two API calls per Service, and exists only to explain
a failure — a Service with nothing wrong has no stopped tasks to report. It is
therefore fetched only when the `DescribeServices` summary already says
something is off: task counts disagree, tasks are pending, a deployment is
active, or ECS reports consecutively failed tasks on a deployment.

That last condition is load-bearing. A crash-looping Service is restarted fast
enough to keep reporting its full task count, so on counts alone it looks
healthy; ECS's own `failedTasks` figure — returned in the same
`DescribeServices` response, at no extra cost — is what catches it. Without it
the gate would turn a crash-looping Service green.

_Avoid_: task fetch, stopped tasks (use Task detail)

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
