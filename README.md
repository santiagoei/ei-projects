# ei-projects

A project cockpit for the terminal: the same watched folders the
[ei.git-status](https://github.com/santiagoei/omarchy-git-status) bar
widget owns, seen as **projects** — every repository with its git state,
the recent **opencode sessions** worked on in them, and the newest
**notes** the Obsidian vault holds for each.

Built for Omarchy + Hyprland + foot; designed to be opened from a
keybind into a floating, centered window (see the setup below).

```
ei-projects  4 projects

▸ icampus.fei.org             6 repos · clean          ⧗ 1h        ✎ 5
  omarchy                     6 repos · 1 pending      ⧗ now       ✎ 1
  uh.humance.app              1 repos · 1 pending      ⧗ 2h        ✎ 5
  obsidian                    1 repos · pending        ⧗ yesterday ✎ 5
```

Enter walks into a project:

```
omarchy  /mnt/files/projects/omarchy

REPOSITORIES
▸ ei-projects                 main        dirty 5
  omarchy-git-status          main        clean

SESSIONS (opencode)
  [env] git-status - Menú por repo…        now  $0.40

NOTES (vault)
  tr.fei/backlog.2026.md                  2d
```

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

## Keys

| key         | action                                            |
|-------------|---------------------------------------------------|
| `↑/↓` `j/k` | move                                              |
| `enter`     | project: act on the row (repo→lazygit, session→resume, note→open) |
| `g` `t` `c` `a` `e` `y` | on a repo: lazygit / terminal / editor / agent / files / copy path |
| `s`         | resume the selected opencode session               |
| `d`         | open the selected note                             |
| `r`         | rescan                                            |
| `esc`       | back to the dashboard (`q` anywhere quits)         |

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
o.window({ class = "^ei-projects$" }, { float = true, center = true, size = { "80%", "80%" } })
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
