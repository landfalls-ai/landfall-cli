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
  leave() {
    return this.#post('/edge/leave', { agentInstanceId: this.agentInstanceId });
  }
  /** Incident context for the local agent: the current timeline (read-only). */
  getBrief() {
    return this.#get('/events');
  }
  /** Durable-cursor delta read (021): only events with seq > sinceSeq. */
  getUpdates(sinceSeq) {
    const since = Number.isFinite(sinceSeq) ? sinceSeq : -1;
    return this.#get(`/events?sinceSeq=${encodeURIComponent(since)}`);
  }
}
