# ei-projects

A project cockpit for the terminal: the same watched folders the
[ei.git-status](https://github.com/santiagoei/omarchy-git-status) bar
widget owns, seen as **projects** — every repository with its git state,
the recent **opencode sessions** worked on in them, and the newest
**notes** the Obsidian vault holds for each.

Built for Omarchy + Hyprland + foot; designed to be opened from a
keybind into a floating, centered window (see the setup below).

Four panes, lazygit-shaped: projects (top-left), the selected project's
repositories (bottom-left), live shortcuts (top-right), and a tabbed
context pane (bottom-right) with the repo's **commits**, its **opencode
sessions**, and the project's freshest **vault notes**. The frame lives
in `## Layout` below.

## What it reads

| Feed     | Source | Cost |
|----------|--------|------|
| Projects | the `watchDirs` of the `ei.git-status` widget in `~/.config/omarchy/shell.json` | a read of your existing config |
| Repos    | `git status` etc., parallel, same flags as the panel's scanner (`--no-optional-locks`, fsmonitor off) | ~0.2 s for 15 repos on ntfs |
| Sessions | `~/.local/share/opencode/opencode.db` via `sqlite3 -readonly` — prefers `session_v2`, falls back to `session`, guards required columns | one subprocess |
| Notes    | the vault on disk, `.md` files by mtime, matched per project by name tokens or `docsMap` | a walk |

No API keys, no daemons, nothing written outside `~/.cache` (which it
does not even use yet). Missing feeds (no sqlite3, no opencode, no vault)
degrade the UI instead of failing it.

## Layout (lazygit-shaped)

```
╭──────────────────────╮╭───────────────────────────────────────────╮
│ PROJECTS · 4         ││ SHORTCUTS   (keys for the current focus)  │
│ > 1 obsidian         │╰───────────────────────────────────────────╯
│   2 omarchy  (1)     │╭───────────────────────────────────────────╮
│   ...                ││ [COMMITS]· SESSIONS · NOTES  main @ repo  │
╞══════════════════════╡│ > 97d5fe3 Move files        santiago · 10m│
│ REPOSITORIES · proj  ││   82cde26 Audit +           santiago · 1h │
│ > obsidian  dirty 2  │╰───────────────────────────────────────────╯
╰──────────────────────╯
```

Commits load on demand for the selected repo (`git log`, newest 15);
sessions narrow from the project list to the selected repo's directory.

## Keys

| key         | action                                            |
|-------------|---------------------------------------------------|
| `tab` `l` `→` / `h` `←` | cycle focus: projects → repos → context    |
| `[` / `]`   | context section: commits / sessions / notes       |
| `↑/↓` `j/k` | move within the focused pane                      |
| `enter`     | projects: to repos · repos: lazygit · context: act on the row |
| `g` `t` `c` `a` `e` `y` | selected repo: lazygit / terminal / editor / agent / files / copy path |
| `s`         | resume the selected session (terminal at its dir) |
| `d`         | open the selected note                            |
| `1`–`9`     | jump to project N                                 |
| `r`         | rescan                                            |
| `esc` / `q` | one pane back / quit                              |

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
