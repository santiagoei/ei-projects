# ei-projects

A project cockpit for the terminal: the same watched folders the
[ei.git-status](https://github.com/santiagoei/omarchy-git-status) bar
widget owns, seen as **projects** — every repository with its git state,
the recent **opencode sessions** worked on in them, and the newest
**notes** the Obsidian vault holds for each.

Built for Omarchy + Hyprland + foot; designed to be opened from a
keybind into a floating, centered window (see the setup below).

Six focusable panes, lazygit-shaped (the sixth appearing only for projects
with a docker environment): projects (top-left), the selected project's
repositories (bottom-left), a one-line **OPEN IN** launcher bar
for the selected repo (top-right), and beneath it three stacked list
panes: the repo's **commits**, its **opencode sessions**, and the
project's freshest **vault notes**. The frame lives in `## Layout` below.

## What it reads

| Feed     | Source | Cost |
|----------|--------|------|
| Projects | the `watchDirs` of the `ei.git-status` widget in `~/.config/omarchy/shell.json` | a read of your existing config |
| Repos    | `git status` etc., parallel, same flags as the panel's scanner (`--no-optional-locks`, fsmonitor off) | ~0.2 s for 15 repos on ntfs |
| Sessions | `~/.local/share/opencode/opencode.db` via `sqlite3 -json` — read-only first, a plain connection on a refused or mysteriously-empty read (the live db carries a WAL that can reject read-only mid-checkpoint); prefers `session_v2`, falls back to `session`, guards required columns | one subprocess |
| Todos    | the same db's `todo` table, for the session under the cursor: its open items (`pending`/`in_progress`) appear in the status line | one subprocess per selection |
| Envs     | a `docker-compose.yml`/`compose.yaml` auto-detected at the project root (or any repo root); state via `docker compose ps --format json` | four stats + one subprocess |
| Notes    | the vault on disk, `.md` files by mtime, matched per project by name tokens or `docsMap` | a walk |

No API keys, no daemons, nothing written outside `~/.cache` (which it
does not even use yet). Missing feeds (no sqlite3, no opencode, no vault)
degrade the UI instead of failing it.

## Layout (lazygit-shaped)

```
╭─ PROJECTS · 4 ──────────╮╭─ OPEN IN · repo · main · dirty 3 ──────╮
│> 1 obsidian    (1)      ││g lazygit · t terminal · c code · a …   │
│  2 omarchy     (1)      │╰────────────────────────────────────────╯
│  3 uh.humance.app       │╭─ COMMITS · main · 15 ──────────────────╮
│  4 icampus.fei.org      ││> 97d5fe3 Move files    santiago · 10m  │
╰─────────────────────────╯│  82cde26 Audit +     santiago · 1h     │
╭─ REPOSITORIES · proj ───╮╰────────────────────────────────────────╯
│> moodle405   ↑2         │╭─ SESSIONS · repo · 20 ─────────────────╮
╰─────────────────────────╯│> Auditoría y curaduría 7d · $0.10      │
                           ╰────────────────────────────────────────╯
                           ╭─ NOTES · 5 ────────────────────────────╮
                           │> 30 areas/area.linux.md     26m        │
                           ╰────────────────────────────────────────╯
tab/h/l panes · ↑/↓ move · enter act · 1-9 project · s session · d note · ? help · r rescan · q quit
```

Six focusable panes — projects, repos, the conditional **ENV** (shown
only while the selected project has a docker environment), commits,
sessions, notes — tab, or a click, walks them, titles sitting on the
border, lazygit-style. OPEN IN is a one-line bar: the git-status row
menu's keys, same letters same meaning, acting on the selected repository.
Commits load on demand for that repo (`git log`, newest 15); sessions
narrow to it (falling back to the project's list) and carry their open
todos into the status line; notes are the project's freshest vault files.
The mouse works throughout: click focuses and selects, the wheel moves
the hovered list. `?` opens the full help.

Colours come from the active Omarchy theme
(`~/.local/state/omarchy/current/theme/colors.toml`): the accent paints
the active pane's border and the selected-row bar is the theme's own
selection colour, so the cockpit reads as part of the desktop. The last
project/repo pair is remembered (by path) under the XDG state dir, so
`SUPER+M` reopens where you left off.

## Keys

Repo launchers (top-right **OPEN IN** pane, same as the bar panel's row menu):

| key   | action                        |
|-------|-------------------------------|
| `g`   | Open lazygit                  |
| `t`   | Open in terminal              |
| `c`   | Open in code                  |
| `a`   | Open in `<agent>` (from `omarchy default agent`) |
| `e`   | Open in file explorer         |
| `y`   | Copy path (of the focused row when in context, else the repo) |

Navigation (the footer bar):

| key         | action                                            |
|-------------|---------------------------------------------------|
| `tab` `l` `→` / `h` `←` `esc` | cycle the panes: projects → repos → (env) → commits → sessions → notes (`esc` at projects quits) |
| `↑/↓` `j/k` | move within the focused pane                      |
| `enter`     | projects: to repos · repos: lazygit · commits: copy sha · sessions: resume · notes: open |
| `s` / `d`   | resume the marked session / open the marked note  |
| `1`–`9`     | jump to project N                                 |
| `r`         | rescan                                            |
| `?`         | full help overlay                                 |
| `q`         | quit                                              |
| mouse       | click focuses + selects a row; wheel moves the hovered list |

ENV pane keys (only while it is focused — there they beat the global
meanings: `r` restarts, it does not rescan; `d` scales down):

| key   | action (opens a terminal, on the selected service)   |
|-------|------------------------------------------------------|
| `u`   | `docker compose up -d`                               |
| `d`   | `docker compose down`                                |
| `r`   | restart                                              |
| `l`   | logs (follow)                                        |
| `x`   | shell (`exec … sh`)                                  |

## Install

```bash
mise install                     # pinned go in mise.toml
mise exec -- go build -o ~/.local/bin/ei-projects .
```

## Hyprland (Omarchy)

`~/.config/hypr/bindings.lua`:

```lua
-- SUPER+M (solo): project cockpit, flotante y centrado.
o.bind("SUPER + M", "Projects",
  o.launch_sole("ei-projects", "foot --app-id=ei-projects -e ei-projects"))
```

`~/.config/hypr/looknfeel.lua`:

```lua
-- Hyprland >=0.56 wants expressions for sizes: "80%" is silently ignored.
o.window({ class = "^ei-projects$" }, {
  float = true,
  center = true,
  size = { "(monitor_w*0.8)", "(monitor_h*0.8)" },
})
```

`o.launch_sole` focuses the existing window instead of stacking a second
one; the rule floats, centers and sizes the cockpit over the monitor
Hyprland has focused.

## Configuration (optional)

`~/.config/ei-projects/config.json` overrides anything derived from
shell.json; all keys optional:

```json
{
  "watchDirs": [ "~/Projects/foo" ],
  "vault": "~/notes/obsidian",
  "docsMap": { "uh.humance.app": "50 area.tr/tr.uh" },
  "maxDepth": 4,
  "notesLimit": 5,
  "sessionLimit": 10
}
```

`docsMap` pins a project to its vault folder when the folder's naming
scheme (PARA-like `tr.uh`) does not speak the project's name — the token
heuristic bridges `icampus.fei.org` → `tr.fei` on the shared "fei", but
two-letter tokens are too generic to trust.

## JSON contract

```bash
ei-projects scan --pretty
```

Prints the full report (projects → repos, sessions, notes, feed errors).
It is intentionally stable: anything else — the bar panel, scripts — can
consume the same shape instead of scanning twice.

## Roadmap

Phase 2 adds Todoist (open tasks per project, reusing the token
`crmne.todoist` already stores) and docker environments
(`docker compose ps` badges, up/down/logs from the cockpit). Phase 3:
`project.toml` manifests per folder, making the graph explicit where
today it is discovered.

## Tests

```bash
mise exec -- go test ./...
```

Coverage: config resolution (shell.json round-trip, vault adoption,
overrides), repo discovery (nesting, depth, dependency pruning), git
states against a real bare remote, session SQL (prefix rules, quoting,
`session_v2` preference, schema guards), vault matching (tokens, mtimes),
and an end-to-end scan over a sandbox home.
