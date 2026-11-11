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
| Repos    | one `git status --porcelain=v2 --branch` per repo — branch, upstream, ahead/behind and the working tree in a single call, parallel, same guards as the panel's scanner (`--no-optional-locks`, fsmonitor off); a git older than 2.22 falls back to the three-call route | one subprocess per repo, used to be four |
| Sessions | `~/.local/share/opencode/opencode.db` via `sqlite3 -json` — read-only first, a plain connection on a refused or mysteriously-empty read (the live db carries a WAL that can reject read-only mid-checkpoint); prefers `session_v2`, falls back to `session`, guards required columns. The same read once more across every watch dir gives the global scope (`allSessions`) | one subprocess per project, one for the union |
| Todos    | the same db's `todo` table: the count of open items per session arrives with the list (one query for the whole report) and badges each row; the items themselves are read for the session under the cursor and shown in the status line | one subprocess per scan, one per selection |
| Envs     | a `docker-compose.yml`/`compose.yaml` auto-detected at the project root (or any repo root); state via `docker compose ps --format json` | four stats + one subprocess |
| Notes    | the vault on disk, `.md` files by mtime, matched per project by name tokens or `docsMap` | a walk |

No API keys, no daemons, nothing written outside `~/.cache` (which it
does not even use yet). Missing feeds (no sqlite3, no opencode, no vault)
degrade the UI instead of failing it, and say so: a broken feed warns in
its pane title, a quiet one explains itself while it holds focus.

## Layout (lazygit-shaped)

```
obsidian › theme_fei4 · main · ●3 ↑2 · env 2/4 · 6 repos scanned 30s
╭▶ PROJECTS · 4 · ●3 ─────╮╭─ OPEN IN · theme_fei4 ─────────────────╮
│> 1 xxxx        ●↑✓      ││g lazygit · t terminal · c code · a …   │
│  2 yyyy        ✓✓+12    │╰────────────────────────────────────────╯
╰─────────────────────────╯╭─ COMMITS · 15 ─────────────────────────╮
╭─ REPOSITORIES ──────────╮│> 97d5fe3 Move files  santiago 10m┃     │
│> theme_fei4   ●3 ↑2     ││  82cde26 Audit +   santiago  16d┃      │
│  www          ✓         │╰────────────────────────────────────────╯
│  moodledev    ✗         │╭─ SESSIONS · repo · 20 · 3 open ────────╮
╰─────────────────────────╯│> Auditoría y curaduría ▸3 7d $0.10     │
                           │  Retomar pendientes  ▸1 2d $0.64       │
                           ╰────────────────────────────────────────╯
                           ╭─ NOTES · 5 · ⌕ ────────────────────────╮
                           │> 30 areas/area.linux.md      26m       │
                           │  80 resources/ia.linux.md     3d       │
                           ╰────────────────────────────────────────╯
⌕ linux · notes · 2/5
tab/h/l panes · ↑/↓ move · enter act · 1-9 project · s/d act · S scope · / filter
· ? help · r rescan · T theme · q quit
```

Above the frame, one breadcrumb line carries the whole context — project ›
repository · branch · what is dirty, ahead or behind, plus the env, repo and
note counts and the age of the scan — so no matter which pane holds focus,
the answer to "where am I?" is on screen once instead of five. Panes lost
that repetition from their titles when it arrived.

Below the frame, the status line carries the last action (four seconds, then
it steps aside), a filter in progress, or the open todos of the session under
the cursor; the legend is the key bar.

The legend along the bottom is conditional: `pgup/dn · home/end` appear only
while the focused pane has more rows than it can paint, and the ENV pane's
compose verbs replace the whole bar while it holds focus. A list that cannot
show every row grows a thumb down its right edge, so "3 of 20" never looks
like "there are only 3".

Six focusable panes — projects, repos, the conditional **ENV** (shown
only while the selected project has a docker environment), commits,
sessions, notes — tab, or a click, walks them, titles sitting on the
border, lazygit-style. A session that left work behind carries `▸N` on its
row, so choosing *which* one to resume does not need the cursor parked on
each one first. A cursor is always a *row*, never an index: the
item it stands for is resolved through the rows that are on screen, which
is what lets a filter narrow a pane without moving the selection or
letting a keystroke act on the wrong repository.

`/` filters the focused pane — characters in order, case and accents
ignored, and the rows keeping their own order (a session list is
chronology, a commit list is history, and a filter that sorted by score
would bury the newest thing under a better-looking one). One pane at a
time, because a cockpit with every pane half-empty makes "there is
nothing here" and "it did not match" indistinguishable; `tab` hands the
same word to the next pane, and a pane filtered down to nothing says so. OPEN IN is a one-line bar: the git-status row
menu's keys, same letters same meaning, acting on the selected repository.
Commits load on demand for that repo (`git log`, newest 15); sessions
narrow to it (falling back to the project's list) and carry their open
todos into the status line; `S` widens that pane to the whole project and
then to every watched folder at once — the newest sessions anywhere, each
row naming the project it came from, which is what "where was I working"
actually needs. Notes are the project's freshest vault files. Each project
row is a glyph per repository (`●` dirty `↑` ahead `↓` behind `✓` clean
`✗` unreadable, `+n` for what did not fit) instead of one count, and every
timestamp recedes when it is old — recent work is the thing worth seeing
first, so it gets the contrast instead of an equal share of grey.
The mouse works throughout: click focuses and selects, the wheel moves
the hovered list. `?` opens the full help.

Colours come from the active Omarchy theme
(`~/.local/state/omarchy/current/theme/colors.toml`): the accent paints
the active pane's border — whose corner turns into a `▶`, because colour
alone was not enough to find the focused pane in a frame of six — and the
selected-row bar is the theme's own selection colour, so the cockpit reads as part of the desktop. `T` reads
that file again after `omarchy theme set`, along with the configured
default agent, so neither needs a restart. The last seat is remembered
under the XDG state dir — project and repo by path (not index: the watched
list grows), the focused pane and each pane's row — so `SUPER+M` reopens
exactly where you left off, and clamps whatever no longer fits.

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
| `pgup/pgdn` `home/end` `ctrl+u/d` | page, jump to first/last, half page — by the rows the pane paints, not the window's |
| `/` | filter the focused pane · `tab` gives the word to the next pane · `enter` keeps it and hands the keys back · `esc` clears it |
| `enter`     | projects: to repos · repos: lazygit · commits: copy sha · sessions: resume · notes: open |
| `s` / `d`   | resume the marked session / open the marked note  |
| `S`         | the sessions pane: this repo · this project · everywhere |
| `1`–`9`     | jump to project N                                 |
| `r`         | rescan                                            |
| `T`         | reload the Omarchy theme and the default agent    |
| `?`         | full help overlay                                 |
| `q`         | quit                                              |
| mouse       | click focuses + selects a row; wheel moves the hovered list |

ENV pane keys (only while it is focused — there they beat the global
meanings: `r` restarts, it does not rescan; `d` scales down):

| key   | action (opens a terminal, on the selected service)   |
|-------|------------------------------------------------------|
| `u`   | `docker compose up -d`                               |
| `s`   | `docker compose stop` (selected service, or all)     |
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
states against a real bare remote (clean, dirty, ahead, detached, upstream,
and the one-call v2 read held against the three-call legacy one), feed
notes and errors, session SQL (prefix rules, quoting, `session_v2`
preference, schema guards), vault matching (tokens, mtimes), pane geometry
(the frame never exceeding the window), paging, the cursor-to-row mapping,
mouse routing and an end-to-end scan over a sandbox home.
