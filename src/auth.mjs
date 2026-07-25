// auth.mjs — native OAuth 2.1 + PKCE for the `landfall` CLI (feature 024, US1).
//
// Runs the Authorization-Code + PKCE flow against Landfall's existing Keycloak:
// generate a verifier/challenge, open the browser to the realm's authorize
// endpoint, receive the code on a loopback listener, exchange it for tokens, and
// cache them locally (0600). A cached, unexpired (or refreshable) token means the
// user is "already authenticated" and the CLI joins a war room with no share link.
//
// Pure Node built-ins (http, crypto, fs) — no new dependency. Tokens are NEVER
// logged. Config via env: LANDFALL_KEYCLOAK_URL / LANDFALL_KEYCLOAK_REALM /
// LANDFALL_CLI_CLIENT_ID (sensible localhost defaults).
import http from 'node:http';
import crypto from 'node:crypto';
import { promises as fs } from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import { spawn } from 'node:child_process';

const KC_URL = () => (process.env.LANDFALL_KEYCLOAK_URL ?? 'http://localhost:8080').replace(/\/$/, '');
const KC_REALM = () => process.env.LANDFALL_KEYCLOAK_REALM ?? 'landfall';
const CLIENT_ID = () => process.env.LANDFALL_CLI_CLIENT_ID ?? 'landfall-cli';
const OIDC = () => `${KC_URL()}/realms/${KC_REALM()}/protocol/openid-connect`;

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

async function saveTokens(tok) {
  await fs.mkdir(cacheDir(), { recursive: true });
  const body = JSON.stringify(
    {
      access_token: tok.access_token,
      refresh_token: tok.refresh_token,
      expires_at: Date.now() + (Number(tok.expires_in ?? 300) - 30) * 1000,
      iss: `${KC_URL()}/realms/${KC_REALM()}`,
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

async function exchange(params) {
  const res = await fetch(`${OIDC()}/token`, {
    method: 'POST',
    headers: { 'content-type': 'application/x-www-form-urlencoded' },
    body: new URLSearchParams(params).toString(),
  });
  if (!res.ok) throw new Error(`token endpoint ${res.status}: ${await res.text().catch(() => '')}`);
  return res.json();
}

/** Refresh the cached access token; returns the new access token or null. */
async function tryRefresh(cache) {
  if (!cache?.refresh_token) return null;
  try {
    const tok = await exchange({
      grant_type: 'refresh_token',
      client_id: CLIENT_ID(),
      refresh_token: cache.refresh_token,
    });
    await saveTokens(tok);
    return tok.access_token;
  } catch {
    return null;
  }
}

/**
 * Return a valid access token from cache (refreshing if needed), or null if the
 * user must `landfall login`. Never triggers a browser flow on its own.
 */
export async function getCachedAccessToken() {
  const cache = await readCache();
  if (!cache?.access_token) return null;
  if (typeof cache.expires_at === 'number' && cache.expires_at > Date.now()) return cache.access_token;
  return tryRefresh(cache);
}

/** True if a (possibly refreshable) session is cached. */
export async function isAuthenticated() {
  return (await getCachedAccessToken()) != null;
}

export async function logout() {
  await fs.rm(cachePath(), { force: true });
}

/**
 * Run the browser Authorization-Code + PKCE flow and cache the result.
 * Returns the access token. `log` is a stderr logger (never prints token text).
 */
export async function login(log = () => {}) {
  const verifier = b64url(crypto.randomBytes(32));
  const challenge = b64url(crypto.createHash('sha256').update(verifier).digest());
  const state = b64url(crypto.randomBytes(16));

  const { code, redirectUri } = await new Promise((resolve, reject) => {
    const server = http.createServer((req, res) => {
      const u = new URL(req.url, `http://127.0.0.1`);
      if (u.pathname !== '/callback') {
        res.writeHead(404).end();
        return;
      }
      const returnedState = u.searchParams.get('state');
      const code = u.searchParams.get('code');
      res.writeHead(200, { 'content-type': 'text/html' });
      res.end('<html><body style="font-family:sans-serif">Landfall sign-in complete — you can close this tab.</body></html>');
      server.close();
      if (!code || returnedState !== state) return reject(new Error('authorization failed or state mismatch'));
      resolve({ code, redirectUri: `http://127.0.0.1:${server.address().port}/callback` });
    });
    server.on('error', reject);
    server.listen(0, '127.0.0.1', () => {
      const port = server.address().port;
      const redirectUri = `http://127.0.0.1:${port}/callback`;
      const authUrl =
        `${OIDC()}/auth?` +
        new URLSearchParams({
          response_type: 'code',
          client_id: CLIENT_ID(),
          redirect_uri: redirectUri,
          scope: 'openid email profile',
          state,
          code_challenge: challenge,
          code_challenge_method: 'S256',
        }).toString();
      log(`opening your browser to sign in… if it does not open, visit:\n${authUrl}`);
      openBrowser(authUrl);
    });
  });

  const tok = await exchange({
    grant_type: 'authorization_code',
    client_id: CLIENT_ID(),
    code,
    redirect_uri: redirectUri,
    code_verifier: verifier,
  });
  await saveTokens(tok);
  return tok.access_token;
}
