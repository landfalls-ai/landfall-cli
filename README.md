# @landfall/edge-bridge — `warroom` CLI

Join a Landfall war room from your own computer and have your **favorite MCP-capable
agent** (Claude Code, Cursor, …) narrate its local investigation into the shared room —
with zero extra effort. (Feature 012; `EDGE_AGENT_INTEGRATION.md` §5.1.)

## Seamless setup (the whole thing)
Point your agent's MCP config at `warroom serve`:

```json
{
  "mcpServers": {
    "warroom": {
      "command": "warroom",
      "args": ["serve"],
      "env": {
        "WARROOM_BASE_URL": "https://api.landfall.example.com",
        "WARROOM_TOKEN": "<your session token>",
        "WARROOM_SLUG": "<org slug>",
        "WARROOM_INCIDENT": "<incident id>",
        "WARROOM_AGENT_LABEL": "Claude Code"
      }
    }
  }
}
```

That's it. Your agent now has incident-scoped tools (`get_brief`, `read_timeline`,
`search_context`, `post_finding`, `note`, `propose_action`, `record_activity`), and
**every call narrates to the war room**: a presence heartbeat ("who's doing what")
plus a timeline contribution for durable artifacts (findings, proposals). The room
shows you present with your agent and a live activity line as you investigate.

## Direct CLI
```
warroom serve   # (default) join + expose the incident MCP tools over stdio
warroom join    # join + keep presence alive (no MCP) — Ctrl-C to leave
warroom note "origin pool is unhealthy"   # post a one-off finding
warroom leave   # leave the incident
```

## Guarantees
- **Read-only by default**; `propose_action` is propose-only (a human approves — you
  cannot execute). humanActorId comes from your session, never the payload.
- Stdio only — the bridge never listens on a public interface.
- Presence/summary/sub-tabs are projected server-side (feature 006) from what the
  bridge posts; nothing here bypasses the war room's approval gate.
