# Landfall mod for Claude Code

Keeps a Claude Code session current on its Landfall war room without ever taking a turn.

Requires Claude Code **2.1.287 or later** (mods are on by default from that version) and `landfall`
v0.9.0 or later; the room at a glance (status, people, Beacon) needs v0.9.6, and an older `landfall`
simply leaves it out. Tested with Claude Code 2.1.291.

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
