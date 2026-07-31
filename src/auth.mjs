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
// FR-056 and SC-010 say there must be exactly one such place, and it is
// `resolveAuthBranch()` in `libs/auth`.
//
// So the CLI now does the least it possibly can: bind a loopback listener, open
// the Landfall web app, and wait for a session to be handed back. The browser
// authenticates through WHICHEVER branch that organization uses — password, the
// organization's own OIDC provider, or (personnel only) Keycloak — and the CLI
// never learns or cares which.
//
//     CLI                          browser                 Landfall web / API
//      │ bind 127.0.0.1:<port>
//      │ open ────────────────────► /cli-auth?port=…&nonce=…
//      │                               ├─ authenticate via the org's branch
//      │                               └─ POST /auth/cli-handoff ─► mint a
//      │                                                            session
//      │ ◄── POST http://127.0.0.1:<port>/callback { accessToken, … }
//      │ write ~/.config/landfall/credentials.json (0600)
//
// Pure Node built-ins (http, crypto, fs) — no new dependency. Tokens are NEVER
// logged. Config via env: LANDFALL_WEB_URL (sensible localhost default).
import http from 'node:http';
import crypto from 'node:crypto';
import { promises as fs } from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import { spawn } from 'node:child_process';

/** The Landfall web app the browser is sent to. */
const WEB_URL = () => (process.env.LANDFALL_WEB_URL ?? 'http://localhost:5173').replace(/\/$/, '');

/** How long to wait for the browser to complete the handoff. */
const HANDOFF_TIMEOUT_MS = 5 * 60_000;

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

async function saveSession({ accessToken, expiresAt, orgSlug }) {
  await fs.mkdir(cacheDir(), { recursive: true });
  const body = JSON.stringify(
    {
      access_token: accessToken,
      // A Landfall session is short-lived (1h) and is deliberately NOT
      // refreshable — there is no refresh token in this model. On expiry the
      // CLI asks for an explicit `landfall login` rather than silently doing
      // anything on the user's behalf.
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

/**
 * A valid cached access token, or `null` if the user must `landfall login`.
 * Never triggers a browser flow on its own.
 *
 * ── Graceful degradation of an existing Keycloak credential (T061, FR-055) ──
 * A credential cached by the PREVIOUS Keycloak flow is still honoured until it
 * expires — the API continues to accept Keycloak tokens (for internal
 * organizations), so nothing breaks mid-session. Once expired it cannot be
 * refreshed by this file any more, and {@link explainExpiredCredential}
 * produces an explicit instruction. Never a silent failure: a CLI that quietly
 * stops working during an incident is worse than one that says what to do.
 */
export async function getCachedAccessToken() {
  const cache = await readCache();
  if (!cache?.access_token) return null;
  if (typeof cache.expires_at === 'number' && cache.expires_at > Date.now()) {
    return cache.access_token;
  }
  return null;
}

/** True if an unexpired session is cached. */
export async function isAuthenticated() {
  return (await getCachedAccessToken()) != null;
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

export async function logout() {
  await fs.rm(cachePath(), { force: true });
}

/**
 * Sign in via the browser handoff and cache the result. Returns the access
 * token. `log` is a stderr logger (it never prints token text).
 *
 * ── Why the loopback listener does not simply trust whatever arrives ───────
 * `127.0.0.1:<port>` is reachable by EVERY process on the machine, so an
 * unrelated local program could POST a token of its own choosing and make the
 * CLI act as an attacker's account. Two checks prevent that, and both matter:
 *
 *   • a one-time `nonce` the CLI generated and passed in the opening URL must
 *     come back — so only something that saw that URL can complete the flow;
 *   • the `Origin` must be the Landfall web app — so an unrelated page in the
 *     user's browser cannot post to the listener.
 *
 * The nonce is compared in constant time. A timing side channel on a value an
 * attacker can retry is worth closing even when exploiting it is a stretch.
 */
export async function login(log = () => {}, { orgSlug } = {}) {
  const nonce = b64url(crypto.randomBytes(32));

  const session = await new Promise((resolve, reject) => {
    const server = http.createServer((req, res) => {
      // The browser POSTs cross-origin from the web app, so it preflights.
      if (req.method === 'OPTIONS') {
        res.writeHead(204, {
          'access-control-allow-origin': WEB_URL(),
          'access-control-allow-methods': 'POST, OPTIONS',
          'access-control-allow-headers': 'content-type',
        });
        res.end();
        return;
      }

      const url = new URL(req.url, 'http://127.0.0.1');
      if (url.pathname !== '/callback' || req.method !== 'POST') {
        res.writeHead(404).end();
        return;
      }
      if ((req.headers.origin ?? '') !== WEB_URL()) {
        res.writeHead(403).end();
        return;
      }

      let body = '';
      req.on('data', (chunk) => {
        body += chunk;
        // A local process must not be able to exhaust memory here.
        if (body.length > 16_384) req.destroy();
      });
      req.on('end', () => {
        let payload;
        try {
          payload = JSON.parse(body);
        } catch {
          res.writeHead(400).end();
          return;
        }
        if (!constantTimeEquals(String(payload.nonce ?? ''), nonce)) {
          res.writeHead(403).end();
          return;
        }
        if (typeof payload.accessToken !== 'string' || payload.accessToken.length === 0) {
          res.writeHead(400).end();
          return;
        }

        res.writeHead(200, {
          'content-type': 'application/json',
          'access-control-allow-origin': WEB_URL(),
        });
        res.end(JSON.stringify({ ok: true }));
        clearTimeout(timer);
        server.close();
        resolve({
          accessToken: payload.accessToken,
          expiresAt: payload.expiresAt,
          orgSlug: payload.orgSlug ?? orgSlug ?? null,
        });
      });
    });

    const timer = setTimeout(() => {
      server.close();
      reject(new Error('timed out waiting for the browser to complete sign-in'));
    }, HANDOFF_TIMEOUT_MS);

    server.on('error', (error) => {
      clearTimeout(timer);
      reject(error);
    });

    server.listen(0, '127.0.0.1', () => {
      const port = server.address().port;
      const handoffUrl =
        `${WEB_URL()}/cli-auth?` +
        new URLSearchParams({
          port: String(port),
          nonce,
          ...(orgSlug ? { org: orgSlug } : {}),
        }).toString();
      log(
        `opening your browser to sign in… if it does not open, visit:\n${handoffUrl}\n` +
          'You will sign in the same way you sign in to Landfall on the web — with your ' +
          'password, or through your organization’s identity provider.',
      );
      openBrowser(handoffUrl);
    });
  });

  await saveSession(session);
  return session.accessToken;
}

/** Timing-safe string comparison that tolerates unequal lengths. */
function constantTimeEquals(a, b) {
  const ab = Buffer.from(a);
  const bb = Buffer.from(b);
  if (ab.length !== bb.length) {
    // Still burn a comparison so the length is not itself a fast path.
    crypto.timingSafeEqual(ab, ab);
    return false;
  }
  return crypto.timingSafeEqual(ab, bb);
}
