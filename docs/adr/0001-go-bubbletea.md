# Go + Bubble Tea for TUI

We're building the TUI in Go using [Bubble Tea](https://github.com/charmbracelet/bubbletea) as the UI framework and the AWS SDK for Go v2 for AWS API access. Go produces a single distributable binary with no runtime dependency, Bubble Tea is the most mature terminal UI framework in the ecosystem, and the two libraries compose naturally. Alternatives considered: Python/Textual, Rust/Ratatui, Node/Ink — all capable but none offer the same combination of strong AWS SDK support, mature TUI primitives, and easy distribution.

## Charm stack (terminal UX)

We standardize on [Charm](https://charm.land/libs/) libraries that pair with Bubble Tea:

- **[Lip Gloss](https://github.com/charmbracelet/lipgloss)** — styles for rows, hints, and stage status.
- **[Bubbles / spinner](https://github.com/charmbracelet/bubbles)** — in-flight fetch indicator beside the title.
- **[Glamour](https://github.com/charmbracelet/glamour)** — Markdown help overlay (`?`).
- **[Huh](https://github.com/charmbracelet/huh)** — `aws-green init` form and in-TUI manage screen.
- **[log](https://github.com/charmbracelet/log)** — optional stderr debug when `AWS_GREEN_DEBUG` is set.
