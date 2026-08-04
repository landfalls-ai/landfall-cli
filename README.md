# `landfall` CLI

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

Or without Homebrew, install directly from a tagged release:

```
npm install -g "github:landfalls-ai/landfall-cli#v0.1.0"
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

```
landfall hooks install                  # every detected host
landfall hooks install --only codex     # just one
landfall hooks install --dry-run        # report what would change, write nothing
landfall hooks uninstall                # remove only landfall's entries
```

Three hosts have a lifecycle-hook surface, and each gets what it supports:

| Host | Config file | Registered |
|---|---|---|
| Claude Code | `~/.claude/settings.json` | `Stop`, `FileChanged`, `PreToolUse` (`Bash` only) |
| Codex CLI | `~/.codex/hooks.json` (+ `codex_hooks = true` in `config.toml`) | `Stop`, `PreToolUse` (`Bash` only) |
| Cursor | `~/.cursor/hooks.json` | `stop` |

No sign-in needed — this edits local config, so it also works from a provisioning
script. Every write is an append into the host's own hook list: your existing hooks
are preserved, re-running is a no-op, and an entry you've hand-edited since is
reported as a conflict rather than overwritten. `hooks uninstall` deletes only an
entry still byte-identical to what was written, and leaves Codex's `codex_hooks`
flag alone — other hooks of yours may depend on it.

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
landfall logout      # clear the cached session
# then, with a plain incident URL or LANDFALL_SLUG + LANDFALL_INCIDENT set:
landfall serve       # joins as your authenticated identity — no share link needed
```

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

- **Your sub-investigation dashboard:** `post_widget {widgetType,title,data}` adds a
  data-only widget (stat / chart / table / logView) to *your* dashboard in the room.
  Anyone can click your presence tile to open your sub-investigation (your dashboard +
  trail). Data-only by design — you pass the values you computed; no code runs.

Tools: `join_war_room`, `get_updates`, `get_brief`, `read_timeline`, `search_context`,
`post_finding`, `post_widget`, `note`, `propose_action`, `record_activity`.

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
landfall hooks uninstall [--only <ids>]                # remove only landfall's hook entries
landfall hooks policy [--init]                         # print (or scaffold) the prod allow-list
```

## Guarantees
- **Read-only by default**; `propose_action` is propose-only (a human approves — you
  cannot execute). humanActorId comes from your verified session, never the payload.
- An edge session token works ONLY on its one incident — default-deny everywhere else.
- Join tickets are single-use, short-lived, and bound to tenant + incident + member.
- Stdio only — the bridge never listens on a public interface; the realtime connection
  is outbound.
- Presence/summary/sub-tabs are projected server-side from what the bridge posts;
  nothing here bypasses the war room's approval gate.

## Releasing

See [RELEASING.md](./RELEASING.md).

## Development

```
npm install
npm test
```

No build step — plain ESM, Node ≥22.
