// @landfall/edge-bridge — the local Edge Bridge (feature 012, EDGE_AGENT_INTEGRATION
// §5.1). A teammate runs `landfall serve`; their favorite MCP-capable agent connects
// over stdio and gets incident-scoped tools. Every tool call narrates their edge
// investigation into the shared war room (presence heartbeat + timeline
// contributions), so the room shows who is doing what — live, with zero extra
// effort from the teammate. Read-only by default; actions are propose-only.
export * from './client.mjs';
export * from './narrate.mjs';
export * from './mcp.mjs';
export * from './tools.mjs';
export * from './link.mjs';
export * from './live.mjs';
