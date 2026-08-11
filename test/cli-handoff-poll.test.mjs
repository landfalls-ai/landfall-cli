// cli-handoff-poll.test.mjs — the CLI-side half of feature 105 (Safari
// mixed-content fix): `landfall login` no longer binds a loopback listener
// and waits for the browser to push a session to it. It opens the browser
// and POLLS `POST /auth/cli-handoff/poll` with the nonce it generated —
// the SAME `fetchImpl`-injected shape `cli-refresh.test.mjs` already uses
// for `refreshAccessToken`/`logout`, and for the identical reason: this is
// the network-shaped half of the flow, testable without spawning a real
// browser (`login()` itself still is not driven end to end here — see that
// file's header comment).
import test from 'node:test';
import assert from 'node:assert/strict';

import { pollForHandoff } from '../src/auth.mjs';

const API = 'https://api.landfalls.ai';

function jsonResponse(status, body) {
  return { ok: status >= 200 && status < 300, status, json: async () => body };
}

/** Records every call so a test can assert both the outcome and the request shape. */
function fakeFetch(handler) {
  const calls = [];
  const fn = async (url, init) => {
    calls.push({ url, init, body: init?.body ? JSON.parse(init.body) : undefined });
    return handler(url, init, calls.length);
  };
  fn.calls = calls;
  return fn;
}

test('resolves on the first 200 with the minted session', async () => {
  const fetchImpl = fakeFetch((url) => {
    assert.equal(url, `${API}/auth/cli-handoff/poll`);
    return jsonResponse(200, {
      accessToken: 'access-1',
      refreshToken: 'lf_refresh_1',
      expiresAt: new Date(Date.now() + 3_600_000).toISOString(),
      orgSlug: 'acme',
    });
  });

  const session = await pollForHandoff(API, 'the-nonce', undefined, {
    fetchImpl,
    intervalMs: 0,
  });

  assert.equal(session.accessToken, 'access-1');
  assert.equal(session.refreshToken, 'lf_refresh_1');
  assert.equal(session.orgSlug, 'acme');
  assert.equal(fetchImpl.calls.length, 1);
  assert.deepEqual(fetchImpl.calls[0].body, { nonce: 'the-nonce' });
});

test('a 404 ("not yet", or "never existed" — indistinguishable on purpose) keeps polling until it succeeds', async () => {
  const fetchImpl = fakeFetch((_url, _init, callNumber) => {
    if (callNumber < 3) return jsonResponse(404, { error: 'not_found' });
    return jsonResponse(200, {
      accessToken: 'access-eventually',
      expiresAt: new Date(Date.now() + 3_600_000).toISOString(),
    });
  });

  const session = await pollForHandoff(API, 'the-nonce', undefined, {
    fetchImpl,
    intervalMs: 0,
  });

  assert.equal(session.accessToken, 'access-eventually');
  assert.equal(fetchImpl.calls.length, 3);
});

test('a network error (offline blip) is tolerated, not thrown — the browser side can take minutes', async () => {
  let attempt = 0;
  const fetchImpl = async () => {
    attempt += 1;
    if (attempt < 3) throw new Error('ENOTFOUND');
    return jsonResponse(200, {
      accessToken: 'access-after-blip',
      expiresAt: new Date(Date.now() + 3_600_000).toISOString(),
    });
  };

  const session = await pollForHandoff(API, 'the-nonce', undefined, {
    fetchImpl,
    intervalMs: 0,
  });

  assert.equal(session.accessToken, 'access-after-blip');
  assert.equal(attempt, 3);
});

test('gives up at the timeout, never hanging forever', async () => {
  const fetchImpl = fakeFetch(() => jsonResponse(404, { error: 'not_found' }));

  await assert.rejects(
    () => pollForHandoff(API, 'the-nonce', undefined, { fetchImpl, intervalMs: 5, timeoutMs: 30 }),
    /timed out waiting for the browser/,
  );
});

test('falls back to the CALLER-supplied orgSlug only when the server response omits one', async () => {
  const fetchImpl = fakeFetch(() =>
    jsonResponse(200, { accessToken: 'x', expiresAt: new Date().toISOString(), orgSlug: null }),
  );

  const session = await pollForHandoff(API, 'the-nonce', 'fallback-org', {
    fetchImpl,
    intervalMs: 0,
  });

  assert.equal(session.orgSlug, 'fallback-org');
});

test('the server-supplied orgSlug wins over the caller-supplied one when both are present', async () => {
  const fetchImpl = fakeFetch(() =>
    jsonResponse(200, { accessToken: 'x', expiresAt: new Date().toISOString(), orgSlug: 'server-org' }),
  );

  const session = await pollForHandoff(API, 'the-nonce', 'caller-org', {
    fetchImpl,
    intervalMs: 0,
  });

  assert.equal(session.orgSlug, 'server-org');
});

test('a malformed JSON response is tolerated as "not yet", not a crash', async () => {
  let attempt = 0;
  const fetchImpl = async () => {
    attempt += 1;
    if (attempt === 1) {
      return { ok: true, status: 200, json: async () => { throw new SyntaxError('bad json'); } };
    }
    return jsonResponse(200, { accessToken: 'ok', expiresAt: new Date().toISOString() });
  };

  const session = await pollForHandoff(API, 'the-nonce', undefined, { fetchImpl, intervalMs: 0 });
  assert.equal(session.accessToken, 'ok');
});
