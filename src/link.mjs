// link.mjs — the magic link (feature 021). A war-room member shares
//   https://<landfall>/o/<slug>/incidents/<id>/agent?ticket=<single-use-jwt>
// and ANY teammate's MCP-capable agent turns it into a live investigation seat:
// parse → redeem the ticket (POST edge/redeem, the ticket IS the credential) →
// receive a short-lived edge session token scoped to exactly that incident.
// Pure + fetch-injectable, so it is node-testable without a network.

/** Parse an agent share link into its parts. Throws on anything else. */
export function parseShareLink(url) {
  let u;
  try { u = new URL(url); } catch { throw new Error(`not a Landfall agent share link: ${url}`); }
  const m = u.pathname.match(/\/o\/([^/]+)\/incidents\/([^/]+)\/agent\/?$/);
  if (!m) throw new Error('not a Landfall agent share link (expected /o/<slug>/incidents/<id>/agent)');
  const ticket = u.searchParams.get('ticket');
  if (!ticket) throw new Error('share link is missing its join ticket — ask for a fresh one');
  return { baseUrl: u.origin, slug: m[1], incidentId: m[2], ticket };
}

/**
 * Redeem a share link for an incident-scoped edge session. Returns the full
 * bridge config `{baseUrl, slug, incidentId, token, humanActorId}`. `baseUrl`
 * overrides the link origin (dev/tests where web and API origins differ).
 */
export async function redeemShareLink(url, { baseUrl, fetchImpl } = {}) {
  const parsed = parseShareLink(url);
  const base = (baseUrl ?? parsed.baseUrl).replace(/\/$/, '');
  const f = fetchImpl ?? globalThis.fetch;
  const r = await f(`${base}/o/${parsed.slug}/incidents/${parsed.incidentId}/edge/redeem`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ ticket: parsed.ticket }),
  });
  if (!r.ok) {
    const hint = r.status === 410 ? ' — the link expired or was already used; ask for a fresh one' : '';
    throw new Error(`could not join the war room (HTTP ${r.status})${hint}`);
  }
  const res = await r.json();
  return {
    baseUrl: base,
    slug: parsed.slug,
    incidentId: parsed.incidentId,
    token: res.token,
    humanActorId: res.humanActorId,
  };
}
