# `landfall` CLI

<img src="./docs/assets/voyage-storm-to-shore.gif" alt="A ship sailing through a storm to shore" width="100%" />

Join a [Landfall](https://landfalls.ai) war room from your own computer and have your
**favorite MCP-capable agent** (Claude Code, Codex, Cursor, VS Code, Claude Desktop,
Windsurf, …) become a **live investigator** in the shared room: its findings land on
the incident timeline in realtime, and it pulls everyone else's discoveries between
tasks.

## Installation

```
brew tap landfalls-ai/landfall
brew install landfall
```

Or without Homebrew, download a prebuilt binary from the
[releases page](https://github.com/landfalls-ai/landfall-cli/releases) — no toolchain
required, not even Go. Pick the archive matching your platform (`darwin`/`linux` ×
`amd64`/`arm64`; Windows is not supported yet), extract it, and put `landfall` on your
`PATH` (this intentionally does not pin a version — a hardcoded one goes stale on every
release with nothing to catch it, which is exactly what happened to the `v0.1.0` this
used to say):

```
curl -LO https://github.com/landfalls-ai/landfall-cli/releases/latest/download/landfall-cli_<os>_<arch>.tar.gz
tar xzf landfall-cli_<os>_<arch>.tar.gz
sudo mv landfall /usr/local/bin/
```

## `landfall install`: auto-register every coding agent on your machine

Once the `landfall` CLI itself is installed, one command detects which supported
coding agents are on **this machine** and wires each one up with its own **native,
global** MCP registration — no per-repo config file needed, and it works the same
whichever directory (or none at all) you're sitting in:

```
landfall login       # sign in once (needed the first time — install registers YOUR machine)
landfall install      # detects installed harnesses, lets you pick which to configure
```

| Harness | Mechanism |
|---|---|
| **Claude Code** | `claude mcp add-json … --scope user` (Claude Code's own user-scope MCP registration), **plus** the `landfall-edge-bridge` Claude Code plugin — see below |
| **Codex CLI** | `codex mcp add landfall -- landfall serve` (Codex's own MCP registration) |
| **VS Code** (Copilot/agent mode) | `code --add-mcp`, or a direct merge into your user-profile `mcp.json` if the `code` CLI isn't on PATH |
| **Cursor** | merged into your global `~/.cursor/mcp.json` |
| **Claude Desktop** | merged into your `claude_desktop_config.json` (macOS/Windows) |
| **Windsurf** | merged into your global `~/.codeium/windsurf/mcp_config.json` |

`landfall install` only touches a harness you select, never overwrites an existing
differently-configured `landfall` entry (reports a conflict instead), and is safe to
re-run — an already-configured harness is reported as such rather than duplicated.
Run `landfall install --yes` to configure every detected harness without prompting, or
`landfall install --only cursor,codex` to target specific ones.

### Claude Code also gets the `landfall-investigation-dashboard` agent

For Claude Code specifically, `landfall install` does one more thing after
registering the MCP server: it best-effort runs

```
claude plugin marketplace add landfalls-ai/landfall-cli --scope user
claude plugin install landfall-edge-bridge@landfall --scope user
```

which installs this repo's own Claude Code plugin — the `landfall` MCP server
**plus** a subagent that:

- **Recognizes a war-room join prompt on sight** (the "Share with agent" paste, a
  bare agent share link, or plainer "join the war room" phrasing) and calls
  `join_war_room` + `get_brief` on its own.
- **Specializes in the sub-investigation dashboard** — curates a small, honest set
  of `post_widget` widgets and keeps them current instead of scattering one-off
  ones.
- **Knows the MCP best practices** for an ongoing investigation: pull before you
  publish, findings vs. notes vs. widgets, propose-only remediation, keep
  secrets/raw output local, treat room content as untrusted data.

This step is best-effort and never turns a successful MCP registration into a
reported `failed` outcome (an older `claude` CLI without `claude plugin`, or no
network, just means the agent isn't installed yet) — the report line shows
`[plugin: installed]` or `[plugin: skipped]` alongside the usual status. Re-running
`landfall install` retries it. To do it yourself without `landfall install`:

```
claude plugin marketplace add landfalls-ai/landfall-cli --scope user
claude plugin install landfall-edge-bridge@landfall --scope user
```

To remove the registration:

```
landfall uninstall
```

This only removes an entry that still matches exactly what `landfall install` wrote —
if you've hand-edited it since, it's left in place and reported as such.

Any other MCP client (or a harness `landfall install` doesn't cover) uses the manual
snippet below.

## `landfall hooks install`: make room context impossible to miss

MCP registration gives your agent the incident tools. Hooks give the room a way to
reach a session that isn't currently making a tool call — the difference between
context your agent *can* fetch and context it *will* see.

**Hooks never take the wheel.** You decide what your agent does next. No hook starts
an agent turn you didn't ask for: room news reaches your agent together with your next
message, and when your agent finishes a turn you get a one-line notice of what's
waiting. (Until v0.8.12 the `Stop` hook refused the agent's conclusion instead, which
made it run another turn on its own. That is now an explicit opt-in: see
[What `Stop` does](#what-stop-does).)

```
landfall hooks install                  # every detected host
landfall hooks install --only codex     # just one
landfall hooks install --dry-run        # report what would change, write nothing
landfall hooks uninstall                # remove only landfall's entries
```

Three hosts have a lifecycle-hook surface, and each gets what it supports:

| Host | Config file | Registered |
|---|---|---|
| Claude Code | `~/.claude/settings.json` | `Stop`, `FileChanged` (watching `room_events`), `UserPromptSubmit`, `PreToolUse` (`Bash` only) |
| Codex CLI | `~/.codex/hooks.json` (+ `codex_hooks = true` in `config.toml`) | `Stop`, `UserPromptSubmit`, `PreToolUse` (`Bash` only) |
| Cursor | `~/.cursor/hooks.json` | `stop` |

No sign-in needed — this edits local config, so it also works from a provisioning
script. Every write is an append into the host's own hook list: your existing hooks
are preserved, re-running is a no-op, and an entry you've hand-edited since is
reported as a conflict rather than overwritten. `hooks uninstall` deletes only an
entry landfall wrote — its current form, or one an earlier version wrote — and
leaves Codex's `codex_hooks` flag alone: other hooks of yours may depend on it.

Cursor's entry carries `--host cursor`, because Cursor's hook contract is not the
one Claude Code and Codex share. Those two read a `Stop` notice as a
`{"systemMessage": "..."}` object on stdout (and an opted-in refusal as exit code 2
with the message on stderr); Cursor reads exactly one JSON object on stdout, and never
sees an exit code or stderr at all. Upgrading from v0.2.0 rewrites the older bare
`landfall hooks stop` entry in place — same position in your list, no duplicate.

**Upgrading Codex from v0.8.12 or earlier:** re-run `landfall hooks install`. It adds
the new `UserPromptSubmit` entry next to your existing ones (no conflict, no
duplicates); without it, a Codex agent only sees room news when it next calls a room
tool.

### What `Stop` does

Your agent finishes a turn. Meanwhile another investigator published the finding
that changes the answer — the war room saw it, your agent did not, because an MCP
session only learns things on a tool call it chose to make.

So on `Stop`, the hook asks the room daemon (and any `landfall serve` running in this
workspace) whether room events newer than your cursor exist. If they do, **you** get
one line from your host, and your agent stops exactly as it was going to:

```
Landfall: 2 updates in the room (1 finding from Maya · Codex, 1 message from Jhonny). They reach your agent with your next message.
```

Nothing is marked read. The updates stay queued, and the next message you send
delivers them to your agent as context (the `UserPromptSubmit` hook below), before the
model generates anything. If your agent calls a room tool first, it gets them there
instead. Either way nothing arrives twice, and once they're delivered the notice goes
quiet. If the room has quarantined something your agent relied on, the line says that
too. With nothing new, the hook exits silently and immediately.

| Host | What you see | What your agent sees |
|---|---|---|
| Claude Code | the line, as a hook message in the session | the updates with your next message |
| Codex CLI | the line, as a hook message in the session | the updates with your next message (needs the `UserPromptSubmit` entry: re-run `landfall hooks install` after upgrading) |
| Cursor | nothing (Cursor's `stop` can only answer by starting another agent turn) | the updates on its next room tool call |

**The old behaviour, opt-in only.** Set `LANDFALL_STOP_HOOK=block` in the environment
your agent host runs hooks with (export it in the shell you start `claude` or `codex`
from), or change the registered command to `landfall hooks stop --block`. Then `Stop`
refuses the conclusion while room context is unread ("Do not conclude yet — N
update(s) ..."), your agent reads it and continues on its own, and whatever the
refusal reported is marked consumed. You are never stuck: a second `Stop` on an
unchanged room goes straight through, and a host that re-runs the hook after a refusal
(`stop_hook_active`) is let through regardless. Any other value of
`LANDFALL_STOP_HOOK`, or none, is the notice. `landfall hooks install` always writes the
plain command; an entry you've changed to `--block` is reported as a hand-edited
conflict on the next install and left exactly as you wrote it.

On Cursor, the opted-in refusal arrives by a different verb. Cursor's `stop` is a
notification — it cannot refuse a conclusion — but it can hand back a
`followup_message`, which Cursor submits as the next user message and which
continues the agent loop. Termination is just as bounded — what was reported is
consumed, an interrupted or errored turn is left alone, and Cursor caps
auto-followups at five per turn regardless.

### What `FileChanged` + `UserPromptSubmit` do

`Stop` tells you when news is waiting. This pair is what hands it to your agent when
it is **idle** — not calling tools, so the in-band block on a tool result can't reach
it. (`UserPromptSubmit` runs on Claude Code and Codex; the `FileChanged` wake is Claude
Code's alone, and Codex's `UserPromptSubmit` reads the live room directly.)

It takes two hook events, because no single one can do the job:

- **`FileChanged` can watch, but not speak.** `landfall serve` appends a marker line
  to `.landfall/room_events` the moment its queue goes from empty to non-empty, and
  the watcher fires — but Claude Code discards this event's output entirely. So the
  wake *stages* the digest and prints a one-line nudge to your terminal. It
  deliberately consumes nothing.
- **`UserPromptSubmit` can speak, but never learns the room changed.** It fires on
  every message you send, so it picks up the staged digest and hands it to the
  session as `additionalContext` — before the model generates anything.

**The honest limit:** nothing can wake a genuinely idle Claude Code session with zero
human action, because nothing server-side can push into one. The earliest moment an
idle session can act on room context is your next message, and that is exactly when
this delivers it — no action beyond what you were already about to do.

The cursor only advances once the digest has actually been handed over, so context is
never consumed by a hook that could not deliver it.

The marker is a **doorbell, not a mailbox**: it carries a timestamp, a pid and a count,
never a finding, a name or an incident id. The digest itself is staged next to the
sockets outside your repo (`0600`), so nothing from the war room lands in a directory
that gets grepped, backed up and occasionally committed. `.landfall/` ignores itself
(it contains a `.gitignore` of `*`) rather than us editing a `.gitignore` you own.

**How a hook reaches a serve process.** A hook is a separate, short-lived process
and cannot see `landfall serve`'s memory, so `serve` binds a local query socket at
`$XDG_RUNTIME_DIR/landfall/<workspace-hash>/<pid>.sock` (macOS:
`~/.local/state/landfall/run/…`; Windows: a per-user named pipe). No new daemon —
it is the serve process you already run, made answerable. Two agent windows on one
repo are two sockets with two cursors, and a hook unions them, so it can over-report
a sibling window's context but never miss your own. Nothing else on your machine is
listening: see [Guarantees](#guarantees).

## `landfall hooks policy`: what your machine may report, in writing

The `PreToolUse` hook can offer to open a war room when you start poking at
production. It reports a **classification** — never your command line — and only
after you say yes.

**It does nothing until you write a policy file.** There is no default allow-list
and no heuristic: a command is only ever recognized because you wrote a rule that
recognizes it.

```
landfall hooks policy --init     # write a starter ~/.config/landfall/prod-policy.json
landfall hooks policy            # print every rule AND the exact payload it would send
```

```json
{
  "version": 1,
  "rules": [
    {
      "id": "kubectl-prod",
      "description": "kubectl aimed at the production cluster",
      "command": "kubectl",
      "allOf": ["--context=prod-us-east-1"],
      "noneOf": ["--dry-run"],
      "category": "kubernetes",
      "entityHints": ["prod-us-east-1", "payments-api"]
    }
  ]
}
```

A rule matches when `command` equals the program name and every string in `allOf`
appears literally in the command line (and none of `noneOf` does). `allOf` entries
are **text, not patterns** — `prod.*` matches the characters `prod.*`.

The part worth checking for yourself: **`category` and `entityHints` are the entire
payload**, and both are literals you typed above. Nothing is extracted from the
command line — there is no parser that could pull a namespace, a hostname or a
flag out of it. `landfall hooks policy` prints the resulting body per rule, so the
complete set of values this machine can ever send is something you can read in one
screen. `category` must be one of `kubernetes`, `cloud`, `database`, `logs`,
`deployment`, `network`, `other`; hints must be bare identifiers (no whitespace, no
shell metacharacters), which is checked when the file loads rather than when you
are mid-incident.

On a match you get a one-key prompt on your terminal, and **`y` is the only answer
that sends anything**. No terminal (CI, a detached session), no answer within 30
seconds, any other key, no cached session, an organization that hasn't enabled
auto-attach — every one of those reports nothing. The hook always exits 0: it never
blocks the command you were running. Commands that match no rule, and every tool
that isn't a shell command, do nothing at all.

Your organization must also switch this on (it is off by default, admin-gated,
server-side), so both ends have to agree before a single intent is accepted.

## The room daemon: one room per machine, a cursor per reader

Since v0.8.0 `landfall serve` is a thin front end. The first time an agent on your machine
joins a room, `serve` starts `landfall daemon` (detached, one per user) and attaches to it as
a named reader; a second agent, a second window, or a helper your agent spawns attaches to the
same room instead of joining it again. The daemon holds the room's live connection, one presence
identity for the machine, and a **separate reading position for every reader**, including one
for you, the person at the terminal. What your agent's helper reads is the helper's business;
your status line and your prompt digest count what *you* have not been shown.

```
landfall rooms              # which rooms this machine has open, and who is reading each
landfall rooms --json
landfall daemon             # run it in the foreground (serve normally spawns it detached)
landfall daemon stop
```

The daemon exits by itself a minute after its last agent reader detaches. Its state (rooms with
their tokens, readers and cursors) lives in `<runtime dir>/daemon/state.json`, mode 0600, where
`<runtime dir>` is `$XDG_RUNTIME_DIR/landfall` or `~/.local/state/landfall/run`. A `stop` or a
signal keeps that state so a restart rejoins; an idle exit forgets it. If the daemon cannot run
(`LANDFALL_DAEMON=0`, or a runtime path too long to bind a socket), `serve` says so once and
behaves exactly as before: one process, one cursor. Since v0.8.7 the hooks and `landfall status`
read the daemon directly, as your own reader, in one call; a front end still binds the
per-process socket an older binary's hooks look for, and a `serve` running without the daemon
is still found through it.

### The working-directory hold

Your agent must not share your code into the room unless you say so. At attach, `serve` tells
the daemon what your working directory looks like: tracked file paths, recent commit hashes,
the directory and package names. A `share_with_room` whose text names any of them is **held**
in this checkout's queue instead of published; the agent is told so in the tool result, and
`landfall status` shows `· N held`. Only you release it:

```
landfall held                 # what is held for this checkout's room, and what each one names
landfall allow-cwd            # let working-directory content into the room; releases everything held
landfall held drop <id>       # discard one
```

The allowance is per room and survives a daemon restart. There is no MCP tool for any of this.

### A share the room refuses

`share_with_room` returns at once and publishes in the background, so the agent is told
"shared" before the room has seen it. When the room then refuses it outright (a malformed
widget, a claim for a room that has closed, any other 4xx), the hand-off is not retried: it is
kept in this checkout's queue with the room's reason, your agent's next room tool call opens
with one line per refusal, for example

```
Your earlier share did not reach the room: engagement is closed; admission is frozen (it began "the root cause is the rollback at 14:20").
It will not be retried. Share it again, corrected, if the room still needs it.
```

and `landfall status` shows `· N shares refused` for an hour. A room that is only
unreachable, slow, rate-limiting or failing on its own side is different: those hand-offs stay
queued and publish when it answers.

## Any other MCP client, or a global CLI install: sign in once, then join with no share link

Configure the MCP server ONCE, with no tokens or IDs:

```json
{
  "mcpServers": {
    "landfall": { "command": "landfall", "args": ["serve"] }
  }
}
```

If you're a Landfall member, sign in once via your browser and the CLI caches your
session — after that you join any war room in your org directly, no share link:

```
landfall login       # opens your browser; you sign in however your org normally does
landfall logout      # clear the cached session (and revoke it server-side)
# then, with a plain incident URL or LANDFALL_SLUG + LANDFALL_INCIDENT set:
landfall serve       # joins as your authenticated identity — no share link needed
```

The signed-in session renews itself silently in the background — no re-prompt, no
flag to turn on. `landfall login`'s 1-hour access token is exchanged for a fresh one
automatically the moment it's needed, for as long as that stays valid (30 days,
sliding). You only see the browser again if that itself expires, or is revoked —
explicitly by `landfall logout`, or organization-wide by an admin's "revoke all
sessions".

Non-members / guests keep using the single-use **share link** below. If an org admin
has turned on **Guest join**, an unauthenticated person can redeem a share link as a
named guest (they must provide a name); otherwise only authenticated members can join.

Then, when an incident happens, any room member clicks **Share with agent** in the war
room and sends you the instruction block. Paste it into your agent — it contains a
single-use share link like

```
https://api.landfall.example.com/o/acme/incidents/inc-4821/agent?ticket=…
```

and the agent calls `join_war_room(shareUrl)`. The bridge redeems the ticket for a
short-lived **edge session scoped to exactly that incident** (8h, nothing else), joins,
and starts investigating. The URL's ticket is single-use and expires in 15 minutes;
possessing the URL without it grants nothing.

You can also pre-join from the terminal:

```
landfall serve --link "https://…/agent?ticket=…"   # or LANDFALL_LINK env
landfall join  "https://…/agent?ticket=…"          # presence-only keep-alive
```

## The live-investigation loop

- **You → the room:** every tool call narrates (presence heartbeat + timeline
  contributions for durable artifacts). `post_finding` / `propose_action` appear in the
  main incident window **instantly** (realtime fan-out), attributed to you + your agent.
- **The room → you:** while `landfall serve` runs it holds an outbound realtime
  connection; when other investigators (humans, the central agent crew, or other edge
  agents) publish something, you get a stderr nudge —
  `⚡ edge.finding from dana · Codex: "origin pool unhealthy" — call get_updates.`
  Your agent gets it **in-band**: the next tool call it makes — any tool — comes back
  with the new context appended, so it cannot miss what the room found while it was
  busy elsewhere.

  ```
  Finding posted to the war room.

  ⚠ 1 update(s) from other investigators since your last tool call:
  #42 edge.finding [dana · Codex] — origin pool unhealthy
  ```

  The block is bounded: past a handful of events only the newest are spelled out and
  the rest are counted, with the `get_updates sinceSeq=…` that fetches them in full.
  Delivery advances the same durable cursor `get_updates` reads, so nothing arrives
  twice — and with the realtime connection unavailable the bridge still works exactly
  as before, on cursor pulls alone.

- **The room's scope → you:** a human can pin what the investigation is about (a repo,
  an architecture component, a telemetry source, a time window, an artifact, a past
  incident), write standing instructions for the organization, and say what a run
  should focus on. All of it rides on the brief, so `get_brief` reads:

  ```
  Scope pinned to this room — read within it unless the person says otherwise:
    repo: landfalls-ai/landfall (pinned by Dana)
    component: checkout-api (pinned by Alex)

  Focus: did the 10:02 rollback do it? (asked by Dana)

  Organization instructions (version 4):
    Never page the on-call before 07:00.
    For checkout-api:
      Its cache is warmed by a cron at :05.
  ```

  Read-only on purpose. There is no tool to pin, unpin or steer: the server treats all
  three as human-only timeline events, because the room's scope is the people's call,
  not their agents'.

- **Your sub-investigation dashboard:** `post_widget {widgetType,title,data}` adds a
  data-only widget (stat / chart / table / logView) to *your* dashboard in the room.
  Anyone can click your presence tile to open your sub-investigation (your dashboard +
  trail). Data-only by design — you pass the values you computed; no code runs.
  With the background bridge the same widget rides `share_with_room`'s `widget`
  argument. Either way the payload is checked on your machine against the room's shape
  for that type (required keys, no unknown keys, every `t` an ISO-8601 time, every `v`
  a finite number, at most 8 series of 5000 points) before anything is sent, and a
  mismatch comes back in the same tool result with what to fix:

  ```
  Nothing was shared: the "chart" widget does not match the shape the room renders.
  - chart.series[0].points[0].t must be an ISO-8601 time such as 2026-09-28T14:05:00Z (got "14:05")
  Fix these and share it again. describe_widget_types lists every type's shape.
  ```

  The room still checks every widget itself; if it refuses one this check passed, the
  refusal reaches your agent as described under "A share the room refuses".

Tools: `join_war_room`, `get_updates`, `get_brief`, `read_timeline`, `search_context`,
`post_finding`, `post_widget`, `note`, `upload_artifact`, `read_artifact`, `propose_action`,
`record_activity`, `get_signal_catalog`, `query_signals` (the incident's connected telemetry sources
and a read against one of them, through Landfall's credential proxy).

## Env-config setup

```json
"env": {
  "LANDFALL_BASE_URL": "https://api.landfall.example.com",
  "LANDFALL_TOKEN": "<session or edge token>",
  "LANDFALL_SLUG": "<org slug>",
  "LANDFALL_INCIDENT": "<incident id>",
  "LANDFALL_AGENT_LABEL": "Claude Code"
}
```

`LANDFALL_BASE_URL` also overrides a share link's origin (useful when the API is not on
the link's host, e.g. local dev).

## Direct CLI
```
landfall serve [--link URL]   # (default) join + expose the incident MCP tools over stdio
landfall join [URL]           # join + keep presence alive (no MCP) — Ctrl-C to leave
landfall note "origin pool is unhealthy"   # post a one-off finding
landfall leave                # leave the incident
landfall install [--yes] [--only <ids>] [--dry-run]   # register this machine's coding agents
landfall uninstall [--yes] [--only <ids>]              # remove that registration
landfall hooks install [--only <ids>] [--dry-run]      # register lifecycle hooks
export LANDFALL_STOP_HOOK=block                        # opt in: Stop refuses a conclusion (default: a notice)
landfall hooks uninstall [--only <ids>]                # remove only landfall's hook entries
landfall hooks policy [--init]                         # print (or scaffold) the prod allow-list
```

## Guarantees
- **Read-only by default**; `propose_action` is propose-only (a human approves — you
  cannot execute). humanActorId comes from your verified session, never the payload.
- An edge session token works ONLY on its one incident — default-deny everywhere else.
- Join tickets are single-use, short-lived, and bound to tenant + incident + member.
- **No network listener.** The bridge speaks MCP over stdio and opens the realtime
  connection outbound; there is no port, and nothing another host can reach. `landfall
  serve` does bind one local **filesystem** socket — `0600`, inside a `0700` directory,
  readable by your user account alone — so lifecycle hooks can ask *"what room events
  have I not seen?"*. It answers exactly three read/cursor operations (`status`, `peek`,
  `consume`) and offers **no way to act in the war room**: nothing that posts, proposes,
  or hands back your session token.
- Presence/summary/sub-tabs are projected server-side from what the bridge posts;
  nothing here bypasses the war room's approval gate.

## Releasing

See [RELEASING.md](./RELEASING.md).

## Development

```
go build ./...
go test ./...
```

Go 1.24+, [Cobra](https://github.com/spf13/cobra) for the command tree and
[Viper](https://github.com/spf13/viper) for the persisted instance config. The binary
builds from `./cmd/landfall`; everything else lives under `internal/`. Releases are
cross-compiled by [goreleaser](https://goreleaser.com) — see
[RELEASING.md](./RELEASING.md).

`landfall` was a Node.js/ESM CLI through `v0.5.0` and was rewritten in Go for `v0.6.0`
(1:1 behavioral parity — same commands, same flags, same exit codes, same stdout/stderr
split). It ships as a single static binary now, so it no longer needs Node installed at
all. The Node implementation (`src/`, `test/`, `bin/landfall.mjs`, `package.json`) was
deleted in `v0.6.2` once the Go port had shipped and been verified.

Comments throughout `internal/` still cite that implementation by path and line
(`bin/landfall.mjs:487`, `src/hooks/stop.mjs`, …) to explain *why* a behavior is the way
it is — several are load-bearing quirks that would otherwise look like bugs. Those paths
no longer exist at `HEAD`; read them against tag **`v0.5.0`**, the last Node release:

```
git show v0.5.0:bin/landfall.mjs
git show v0.5.0:src/hooks/stop.mjs
```
