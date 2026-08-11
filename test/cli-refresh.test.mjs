// cli-refresh.test.mjs — feature 104's CLI-side half: a cached credential now
// carries a refresh token, and `getCachedAccessToken`/`logout` use it silently
// against `POST /auth/cli-refresh` / `POST /auth/cli-logout`
// (contracts/cli-refresh.md, monorepo `specs/104-cli-session-refresh/`).
//
// `login()`'s loopback-server flow (where `refreshToken` first enters the
// cache) is deliberately NOT exercised here — no test in this suite drives it
// end to end, because `openBrowser()` really does spawn `open`/`xdg-open` and
// nothing here should pop a browser window on a developer's machine or a CI
// runner. That one-line addition (`refreshToken: payload.refreshToken`) is
// symmetric with the three fields already carried the same way and is covered
// functionally by the monorepo's own `cli-handoff` integration tests, which
// prove the server includes it. What IS new, novel, and network-shaped —
// `refreshAccessToken`, the silent-refresh branch of `getCachedAccessToken`,
// and `logout`'s server-side revoke — is fully covered below with an injected
// `fetchImpl`, following the same DI pattern `EdgeBridgeClient` already uses.
import test from 'node:test';
import assert from 'node:assert/strict';
import { promises as fs } from 'node:fs';
import path from 'node:path';
import os from 'node:os';

import { getCachedAccessToken, refreshAccessToken, logout } from '../src/auth.mjs';

const HOSTED = { name: 'hosted', web: 'https://app.landfalls.ai', api: 'https://api.landfalls.ai' };

/** Each test gets its own config dir so the developer's real session is untouched. */
async function withCredentials(credentials, fn) {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), 'landfall-cli-refresh-'));
  const prev = process.env.XDG_CONFIG_HOME;
  process.env.XDG_CONFIG_HOME = dir;
  try {
    if (credentials) {
      await fs.mkdir(path.join(dir, 'landfall'), { recursive: true });
      await fs.writeFile(
        path.join(dir, 'landfall', 'credentials.json'),
        JSON.stringify(credentials),
        { mode: 0o600 },
      );
    }
    return await fn();
  } finally {
    if (prev === undefined) delete process.env.XDG_CONFIG_HOME;
    else process.env.XDG_CONFIG_HOME = prev;
    await fs.rm(dir, { recursive: true, force: true });
  }
}

async function readCredentials() {
  const p = path.join(process.env.XDG_CONFIG_HOME, 'landfall', 'credentials.json');
  return JSON.parse(await fs.readFile(p, 'utf8'));
}

function jsonResponse(status, body) {
  return { ok: status >= 200 && status < 300, status, json: async () => body };
}

/** Records every call so a test can assert both the outcome and the request shape. */
function fakeFetch(handler) {
  const calls = [];
  const fn = async (url, init) => {
    calls.push({ url, init, body: init?.body ? JSON.parse(init.body) : undefined });
    return handler(url, init);
  };
  fn.calls = calls;
  return fn;
}

test('getCachedAccessToken returns the cached token untouched when it is not near expiry', async () => {
  await withCredentials(
    {
      access_token: 'still-good',
      refresh_token: 'lf_refresh_x',
      expires_at: Date.now() + 600_000,
      instance: { api: HOSTED.api },
    },
    async () => {
      const fetchImpl = fakeFetch(() => { throw new Error('must not be called'); });
      const token = await getCachedAccessToken({ fetchImpl });
      assert.equal(token, 'still-good');
      assert.equal(fetchImpl.calls.length, 0, 'a fresh token must not trigger any network call');
    },
  );
});

test('getCachedAccessToken silently refreshes an expired token and persists the rotated pair (US1)', async () => {
  await withCredentials(
    {
      access_token: 'expired',
      refresh_token: 'lf_refresh_old',
      expires_at: Date.now() - 1_000,
      org_slug: 'acme',
      instance: { api: HOSTED.api },
    },
    async () => {
      const fetchImpl = fakeFetch((url) => {
        assert.equal(url, `${HOSTED.api}/auth/cli-refresh`);
        return jsonResponse(200, {
          accessToken: 'new-access',
          refreshToken: 'lf_refresh_new',
          expiresAt: new Date(Date.now() + 3_600_000).toISOString(),
        });
      });

      const token = await getCachedAccessToken({ fetchImpl });

      assert.equal(token, 'new-access', 'the caller gets a working token with no visible prompt');
      assert.equal(fetchImpl.calls.length, 1);
      assert.deepEqual(fetchImpl.calls[0].body, { refreshToken: 'lf_refresh_old' });

      const saved = await readCredentials();
      assert.equal(saved.access_token, 'new-access');
      assert.equal(saved.refresh_token, 'lf_refresh_new', 'rotation: the OLD refresh token is gone from disk');
      assert.equal(saved.org_slug, 'acme', 'unrelated fields survive the refresh');
      assert.ok(saved.expires_at > Date.now());
    },
  );
});

test('getCachedAccessToken refreshes PROACTIVELY within the skew window, not only after outright expiry', async () => {
  await withCredentials(
    {
      access_token: 'about-to-expire',
      refresh_token: 'lf_refresh_old',
      expires_at: Date.now() + 30_000, // inside the 60s skew, not yet expired
      instance: { api: HOSTED.api },
    },
    async () => {
      const fetchImpl = fakeFetch(() =>
        jsonResponse(200, {
          accessToken: 'new-access',
          refreshToken: 'lf_refresh_new',
          expiresAt: new Date(Date.now() + 3_600_000).toISOString(),
        }),
      );
      const token = await getCachedAccessToken({ fetchImpl });
      assert.equal(token, 'new-access');
      assert.equal(fetchImpl.calls.length, 1);
    },
  );
});

test('a refused refresh (reused/expired/revoked — FR-011 uniform 401) falls back to null, not a thrown error', async () => {
  await withCredentials(
    {
      access_token: 'expired',
      refresh_token: 'lf_refresh_dead',
      expires_at: Date.now() - 1_000,
      instance: { api: HOSTED.api },
    },
    async () => {
      const fetchImpl = fakeFetch(() => jsonResponse(401, { error: 'authentication_failed' }));
      const token = await getCachedAccessToken({ fetchImpl });
      assert.equal(token, null, 'the caller falls through to explainExpiredCredential — no crash');
    },
  );
});

test('a network error while refreshing (offline) is treated as "could not refresh", never thrown', async () => {
  await withCredentials(
    {
      access_token: 'expired',
      refresh_token: 'lf_refresh_x',
      expires_at: Date.now() - 1_000,
      instance: { api: HOSTED.api },
    },
    async () => {
      const fetchImpl = fakeFetch(() => { throw new Error('ENOTFOUND'); });
      assert.equal(await refreshAccessToken({ fetchImpl }), null);
    },
  );
});

test('a legacy credential with no refresh_token declines refresh without making a network call', async () => {
  await withCredentials(
    { access_token: 'expired', expires_at: Date.now() - 1_000, iss: 'landfall-core' },
    async () => {
      const fetchImpl = fakeFetch(() => { throw new Error('must not be called'); });
      assert.equal(await getCachedAccessToken({ fetchImpl }), null);
      assert.equal(fetchImpl.calls.length, 0);
    },
  );
});

test('a credential with a refresh_token but no recorded instance declines refresh (nothing to target)', async () => {
  await withCredentials(
    { access_token: 'expired', refresh_token: 'lf_refresh_x', expires_at: Date.now() - 1_000 },
    async () => {
      const fetchImpl = fakeFetch(() => { throw new Error('must not be called'); });
      assert.equal(await refreshAccessToken({ fetchImpl }), null);
      assert.equal(fetchImpl.calls.length, 0);
    },
  );
});

test('logout revokes server-side (FR-008) THEN clears the local file, even though the server call is fire-and-forget best-effort', async () => {
  await withCredentials(
    { access_token: 'x', refresh_token: 'lf_refresh_x', expires_at: Date.now() + 600_000, instance: { api: HOSTED.api } },
    async () => {
      const fetchImpl = fakeFetch((url) => {
        assert.equal(url, `${HOSTED.api}/auth/cli-logout`);
        return jsonResponse(204, undefined);
      });
      await logout({ fetchImpl });

      assert.equal(fetchImpl.calls.length, 1);
      assert.deepEqual(fetchImpl.calls[0].body, { refreshToken: 'lf_refresh_x' });

      const p = path.join(process.env.XDG_CONFIG_HOME, 'landfall', 'credentials.json');
      await assert.rejects(() => fs.readFile(p, 'utf8'), 'the local credential file must be gone');
    },
  );
});

test('logout still clears the local file when the revoke call fails (offline sign-out must still work)', async () => {
  await withCredentials(
    { access_token: 'x', refresh_token: 'lf_refresh_x', expires_at: Date.now() + 600_000, instance: { api: HOSTED.api } },
    async () => {
      const fetchImpl = fakeFetch(() => { throw new Error('offline'); });
      await assert.doesNotReject(() => logout({ fetchImpl }));

      const p = path.join(process.env.XDG_CONFIG_HOME, 'landfall', 'credentials.json');
      await assert.rejects(() => fs.readFile(p, 'utf8'));
    },
  );
});

test('logout on a legacy credential (no refresh_token) skips the network call and just clears the file', async () => {
  await withCredentials(
    { access_token: 'x', expires_at: Date.now() + 600_000, iss: 'landfall-core' },
    async () => {
      const fetchImpl = fakeFetch(() => { throw new Error('must not be called'); });
      await logout({ fetchImpl });
      assert.equal(fetchImpl.calls.length, 0);
    },
  );
});

test('logout with no cached session at all is a silent no-op, not a throw', async () => {
  await withCredentials(null, async () => {
    const fetchImpl = fakeFetch(() => { throw new Error('must not be called'); });
    await assert.doesNotReject(() => logout({ fetchImpl }));
  });
});
