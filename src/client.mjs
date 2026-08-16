// client.mjs — the Edge Bridge's outbound connection to Landfall. Wraps the
// feature-006 `edge/*` HTTP contract with the teammate's session token. `fetch`
// is injectable so the client is node-testable without a network. Read-only by
// default; contributions/actions are propose-only + approval-gated server-side.

export class EdgeBridgeClient {
  /**
   * @param {{baseUrl:string, slug:string, incidentId:string, token:string, agentLabel?:string}} cfg
   * @param {typeof fetch} [fetchImpl]
   */
  constructor(cfg, fetchImpl) {
    this.cfg = cfg;
    this.fetch = fetchImpl ?? globalThis.fetch;
    this.agentInstanceId = null;
  }

  get #base() {
    return `${this.cfg.baseUrl.replace(/\/$/, '')}/o/${this.cfg.slug}/incidents/${this.cfg.incidentId}`;
  }
  get #headers() {
    return { authorization: `Bearer ${this.cfg.token ?? ''}`, 'content-type': 'application/json' };
  }
  async #post(path, body) {
    const r = await this.fetch(`${this.#base}${path}`, { method: 'POST', headers: this.#headers, body: JSON.stringify(body ?? {}) });
    if (!r.ok) {
      // Surface a server-provided `reason`/`message` (e.g. the artifact policy
      // rejection) so the caller can relay a clear cause, not just a status.
      let reason = '';
      try { const b = await r.json(); reason = b?.reason ?? b?.message ?? ''; } catch { /* no body */ }
      throw new Error(`${path} → HTTP ${r.status}${reason ? `: ${reason}` : ''}`);
    }
    return r.status === 202 ? {} : r.json();
  }
  async #get(path) {
    const r = await this.fetch(`${this.#base}${path}`, { headers: { authorization: `Bearer ${this.cfg.token ?? ''}` } });
    if (!r.ok) throw new Error(`${path} → HTTP ${r.status}`);
    return r.json();
  }

  /** Join the incident with this member's edge agent; the server issues an instance id. */
  async join() {
    const res = await this.#post('/edge/join', { edgeAgentLabel: this.cfg.agentLabel ?? 'edge-agent' });
    this.agentInstanceId = res.agentInstanceId ?? null;
    return res;
  }
  /** Liveness + current activity → drives presence + the "who's doing what" summary. */
  heartbeat(doing) {
    return this.#post('/edge/heartbeat', { agentInstanceId: this.agentInstanceId, doing });
  }
  /** Consolidate a contribution (finding | query | hypothesis | action) onto the shared timeline. */
  contribute(kind, body) {
    return this.#post('/edge/contributions', { agentInstanceId: this.agentInstanceId, kind, ...(body ?? {}) });
  }
  /**
   * Share a locally-created artifact into the incident (feature 025). Bytes are
   * base64. The server enforces the size/type policy, stores the bytes, and
   * appends the durable `artifact.shared` event — this is NOT an execution
   * channel (FR-007). Reuses the incident-scoped bearer via `#post`.
   */
  uploadArtifact(filename, contentType, dataBase64) {
    return this.#post('/artifacts', { filename, contentType, dataBase64, edgeAgentLabel: this.cfg.agentLabel, agentInstanceId: this.agentInstanceId });
  }
  /**
   * FLAG a published context item (chat message or finding) as wrong or
   * misleading (feature 029). This is a POSITION, not a decision: the server
   * tallies distinct actors and requires a human among the concurring voters
   * before anything is quarantined, so an agent flagging alone changes nothing
   * but the visible tally.
   *
   * `agentInstanceId` is the id the SERVER issued at `join()` — it says which
   * agent instance is speaking, and the server still resolves the human and the
   * display name itself. Nothing here asserts an identity of its own.
   */
  flagContext(targetSeq, reason, targetKind) {
    return this.#post('/vetting/flag', {
      agentInstanceId: this.agentInstanceId,
      edgeAgentLabel: this.cfg.agentLabel,
      targetSeq,
      ...(targetKind ? { targetKind } : {}),
      reason,
    });
  }

  /**
   * Take a position on a STAGED claim (feature 034): `corroborate` or `contest`.
   * One active position per participant — repeating it replaces, never adds.
   * The author's own corroboration is excluded server-side, and admission needs
   * a human in the chain, so this can raise or lower the tally but never decide.
   */
  positionClaim(claimSeq, position, reason) {
    return this.#post(`/claims/${encodeURIComponent(claimSeq)}/position`, {
      agentInstanceId: this.agentInstanceId,
      edgeAgentLabel: this.cfg.agentLabel,
      position,
      ...(reason ? { reason } : {}),
    });
  }

  /**
   * STAGE a claim (feature 034). It enters the staging area — NOT the room feed
   * and NOT any other participant's agent context — until it earns admission.
   * Staging is how a local finding becomes something the room can vote on.
   */
  stageClaim(body) {
    return this.#post('/claims', {
      agentInstanceId: this.agentInstanceId,
      edgeAgentLabel: this.cfg.agentLabel,
      ...(body ?? {}),
    });
  }

  leave() {
    return this.#post('/edge/leave', { agentInstanceId: this.agentInstanceId });
  }
  /** Incident context for the local agent: the current timeline (read-only). */
  getBrief() {
    return this.#get('/events');
  }
  /**
   * The war-room context frame (feature 116): title/severity/status, an
   * established-vs-open brief, and current participants — everything
   * `join_war_room`/`get_brief` need to render a real answer in one call,
   * instead of the bare event-type histogram `getBrief()` alone can offer.
   */
  getContextFrame() {
    return this.#get('/edge/context/frame');
  }
  /**
   * What changed since `sinceVersion` (feature 116), already classified by the
   * server into addressed-to-you / substantive-from-others / routine-counted-
   * only — this replaces `getUpdates()`'s raw, unranked event list as the
   * primary "what's new" read for `get_updates` and the in-band piggyback.
   * `sinceVersion` is the same durable cursor `getUpdates()`'s `sinceSeq` was —
   * the server's version counter is the incident's own seq (see
   * contracts/edge-context-api.md), so no new cursor concept is needed here.
   *
   * `agentInstanceId` (server-verified against this incident's join grant, the
   * SAME discipline `getAttention()`/`getDivergence()` already use) is what
   * lets the server tell this agent's OWN activity apart from everyone else's
   * — omit it and every event looks like it came from a stranger.
   */
  getContextDelta(sinceVersion) {
    const since = Number.isFinite(sinceVersion) ? sinceVersion : 0;
    const agent = this.agentInstanceId ? `&agentInstanceId=${encodeURIComponent(this.agentInstanceId)}` : '';
    return this.#get(`/edge/context/delta?sinceVersion=${encodeURIComponent(since)}${agent}`);
  }
  /**
   * Real search hits (feature 116) — seq/type/author/snippet, not a bare count.
   * Structured keyword matching over the room's visible content; no embeddings.
   */
  searchContext(query) {
    const agent = this.agentInstanceId ? `&agentInstanceId=${encodeURIComponent(this.agentInstanceId)}` : '';
    return this.#get(`/edge/context/search?q=${encodeURIComponent(query)}${agent}`);
  }
  /**
   * What the room is waiting on from THIS agent (#252): staged claims awaiting
   * its position, and context it authored or cited that has since been flagged
   * or quarantined.
   *
   * `agentInstanceId` is the id the SERVER issued at `join()`, and it is not an
   * identity claim: the server checks it against this incident's join grant and
   * refuses one it never issued. It matters because `quorum.ts` counts an agent
   * and its owning human as DISTINCT voters — omit it and the answer is the
   * human's queue, which both hides a vote this agent still owes and offers it
   * one it cannot cast.
   */
  getAttention() {
    const q = this.agentInstanceId ? `?agentInstanceId=${encodeURIComponent(this.agentInstanceId)}` : '';
    return this.#get(`/vetting/attention${q}`);
  }
  /** Durable-cursor delta read (021): only events with seq > sinceSeq. */
  getUpdates(sinceSeq) {
    const since = Number.isFinite(sinceSeq) ? sinceSeq : -1;
    return this.#get(`/events?sinceSeq=${encodeURIComponent(since)}`);
  }

  /**
   * Feature 20260812-010632 (US4/T049): is THIS agent's recent read history
   * drifting from the room's established causal claim? Same `agentInstanceId`
   * discipline as `getAttention()` above — server-verified against this
   * incident's own join grant (#249/#252), never trusted as a bare claim.
   * The server recomputes this fresh from the durable timeline on every
   * call — no server-side session state to poll differently — so this is a
   * plain, stateless GET, same shape as `getAttention()`.
   */
  getDivergence() {
    const q = this.agentInstanceId ? `?agentInstanceId=${encodeURIComponent(this.agentInstanceId)}` : '';
    return this.#get(`/vetting/divergence${q}`);
  }
}
