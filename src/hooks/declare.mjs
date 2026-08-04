// declare.mjs — POST a confirmed classified intent to Landfall (#233 → #231).
//
// The endpoint is `POST /o/:slug/edge/declared-intent`, whose body schema is
// `.strict()`: an unknown key is a 400, not a silent strip. That is deliberate
// on the server's side and it is why this file sends the intent object exactly
// as `intentFor()` built it, with nothing added — no command line, no hostname,
// no local paths, not even a client version. If a field is ever wanted there,
// it has to be added to the contract first, which is the intended friction.
//
// The response is `{decision: 'attach'|'open'|'none', incidentId?, workspaceUrl?,
// reason?}` — what to DO about the intent is the server's call (#235), not this
// hook's. The hook reports and relays; it never decides that a war room should
// exist.
import { getCachedAccessToken, getCachedOrgSlug } from '../auth.mjs';

const DEFAULT_BASE_URL = 'http://localhost:3001';

/** How long to wait on the API before giving up. A hook must not hang a shell. */
export const DECLARE_TIMEOUT_MS = 5_000;

/**
 * Resolve where to send an intent and as whom.
 *
 * Returns `{ok: true, baseUrl, slug, token}` or `{ok: false, reason}` with a
 * reason meant for a human. Resolved BEFORE the engineer is prompted: asking
 * someone to approve a report that cannot be sent wastes the one interruption
 * this feature is allowed.
 */
export async function resolveTarget(deps = {}) {
  const {
    env = process.env,
    readToken = getCachedAccessToken,
    readOrgSlug = getCachedOrgSlug,
  } = deps;

  const token = await readToken();
  if (!token) {
    return { ok: false, reason: 'not signed in — run `landfall login` (nothing was reported)' };
  }
  const slug = env.LANDFALL_SLUG || (await readOrgSlug());
  if (!slug) {
    return { ok: false, reason: 'no organization on the cached session — set LANDFALL_SLUG (nothing was reported)' };
  }
  return {
    ok: true,
    baseUrl: (env.LANDFALL_BASE_URL || DEFAULT_BASE_URL).replace(/\/$/, ''),
    slug,
    token,
  };
}

/**
 * Send one declared intent. Resolves with the server's outcome, or
 * `{decision: 'none', reason}` when the report could not be delivered.
 *
 * A failure here is reported to the engineer on stderr and never raised: the
 * command they are actually trying to run is none of this hook's business, and
 * an unreachable API — or an organization that has not enabled the feature, in
 * which case #231 answers 404 by design — must not turn into an error in the
 * middle of their turn.
 */
export async function sendIntent(target, intent, deps = {}) {
  const { fetchImpl = globalThis.fetch, timeoutMs = DECLARE_TIMEOUT_MS } = deps;
  const url = `${target.baseUrl}/o/${encodeURIComponent(target.slug)}/edge/declared-intent`;
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  try {
    const res = await fetchImpl(url, {
      method: 'POST',
      headers: { authorization: `Bearer ${target.token}`, 'content-type': 'application/json' },
      body: JSON.stringify(intent),
      signal: controller.signal,
    });
    if (!res.ok) {
      // 404 is the shipped state for an organization that has not enabled
      // auto-attach — #231 answers it with the same body as an unknown org, on
      // purpose, so there is nothing to distinguish and nothing to report but
      // the status.
      return { decision: 'none', reason: `declared intent not accepted (HTTP ${res.status})` };
    }
    return await res.json();
  } catch (err) {
    const why = err?.name === 'AbortError' ? 'timed out' : err?.message || 'failed';
    return { decision: 'none', reason: `could not reach Landfall (${why})` };
  } finally {
    clearTimeout(timer);
  }
}

/** One line of feedback for the engineer, per decision. Never echoes the intent. */
export function describeOutcome(outcome) {
  if (outcome?.decision === 'attach') {
    return `joined the matching war room${outcome.workspaceUrl ? ` — ${outcome.workspaceUrl}` : ''}`;
  }
  if (outcome?.decision === 'open') {
    return `opened a provisional war room${outcome.workspaceUrl ? ` — ${outcome.workspaceUrl}` : ''}`;
  }
  return outcome?.reason ? `no war room opened (${outcome.reason})` : 'no war room opened';
}
