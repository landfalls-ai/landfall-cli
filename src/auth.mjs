// auth.mjs — browser handoff sign-in for the `landfall` CLI.
//
// ── What changed, and why (feature 043, tasks T060/T061) ──────────────────
// This file used to run an OAuth 2.1 + PKCE flow directly against Landfall's
// Keycloak (feature 024). It no longer speaks OIDC to anyone.
//
// The reason is the whole point of feature 043: after it, an organization's
// people authenticate against THEIR OWN identity provider, and Keycloak is
// Landfall-personnel-only. A CLI hard-coded to Keycloak would work for exactly
// one population — Landfall staff — and would be a second, divergent place
// where "which authentication branch does this organization use?" gets decided.
// FR-056 and SC-010 say there must be exactly one such place, and it lives in
// the core API's own auth-branch resolver — this CLI never re-implements it.
//
// So the CLI does the least it possibly can: open the Landfall web app and
// wait for a session to be handed back. The browser authenticates through
// WHICHEVER branch that organization uses — password, the organization's own
// OIDC provider, or (personnel only) Keycloak — and the CLI never learns or
// cares which.
//
//     CLI                          browser                 Landfall web / API
//      │ open ────────────────────► /cli-auth?nonce=…
//      │                               ├─ authenticate via the org's branch
//      │                               └─ POST /auth/cli-handoff ─► mint +
//      │                                                            STORE a
//      │                                                            redeemable
//      │                                                            grant
//      │ poll ─── POST /auth/cli-handoff/poll {nonce} ─────────────► redeem
//      │ ◄──────────────────────────────────────────── { accessToken, … }
//      │ write ~/.config/landfall/credentials.json (0600)
//
// ── Why this is a POLL now, not a loopback listener (2026-08-11) ──────────
// This used to bind `http.createServer` on a loopback port and have the
// BROWSER push the finished session to it (`POST http://<host>:<port>/
// callback`). That mechanism needed three separate patches in one day and
// was still broken:
//   1. Chrome's Private Network Access policy silently failed the push
//      unless the preflight response carried an extra header (fixed).
//   2. Safari refused the push entirely regardless of that header — tried
//      fetching `127.0.0.1`, then `localhost` after a live report, neither
//      fixed it.
//   3. The actual cause, found by inspecting Safari's own Network panel
//      live: zero request entry at all — WebKit refuses to even ATTEMPT an
//      `https:` page's `fetch()` to any `http:` target, loopback or not. No
//      hostname choice fixes a categorical block.
//
// Rather than a fourth patch to the same mechanism, the monorepo's
// `POST /auth/cli-handoff` now ALSO stores a redeemable grant server-side
// (keyed by this same `nonce`), and the CLI retrieves it over an ordinary
// outbound HTTPS poll — exactly the same shape as `refreshAccessToken`/
// `logout` below already use. No browser involvement in this half of the
// flow at all, so CORS/mixed-content/Private-Network-Access simply do not
// apply — there is no cross-origin request for any of them to govern.
// `preflightHeaders`/the loopback `http.createServer` this file used to
// export are gone with it; see git history if you need the old shape.
//
// ── Refresh, seamlessly, in the background (feature 104) ───────────────────
// The 1-hour access token used to be a dead end: once it expired, every
// command failed until the user noticed and ran `landfall login` again
// through a full browser round trip. It no longer is. Every credential this
// file writes now carries a refresh token too (`POST /auth/cli-refresh`,
// unauthenticated by session on purpose — it has to work precisely when the
// access token has already expired), and `getCachedAccessToken()` uses it
// silently: an expired-or-near-expiry access token is exchanged for a fresh
// rotated pair before the caller ever sees a `null`. No visible re-auth
// prompt, no flag to opt in — this is what "signed in" now means. The only
// time a human sees a prompt again is when the refresh token itself is dead:
// expired past its own (30-day, sliding) window, or revoked — explicitly by
// `landfall logout` (now a real server call, not just a local file delete),
// or organization-wide by that org's own "revoke all sessions" action, which
// a pinned refresh silently re-checks on every use. `explainExpiredCredential`
// is what prints the fallback instruction at that point.
//
// Pure Node built-ins (http, crypto, fs, fetch) — no new dependency. Tokens
// are NEVER logged.
//
// Where the browser is sent comes from `src/instance.mjs` and NOWHERE else.
// This file used to own its own `LANDFALL_WEB_URL ?? 'http://localhost:5173'`
// default, which is the exact line that made `brew install landfall` followed
// by `landfall login` fail for every customer: it printed a developer's local
// dev-server address, opened a browser, and reported nothing while the browser
// showed a connection error.
import crypto from 'node:crypto';
import { promises as fs } from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import { spawn } from 'node:child_process';
import {
  resolveInstance,
  probe,
  describeFailure,
  REACHABILITY,
  EXIT_UNREACHABLE,
} from './instance.mjs';

/** How long to wait for the browser to complete the handoff. */
const HANDOFF_TIMEOUT_MS = 5 * 60_000;

/** How often to poll `POST /auth/cli-handoff/poll` while waiting. Short
 * enough that signing in feels immediate once the browser tab shows
 * "Command line connected"; long enough that a login storm from many
 * teammates behind one office NAT stays well under the endpoint's
 * per-IP rate limit (120/min at the time of writing). */
const POLL_INTERVAL_MS = 1_500;

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function cacheDir() {
  const base = process.env.XDG_CONFIG_HOME || path.join(os.homedir(), '.config');
  return path.join(base, 'landfall');
}
function cachePath() {
  return path.join(cacheDir(), 'credentials.json');
}

function b64url(buf) {
  return buf.toString('base64').replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

/** Open a URL in the user's default browser (best-effort, cross-platform). */
function openBrowser(url) {
  const cmd = process.platform === 'darwin' ? 'open' : process.platform === 'win32' ? 'cmd' : 'xdg-open';
  const args = process.platform === 'win32' ? ['/c', 'start', '', url] : [url];
  try {
    spawn(cmd, args, { stdio: 'ignore', detached: true }).unref();
  } catch {
    /* headless — the URL is printed for the user to open manually */
  }
}

async function saveSession({ accessToken, refreshToken, expiresAt, orgSlug, instance }) {
  await fs.mkdir(cacheDir(), { recursive: true });
  const body = JSON.stringify(
    {
      access_token: accessToken,
      // WHICH Landfall minted this. Without it, a stored token can be silently
      // presented to a deployment that never issued it once the resolved
      // instance changes — the failure then looks like "your session broke"
      // rather than "you are pointed somewhere else". A credential written
      // before this field existed reads as unknown, which is treated as a
      // mismatch and prompts a fresh sign-in rather than being assumed to match.
      instance: instance ?? null,
      // The access token is short-lived (1h) but IS refreshable (feature
      // 104) — `refresh_token` is the credential `refreshAccessToken` presents
      // to renew it silently, in the background, with no visible prompt.
      // Absent only for a credential written by a pre-104 CLI/server pair
      // (graceful degradation: treated as legacy, never assumed refreshable).
      refresh_token: refreshToken ?? null,
      expires_at: typeof expiresAt === 'string' ? Date.parse(expiresAt) : Date.now() + 3_540_000,
      org_slug: orgSlug ?? null,
      iss: 'landfall-core',
    },
    null,
    2,
  );
  await fs.writeFile(cachePath(), body, { mode: 0o600 });
  await fs.chmod(cachePath(), 0o600).catch(() => {});
}

async function readCache() {
  try {
    return JSON.parse(await fs.readFile(cachePath(), 'utf8'));
  } catch {
    return null;
  }
}

// How much of the access token's remaining lifetime is "close enough to
// expired" to refresh proactively, rather than waiting for a caller to see a
// 401 mid-request. 1 minute — generous next to the 1h access-token TTL, cheap
// against the 30-day refresh-token one.
const REFRESH_SKEW_MS = 60_000;

/**
 * A valid cached access token, refreshing it silently in the background first
 * if it is expired or within {@link REFRESH_SKEW_MS} of expiring — or `null`
 * if the user must `landfall login` (no cached session, or the refresh token
 * itself is dead: expired, reused, or revoked). Never triggers a BROWSER flow
 * on its own; the background refresh is a single unauthenticated HTTP call
 * (feature 104) — see {@link refreshAccessToken}.
 *
 * ── Graceful degradation of an existing Keycloak credential (T061, FR-055) ──
 * A credential cached by the PREVIOUS Keycloak flow is still honoured until it
 * expires — the API continues to accept Keycloak tokens (for internal
 * organizations), so nothing breaks mid-session. Once expired it cannot be
 * refreshed (it predates refresh tokens entirely — `refresh_token` is absent,
 * so {@link refreshAccessToken} declines rather than guessing), and
 * {@link explainExpiredCredential} produces an explicit instruction. Never a
 * silent failure: a CLI that quietly stops working during an incident is
 * worse than one that says what to do.
 */
export async function getCachedAccessToken({ fetchImpl } = {}) {
  const cache = await readCache();
  if (!cache?.access_token) return null;
  if (typeof cache.expires_at === 'number' && cache.expires_at - Date.now() > REFRESH_SKEW_MS) {
    return cache.access_token;
  }
  return await refreshAccessToken({ fetchImpl });
}

/**
 * Silently exchange the cached refresh token for a fresh, rotated pair
 * (`POST /auth/cli-refresh`, contracts/cli-refresh.md) and persist the
 * result. Returns the new access token, or `null` — with nothing written —
 * if there is nothing to refresh with, the call fails, or the server refuses
 * it (FR-011: every refusal reason collapses to the same shape here — an
 * unknown, expired, reused, or org-revoked refresh token all look identical
 * from this side, on purpose; the specific cause lives only in the server's
 * own audit record).
 *
 * Targets `cache.instance.api` — the SAME Landfall that minted the chain —
 * never whichever instance happens to be currently resolved. A credential
 * predating instance tracking (`instance` absent) has nothing to target and
 * is declined the same as one with no refresh token at all.
 */
export async function refreshAccessToken({ fetchImpl = globalThis.fetch } = {}) {
  const cache = await readCache();
  if (!cache?.refresh_token || !cache?.instance?.api) return null;

  let res;
  try {
    res = await fetchImpl(`${cache.instance.api}/auth/cli-refresh`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ refreshToken: cache.refresh_token }),
    });
  } catch {
    // Offline, DNS failure, whatever — indistinguishable from "could not
    // refresh" here. The caller falls back to the expired-credential path.
    return null;
  }
  if (!res.ok) return null; // 401: uniform refusal — see doc comment above.

  let body;
  try {
    body = await res.json();
  } catch {
    return null;
  }
  if (typeof body.accessToken !== 'string' || typeof body.refreshToken !== 'string') return null;

  await saveSession({
    accessToken: body.accessToken,
    refreshToken: body.refreshToken,
    expiresAt: body.expiresAt,
    orgSlug: cache.org_slug ?? null,
    instance: cache.instance,
  });
  return body.accessToken;
}

/** True if an unexpired session is cached. */
export async function isAuthenticated() {
  return (await getCachedAccessToken()) != null;
}

/**
 * The organization slug the cached session belongs to, or null.
 *
 * A Landfall session is pinned to exactly one organization (feature 043), so
 * this is the session's own answer to "which org am I in?" rather than a
 * preference — which is why the declared-intent hook (#233) uses it instead of
 * asking the engineer to configure a slug a second time. Returned even when the
 * token has expired: callers that need a live token ask for one separately, and
 * the slug is not a credential.
 */
export async function getCachedOrgSlug() {
  const cache = await readCache();
  return typeof cache?.org_slug === 'string' && cache.org_slug ? cache.org_slug : null;
}

/**
 * The message to print when a cached credential has expired — including the
 * case where it is a legacy Keycloak one this CLI can no longer refresh.
 * Returns `null` when there is nothing to explain.
 */
export async function explainExpiredCredential() {
  const cache = await readCache();
  if (!cache?.access_token) return null;
  if (typeof cache.expires_at === 'number' && cache.expires_at > Date.now()) return null;

  const legacy = typeof cache.iss === 'string' && cache.iss.includes('/realms/');
  return legacy
    ? 'Your cached Landfall credential was issued by the old Keycloak sign-in and has expired. ' +
        'It cannot be refreshed. Run `landfall login` to sign in through your organization’s own ' +
        'identity provider.'
    : 'Your Landfall session has expired. Run `landfall login` to sign in again.';
}

/**
 * Sign out. Revokes the refresh chain server-side FIRST (`POST
 * /auth/cli-logout`, feature 104, FR-008) — without this, a stray copy of the
 * old credential file left on a backup or another machine could keep
 * refreshing silently after an explicit sign-out, which would make "sign
 * out" a lie. Best-effort and silent on failure (RFC 7009's own convention,
 * matching what the endpoint itself does for an unknown token): local
 * sign-out must still succeed even offline, so a failed or unreachable
 * revoke call never blocks clearing the local file. A pre-104 credential (no
 * `refresh_token`/`instance` recorded) has nothing to revoke and skips
 * straight to the local delete, exactly as before this feature existed.
 */
export async function logout({ fetchImpl = globalThis.fetch } = {}) {
  const cache = await readCache();
  if (cache?.refresh_token && cache?.instance?.api) {
    try {
      await fetchImpl(`${cache.instance.api}/auth/cli-logout`, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ refreshToken: cache.refresh_token }),
      });
    } catch {
      /* offline or unreachable — local sign-out still proceeds below */
    }
  }
  await fs.rm(cachePath(), { force: true });
}

/**
 * FR-014: is the cached session for the instance we are now pointed at?
 *
 * Returns an explanatory message when it is NOT, and null when it is fine.
 *
 * A credential written before this feature carries no instance at all. That
 * reads as unknown, and unknown is treated as a MISMATCH rather than assumed
 * to match: assuming would silently present an existing token to a host that
 * never issued it, which is the precise thing recording the instance exists
 * to prevent.
 */
export async function explainInstanceMismatch(resolved) {
  const cache = await readCache();
  if (!cache?.access_token) return null; // nothing cached; not a mismatch

  const cachedApi = cache.instance?.api ?? null;
  if (cachedApi === resolved.api) return null;

  return cachedApi
    ? `Your saved session is for ${cachedApi}, but you are now pointed at ${resolved.api}.\n` +
        '  A session from one Landfall cannot be used against another.\n' +
        '  Run `landfall login` to sign in to this one.'
    : 'Your saved session predates instance tracking, so it cannot be confirmed to belong to ' +
        `${resolved.api}.\n  Run \`landfall login\` to sign in again.`;
}

/**
 * Poll `POST /auth/cli-handoff/poll` for the session the browser's
 * `POST /auth/cli-handoff` call already stashed under this `nonce`.
 *
 * ── Why the nonce alone is enough here ──────────────────────────────────
 * This is the SAME nonce `login()` put in the URL it sent the browser to —
 * a 256-bit value nobody else ever sees, so presenting it back is proof of
 * having been the one who opened that link. The server enforces the rest:
 * an exact-hash lookup only (no listing/enumeration is possible), and
 * single-use via an atomic conditional update, so a retried poll — a
 * normal event; this loop has no way to know its previous request landed —
 * can never redeem the same grant twice.
 *
 * A 404 means "not yet" and is indistinguishable, on purpose, from "never
 * existed" — this loop treats both identically and just keeps polling
 * until `HANDOFF_TIMEOUT_MS`. A network blip gets the same treatment: the
 * browser side of this flow can take minutes (typing a password, clicking
 * through an SSO redirect), so one failed request is never a reason to
 * give up early.
 */
export async function pollForHandoff(
  apiUrl,
  nonce,
  orgSlug,
  { fetchImpl = globalThis.fetch, intervalMs = POLL_INTERVAL_MS, timeoutMs = HANDOFF_TIMEOUT_MS } = {},
) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    await sleep(intervalMs);

    let res;
    try {
      res = await fetchImpl(`${apiUrl}/auth/cli-handoff/poll`, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ nonce }),
      });
    } catch {
      continue; // offline / DNS blip — keep polling until the deadline
    }
    if (!res.ok) continue; // 404 "not yet" (or any other refusal) — keep polling

    let body;
    try {
      body = await res.json();
    } catch {
      continue;
    }
    if (typeof body.accessToken !== 'string' || body.accessToken.length === 0) continue;

    return {
      accessToken: body.accessToken,
      // Feature 104: present on every server new enough to mint one.
      // Tolerated as absent against an older API (graceful degradation —
      // the credential is then simply not refreshable, same as a legacy
      // Keycloak one) rather than rejecting the handoff over it.
      refreshToken: typeof body.refreshToken === 'string' ? body.refreshToken : undefined,
      expiresAt: body.expiresAt,
      orgSlug: body.orgSlug ?? orgSlug ?? null,
    };
  }
  throw new Error('timed out waiting for the browser to complete sign-in');
}

/**
 * Sign in via the browser handoff and cache the result. Returns the access
 * token. `log` is a stderr logger (it never prints token text).
 */
export async function login(log = () => {}, { orgSlug, url = null, instance = null } = {}) {
  const nonce = b64url(crypto.randomBytes(32));

  // Resolved ONCE, and used for every check below. Resolving per-use is how
  // a login could otherwise probe one origin while polling another.
  const resolved = instance ?? (await resolveInstance({ url }));
  const webUrl = resolved.web;

  // Preflight BEFORE the browser opens. The reported defect was not only the
  // wrong address, it was being handed a browser error page as the only
  // diagnosis. A timeout deliberately does not block: a strict check would
  // turn a slow link or a proxy into "the product is broken".
  const reach = await probe(webUrl);
  if (reach === REACHABILITY.TIMEOUT) {
    log(`warning: ${webUrl} did not answer quickly. Continuing anyway.`);
  } else if (reach !== REACHABILITY.OK) {
    const error = new Error(describeFailure(reach, resolved));
    error.exitCode = EXIT_UNREACHABLE;
    error.unreachable = true;
    throw error;
  }

  const handoffUrl =
    `${webUrl}/cli-auth?` +
    new URLSearchParams({
      nonce,
      ...(orgSlug ? { org: orgSlug } : {}),
    }).toString();
  log(
    `opening your browser to sign in… if it does not open, visit:\n${handoffUrl}\n` +
      'You will sign in the same way you sign in to Landfall on the web — with your ' +
      'password, or through your organization’s identity provider.',
  );
  openBrowser(handoffUrl);

  const session = await pollForHandoff(resolved.api, nonce, orgSlug);

  await saveSession({ ...session, instance: resolved });
  return session.accessToken;
}
