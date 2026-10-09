# Landfall mod for Claude Code

Keeps a Claude Code session current on its Landfall war room without ever taking a turn.

Requires Claude Code **2.1.287 or later** (mods are on by default from that version) and `landfall`
v0.9.0 or later; the room at a glance (status, people, Beacon) needs v0.9.6, and an older `landfall`
simply leaves it out. The console needs the `landfall` that ships `whoami`, `login --json`, `brief`,
`artifacts`, `artifact`, `wall --person` and `open`. Tested with Claude Code 2.1.294.

## Install

```sh
landfall hooks install
```

On Claude Code 2.1.287+ that installs this mod (`landfall@landfall`) beside the hooks. By hand:
`claude plugin marketplace add landfalls-ai/landfall-cli`, then `claude plugin install landfall@landfall`.

Already have an older mod (it answers `/room`, and `/landfall` is not a command)? The console arrived in
0.5.0: `brew upgrade landfall`, then `claude plugin marketplace update landfall` and
`claude plugin update landfall@landfall`, and start a new session. `claude plugin list` shows your version.

## What it does

One command, `/landfall`, and one pane, the console. Keys draft prompts or run the `landfall` CLI;
nothing starts a turn, nothing makes an MCP call, nothing changes the incident's status, severity or
focus (those stay in the war room).

| Where | What the person sees | When |
|---|---|---|
| Band above the prompt | `◆ Landfall 168 · cloudfront-5xx-high · 3 new  ● vote waiting 4m 10s  ● agent ✓`, the vote's statement and keys (**5** corroborate, **6** contest, **7** read the evidence, **8** later), Beacon's step or conclusion (**9**), the newest line or `New on the wall` (**w**), then **1** catch up, **2** open the console, **3** later, **4** add chart | only while something needs the person |
| Status line | `🔴 Landfall 168 · investigating · SEV2 · 4 here · Beacon investigating · 3 new`; 🟡 mitigated, 🟢 resolved, hollow `○` while reconnecting | always, while the folder is in a room |
| Hint line (terminal) | `Here: carol (war room) · bob (Claude Code)` | while you are not typing |
| Prompt box | a dim "Catch me up on what changed in the war room." after a turn, Tab to take it | only when the room has news |
| Toasts | a mention, a status change, Beacon concluding, a vote asked of you, a new widget or artifact; 80 characters at most | once each |
| Sound | one soft chime when your incident becomes SEV1 or a vote waits on you; off until **Sound cues** is switched on in `/config`; macOS only | once per event |

### The console

`/landfall [tab] [text]`. A segmented switcher is pinned at the top of the pane (the console moves
its own body under it); every tab stays live while the console is open, and a tab you are not
looking at keeps its count on the switcher. Closing the console (Esc, the mark) stops every read.

| Tab | Shows | `/landfall …` | Reads |
|---|---|---|---|
| Home | your vote, Beacon, who is here and what each last shared, the wall's tiles, load balancers, the latest events; on a short pane the last blocks give way and say where they went | `home`, `overview` | the watch stream, and the Wall, Load balancers and Timeline reads |
| Vote | the finding the room waits on your position for; **c** corroborate, **x** contest with a reason, **l** later | `vote` | watch, `landfall vote` as you |
| Context | the room's shared context and its artifacts; **a** adds the shared context to your agent's conversation (one appended note, no turn), Enter adds an artifact | `context`, `context artifacts` | `landfall brief`, `landfall artifacts`, `landfall artifact` |
| Wall | the shared wall, a person's dashboard, or yours; **a** ask, **n** next widget, **d** next dashboard | `wall`, `wall <name>`, `wall mine`, `topology` | `landfall wall [--person]`, every 15 s |
| People | who is here, their lines and latest contribution; Enter opens a person's investigation; **c** claim a line, **e** release | `people`, `who`, `people <name>` | watch |
| Timeline | the room oldest first, kind chips, Enter quotes a row | `timeline [findings\|status\|beacon\|people\|other]` | `landfall timeline`, on each new event |
| Load balancers | targets by zone and health, 5xx per target group; **s** share as a widget, **a** ask | `lb [name]` | `landfall lb`, every 60 s |
| Incidents | open incidents, your rooms first; Enter joins (the shared context is added to your conversation), **b** brief only, **o** open in browser | `incidents` | `landfall incidents`, every 30 s |
| More | Comms (stakeholder updates, read only) and Brain (the company second brain) | `more`, `comms`, `brain [text]` | `landfall comms`, `landfall brain` |

Three words act without opening a tab: `/landfall lines <label>` claims a line, `/landfall chart` adds
your agent's latest metric read to the room as a chart, `/landfall sound` says whether the chime is on.
Where no pane can be placed (`claude -p`), `/landfall <tab>` answers the tab in text.

**The way in.** `/landfall` alone lands on Home in a room. Not in a room, it lands on Incidents as
the incident picker, or, with no sign-in (`landfall whoami --json`), as the sign-in state: **s** runs
`landfall login --json`, the CLI's own browser handoff (the mod never opens a browser), the console
waits with the link and a clock, **x** cancels (ending the CLI), and once you are in it goes back to
the picker. Enter on an incident runs `landfall join --incident`, adds the shared context to your
conversation and switches to Home; your agent adopts the room on its next tool call.

A failed read keeps what the tab showed and says `stale · updated 40s ago · <why>`; while the
room's connection is down the header and the band say **Reconnecting to the room…**.

### Layout

`hooks/register.js` owns the session: the single `session.start`, the one `landfall watch` stream
and the band. `hooks/console.js` owns the console: the `/landfall` command, the pane, the pinned
switcher (`ui.scroll`), Home, the footer and keys row, and the entry flow (`hooks/components/signin.js`
is the sign-in state machine). Each file in `hooks/components/` is one feature with the same five
exports (`install`, `band`, `onSnapshot`, `start`, `tick`), and each console tab adds `tab`, `badge`,
`warm` and optionally `keys`, `footer`, `refresh` and `text`; register.js and console.js call each by
name and hand it an `io` object in place of `$`, because `claude plugin validate` follows `$` only
into functions declared in the same file. `hooks/kit.js` draws Landfall's pieces for each surface;
`hooks/views.js` holds the pure chart and graph drawings; `hooks/live.js` the bookkeeping every live
read shares.

## Claude desktop

The Code tab of the Claude desktop app runs this same mod. Turn it on under **+**, **Add plugins**
(it may be listed as Disabled), and in a new session send any message before `/landfall` (a new
session knows a mod's commands once it has started). Known issue with the desktop app's Claude Code
2.1.293: a Code session can still miss the installed mod; until the app fixes it, start Claude with
`open -a Claude --env CLAUDE_CODE_PLUGIN_DIRS="$(ls -d ~/.claude/plugins/cache/landfall/landfall/*/ | sort -V | tail -1)"`.
What differs there:

- The console draws as native cards and real buttons instead of key hints. The keys in the tables above
  are the terminal's; on desktop you press the button.
- The dock is narrow, so the tab switcher shortens names by a fixed ladder until the row fits: Load
  balancers becomes `LB`, then Context `Ctx`, Timeline `Time`, Incidents `Inc` and People `Ppl`. It never
  wraps, and the active tab keeps its fill. The pane title always says the full name (`Landfall · People`).
- A drawing that shows hover titles sits in the desktop's own white frame, and the engine passes a hook
  no theme, so the mod paints its own background and text colors in the SVG and switches them with
  `prefers-color-scheme`.
- The sound cue is macOS only, as above.

Claude desktop chat (not the Code tab) has no mods. There Landfall comes through the hosted connector, a
remote MCP server at `/o/<org>/mcp`, whose tools render as MCP Apps (incidents, room, canvas).

## How it fits with `landfall hooks install`

Both stay installed. At session start the mod sets `LANDFALL_MOD=claude-code`; Claude Code passes it
to every settings hook and status line it starts after that, and Landfall's own `stop`,
`file-changed` and `user-prompt-submit` hooks and `landfall status` then do nothing, so the session
hears the room once. `pre-tool-use` (the opt-in intent policy) is unaffected. Codex, Cursor, older
Claude Code and organizations that only allow managed mods keep the settings hooks as before.

## Try a checkout without installing

```sh
claude --plugin-dir /path/to/landfall-cli/plugins/claude-code
```

then paste a war-room share link. `claude plugin validate plugins/claude-code` lists every event the
mod handles and every call it makes: it starts `landfall` and nothing else (no network, no file
access of its own).

## Test

```sh
cd plugins/claude-code && claude plugin test
```
