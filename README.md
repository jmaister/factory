# factory

**A software factory driven by your ticket board.** Create a task, move it to *TODO*, and an AI agent (Claude Code) analyses it, implements it, tests it, opens a pull request and deploys it to a local test environment. You review in the board and on GitHub; the factory does the rest.

```
📝 DRAFT ──you──▶ 📥 TODO ──auto──▶ 🤖 IN PROGRESS ──auto──▶ 👀 REVIEW ──you (merge)──▶ 👍 APPROVED ──auto──▶ ✅ DONE
                                         ▲                       │
                                         └──── 🔁 REWORK ◀───────┘ (auto on CI failure or review comments)
```

Every task runs in its own git worktree, in an agent session you can reach from the Claude app (Remote Control). Each branch can be deployed to one of several local **test slots**, each with its own URL, so several features can be tested side by side.

## Features

- **Board as the interface.** Task state is the column. Nothing else to learn.
- **Autonomous agent loop.** Picks the oldest task, runs Claude Code in an isolated worktree, commits, pushes, opens a PR.
- **Turn-based sessions.** A Claude Code `Stop` hook tells the factory when the agent stops; the factory publishes as soon as there are changes and keeps the conversation alive for follow-up.
- **Rework that resumes context.** Review comments on GitHub or a comment on the task send the work back to the same session.
- **CI-aware.** Waits for the CI of the PR head commit before asking for review; failures go back to rework with the failing run linked.
- **Local test slots.** Up to 5 branches deployed at once (`build` + run), with per-slot URLs, shared databases and data, and test accounts shown in the UI.
- **Terminal UI.** Task list grouped by column, detail panel with progress bar, PR/CI status, slot URL, comments and content, live log, help screen.
- **Webhook wake-up.** Board changes wake the loop immediately; polling is the safety net.

## Supported today

| Role | Provider | Status |
|---|---|---|
| Tickets | [Vikunja](https://vikunja.io) | ✅ |
| Source code | GitHub | ✅ |
| CI results | Forgejo Actions | ✅ (read-only) |
| Agent | Claude Code | ✅ |

The provider layer is interfaces (`internal/tickets`, `internal/repos`, `internal/ci`). Next on the list: **Jira** (tickets), **GitHub Issues**, **GitLab** and **Gitea/Forgejo** as code hosts, and GitHub Actions as CI. Adding a provider is one file that implements the interface.

## Requirements

- Go 1.26+ (to build), `git`, `make`, `screen`
- Claude Code CLI (`claude`), logged in on the machine that runs the factory
- A Vikunja instance with a kanban view on the project
- A GitHub repository (fine-grained token)
- Optional: Forgejo Actions for CI results, `tg` (Taronja Gateway) if your app uses it

See [spec/permisos-tokens.md](spec/permisos-tokens.md) for the exact permissions each token needs.

## Install

Linux and macOS:

```bash
curl -fsSL https://raw.githubusercontent.com/jmaister/factory/main/install.sh | bash
```

Windows (PowerShell):

```powershell
irm https://raw.githubusercontent.com/jmaister/factory/main/install.ps1 | iex
```

Or build from source:

```bash
git clone https://github.com/jmaister/factory && cd factory
make build            # bin/factory
make install          # ~/.local/bin/factory
```

Packages for linux/darwin/windows on amd64 and arm64 are attached to each [release](https://github.com/jmaister/factory/releases).

## Configure

The factory is configured per project. Put two files next to your repository (keep them out of git):

**`factory.yaml`**

```yaml
tickets:
  type: vikunja
  url: http://vikunja.local:3456
  token: ${VIKUNJA_TOKEN}
  project: myproject            # Vikunja project with the kanban board

repo:
  type: github
  url: https://github.com/you/myproject
  path: .                       # local checkout of this repository
  branch: main                  # base branch
  token: ${GITHUB_TOKEN}

build: make build               # compiles the code of a slot
local_deploy: true              # build + run the branch when the agent finishes
local_links: [.env, node_modules]   # git-ignored files shared with slots

app_binary: ./myapp             # binary produced by `build`
# gateway_command: tg run --config gateway.yaml   # default; set it if your app has no gateway

slots:
  count: 5
  base_port: 8010               # slot i: base_port + 10·i (app on +1)
  public_host: http://192.168.1.35

agent:
  model: claude-haiku-5-5
  max_rework: 3

webhook:
  listen: ":8787"
  public_url: http://192.168.1.35:8787

ci-server:                      # optional
  type: forgejo
  url: http://forgejo.local:3000
  token: ${FORGEJO_TOKEN}
  repo: owner/myproject

timezone: Europe/Madrid
```

**`factory.env`** (permissions `600`, never committed):

```bash
VIKUNJA_TOKEN=...
GITHUB_TOKEN=...
FORGEJO_TOKEN=...             # if ci-server is set
FACTORY_WEBHOOK_TOKEN=...     # created by `factory webhook install`
TEST_ADMIN_USER=admin         # optional: shown in the detail panel
TEST_ADMIN_PASSWORD=...
```

The board needs one column per state: `📝 DRAFT`, `📥 TODO`, `🤖 EN CURSO`, `👀 REVISIÓN`, `🔁 REWORK`, `👍 APROBADO`, `✅ HECHO` (titles are fixed in `internal/config`).

Check the setup:

```bash
factory check
factory webhook install       # optional: wake the loop on board changes
```

## Use

```bash
factory start                 # opens the TUI and runs the loop
factory start --once          # process one task and exit (no UI)
factory new "Title of the task"   # create a task in DRAFT
factory ready ACA-7           # move a DRAFT task to TODO
factory list                  # tasks per column
factory deploy ACA-7          # deploy a task's branch to a slot
factory version
```

Keys in the TUI: `Tab` focus · `↑/↓` move · `d` deploy the selected task to a slot · `c` content and comments · `l` log · `t` color test · `h` help · `r` refresh · `q` quit.

Typical flow:

1. Write the task in Vikunja (DRAFT) and move it to TODO.
2. Open the agent session from the Claude app (Remote Control, session `factory ACA-7`) if you want to answer questions.
3. When CI is green the task moves to REVIEW. Open the PR link from the detail panel, test the slot URL, review.
4. Merge on GitHub (or move the task to APPROVED with the PR open). The factory merges, deploys master and moves the task to DONE when the merge CI is green.

Full state machine, who triggers each step, and what is automatic: [spec/workflow.md](spec/workflow.md).

## How it works

- **State** is the Vikunja column. The factory never keeps its own task database.
- **Per task**: a worktree in `~/.local/state/factory/<project>/worktrees/`, a branch `task/<id>`, a Claude session in `screen` (`factory-<id>`). The session id is stored as a task comment, so a conversation can be resumed with `claude --resume <id>`.
- **Per slot**: a detached worktree at the deployed commit, symlinks to the repository's databases and `data/`, and a `screen` session running the app and the gateway.
- **Nothing is merged or deployed without a human action** except what the states above mark as automatic.

## Limitations

- The UI and task titles in the board are in Spanish today. Translations are welcome.
- Slots share one database: a branch with schema migrations affects the others. Use slots for features that don't change the schema, or test them in isolation.
- The Claude session stops being observable from the factory when it is closed; a session started before the `Stop` hook existed needs a manual signal file (see the workflow doc).
- Forgejo is read-only (CI status). The factory does not run CI itself.
- Linux and macOS are the tested platforms. Windows builds exist, but the local deployment flow relies on `screen` and POSIX shells.

## Documentation

- [spec/workflow.md](spec/workflow.md) — states, automatic vs manual steps, diagram
- [spec/permisos-tokens.md](spec/permisos-tokens.md) — permissions per token
- [spec/release.md](spec/release.md) — how releases are built and published
- [DESIGN.md](DESIGN.md) — original design notes

## Contributing

Issues and pull requests are welcome. Please run `go vet ./...` and `gofmt -l .` before sending a change. There is no test suite yet; new providers should come with tests for their API mapping.

## License

Not decided yet.
