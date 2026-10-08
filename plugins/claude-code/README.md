# Landfall mod for Claude Code

Keeps a Claude Code session current on its Landfall war room without ever taking a turn.

Requires Claude Code **2.1.287 or later** (mods are on by default from that version) and `landfall`
v0.9.0 or later; the room at a glance (status, people, Beacon) needs v0.9.6, and an older `landfall`
simply leaves it out. The components below need v0.10.0. Tested with Claude Code 2.1.293.

## Install

```sh
landfall hooks install
```

On Claude Code 2.1.287+ that installs this mod (`landfall@landfall`) beside the hooks. By hand:
`claude plugin marketplace add landfalls-ai/landfall-cli`, then `claude plugin install landfall@landfall`.

## What it does

| Where | What the person sees | When |
|---|---|---|
| Band above the prompt | `Landfall · <room> · 2 new`, the newest line, and three keys: **1** catch up, **2** show the room, **3** later | as soon as the room daemon has news, idle or not |
| Toast | a chat message that names someone | once per message |
| Status line | the room at a glance: `🔴 Landfall 166 · investigating · SEV2 · 4 here · Beacon investigating · 2 new · 1 held`; the dot turns 🟡 once mitigated, 🟢 once resolved | always, while the folder is in a room |
| Hint line under the prompt | who else is here and where: `Here: bob (Claude Code) · carol (war room)` | while you are not typing |
| Toast | `<room> is now resolved`, `<room>: Beacon concluded` | once per change |
| Prompt box | a dim "Catch me up on what changed in the war room." after a turn, Tab to take it | only when the room has news |
| `/room` (or **2**) | the room at a glance (status, the leading theory, everyone in it with their agents and what each is doing), then every untold line, in a pane, with no model call | when typed, even mid-turn |
| Band, second row | `Chart ready: <metric> · 4: add it to the room` | after your agent reads a metric through the room (`query_signals`) |
| `/chart` | the same, as a command | when typed |

**4 add it to the room** puts that read on the war room's canvas as a chart, on your own
dashboard, with no model call and nothing more to approve: the mod runs `landfall chart`, which has
your room daemon make the same read with the room's own session and queues the chart for your
session to publish, so nobody copies a data point. (It is a local command rather than an MCP call
because Claude Code puts a plugin's MCP call made from a key press to its permission dialog.) Your
agent can do the same through `share_with_room` with `fromQuery` when you ask it to chart something.

**The room at a glance** comes from the war room's context frame, which your room daemon already
reads and caches: it never waits on the network to draw, refreshes the frame in the background
within 30 s, and at once when the room's status changes or someone joins or leaves. "Here" means
active in the last 45 s; a browser tab in the war room counts as being there.

**1 catch up** only drafts the prompt; the person sends it. The agent hears the room on the person's
next message, exactly as it did with the settings hooks: the mod runs `landfall hooks
user-prompt-submit --from-mod` and attaches what it returns as context only Claude reads.

### The room's components (mod 0.5.0, landfall v0.10.0)

Every one draws in the terminal and, in the Code tab of the Claude desktop app, as native cards,
pills and vector charts (VS Code and the mobile app draw compact versions). A key or button only
drafts a prompt or runs a `landfall` command: nothing starts a turn, nothing changes the incident's
status, severity or focus (those stay in the war room), and no button makes an MCP call.

| Where | What the person sees | Runs |
|---|---|---|
| Band | **Your vote is waiting**: the staged finding, who staged it, positions so far, time left; **5** corroborate, **6** contest with a reason, **7** read the evidence, **8** later. A toast for each new request. On a wide fullscreen terminal the full card opens beside the conversation. Never offered for your own finding | `landfall vote` (as you, never as your agent) |
| Band | `◆ Beacon step 6 · comparing 5xx per target group`; after a run, its conclusion with **9** to read it | watch |
| Band | `Lines: eu-west-1 5xx (you) · origin pool (alice)` | watch |
| `/vote` | the full vote card | `landfall vote` |
| `/incidents` | your organization's open incidents, your rooms first; Enter joins from this session, **b** brief only, **o** open in the browser | `landfall incidents`, `landfall join --incident` |
| `/who` | everyone in the room, where they work, and each person's latest contribution with its state; press to quote it | watch |
| `/wall` | the room's canvas in the shared arrangement, every widget type drawn natively, data read as you | `landfall wall` |
| `/lb [name]` | load balancers in the room's AWS connection: targets by zone and health, 5xx share per target group by minute; **s** shares it as a chart | `landfall lb` |
| `/timeline [kind]` | the room oldest first, filters for findings, status, Beacon, people; press to quote | `landfall timeline` |
| `/topology` | the room's graph widget, trust as line style | `landfall wall` |
| `/lines [text]` | claim or release a line of investigation | `landfall lines` |
| `/comms` | stakeholder updates and whether they were approved and sent (read only) | `landfall comms` |
| `/brain [text]` | past incidents like this one from the Company second brain | `landfall brain` |
| Tool rows | your agent's Landfall tool calls as short Landfall rows (a read as a sparkline, a share with what became of it) | none |
| Context | the room's focus and scope as one context block for your agent | watch |
| Sound | one soft chime when your incident becomes SEV1 or a vote waits on you; off until **Sound cues** is switched on in `/config`; macOS only | none |
| `/landfall-sound` | whether the chime is on, and how to change it | none |

### Layout

`hooks/register.js` owns the session: the single `session.start`, the one `landfall watch` stream
and the band. Each file in `hooks/components/` is one feature with the same four exports
(`install`, `band`, `onSnapshot`, `start`); `register.js` calls each by name and hands it an `io`
object in place of `$`, because `claude plugin validate` follows `$` only into functions declared
in the same file. `hooks/kit.js` draws Landfall's pieces for each surface; `hooks/views.js` holds
the pure chart and graph drawings.

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
