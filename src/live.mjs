// live.mjs — the bridge's outbound realtime connection (feature 021,
// EDGE_AGENT_INTEGRATION §8.1). While `warroom serve` runs, it subscribes to the
// incident's Socket.IO room so the teammate is NUDGED (stderr) the moment other
// investigators publish shared context; the agent then pulls it with
// `get_updates` at its next task boundary (pull stays authoritative — an MCP
// host controls its own context). Best-effort by design: if the gateway is
// unreachable the bridge still works via cursor pulls.
import { io } from 'socket.io-client';

/** Event types that are noise for a teammate (presence plumbing, own joins). */
const QUIET_TYPES = new Set([
  'edge.participant.heartbeat',
  'edge.participant.joined',
  'edge.participant.left',
  'edge.ticket.redeemed',
]);

/**
 * Watch the incident live. Calls `onEvent(evt)` for every meaningful shared
 * event not authored by this agent instance. Returns a stop() function.
 */
export function watchIncident({ baseUrl, slug, incidentId, token }, { onEvent, ownInstanceId, log } = {}) {
  const socket = io(`${baseUrl.replace(/\/$/, '')}/realtime`, {
    transports: ['websocket'],
    auth: { token, slug },
  });
  socket.on('connect', () => {
    socket.emit('incident.subscribe', { incidentId });
    log?.('live: subscribed to war-room updates');
  });
  socket.on('incident.event', (evt) => {
    if (!evt || QUIET_TYPES.has(evt.type)) return;
    const instance = evt.payload && typeof evt.payload === 'object' ? evt.payload.agentInstanceId : undefined;
    const own = typeof ownInstanceId === 'function' ? ownInstanceId() : ownInstanceId;
    if (own && instance === own) return; // don't echo our own publications
    onEvent?.(evt);
  });
  socket.on('connect_error', (e) => {
    log?.(`live: realtime unavailable (${e?.message ?? 'connect error'}) — falling back to get_updates pulls`);
  });
  return () => socket.disconnect();
}

/** One human-readable stderr line for a pushed shared event. */
export function describeEvent(evt) {
  const p = (evt?.payload && typeof evt.payload === 'object' ? evt.payload : {});
  const who = [p.humanActorId, p.edgeAgentLabel].filter(Boolean).join(' · ');
  const what = p.text ?? p.description ?? p.doing ?? p.summary ?? '';
  const body = typeof what === 'string' && what ? `: "${truncate(what)}"` : '';
  return `⚡ ${evt?.type ?? 'event'}${who ? ` from ${who}` : ''}${body} — new shared context; call get_updates.`;
}

function truncate(s, n = 100) {
  s = String(s);
  return s.length > n ? `${s.slice(0, n - 1)}…` : s;
}
