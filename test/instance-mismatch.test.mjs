// FR-014: a cached session belongs to the Landfall that minted it.
//
// This file exists because of a real escape. v0.3.0 shipped
// `explainInstanceMismatch` fully implemented, unit-testable, and **never
// called**. Every test passed, because every test called the function
// directly. Nothing asserted it was reachable from a command, so the feature
// was dead code in the published build and FR-014 was only half satisfied
// (expiry was handled by older code; the mismatch branch was not).
//
// So the first test below deliberately does NOT test behaviour. It asserts
// WIRING: that the module implementing the check is imported by the entry
// point that needs it. A unit test of a function nobody calls proves nothing,
// and that is precisely the hole this closes.
import test from 'node:test';
import assert from 'node:assert/strict';
import { promises as fs } from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import { fileURLToPath } from 'node:url';

import { explainInstanceMismatch } from '../src/auth.mjs';

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

test('the mismatch check is actually reachable from the CLI entry point', async () => {
  const entry = await fs.readFile(path.join(REPO_ROOT, 'bin', 'landfall.mjs'), 'utf8');
  assert.match(
    entry,
    /explainInstanceMismatch/,
    'bin/landfall.mjs must import explainInstanceMismatch; without a caller it is dead code',
  );
  // Imported AND invoked. An unused import would satisfy the check above.
  assert.match(
    entry,
    /await\s+explainInstanceMismatch\s*\(/,
    'explainInstanceMismatch must be called, not merely imported',
  );
});

/** Each test gets its own config dir so the developer's real session is untouched. */
async function withCredentials(credentials, fn) {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), 'landfall-mismatch-'));
  const prev = process.env.XDG_CONFIG_HOME;
  process.env.XDG_CONFIG_HOME = dir;
  try {
    await fs.mkdir(path.join(dir, 'landfall'), { recursive: true });
    if (credentials) {
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

const HOSTED = { name: 'hosted', web: 'https://app.landfalls.ai', api: 'https://api.landfalls.ai' };

test('a session minted against another instance is refused with an explanation', async () => {
  await withCredentials(
    {
      access_token: 'x',
      expires_at: Date.now() + 600_000,
      instance: { api: 'https://landfall.other-company.com' },
    },
    async () => {
      const message = await explainInstanceMismatch(HOSTED);
      assert.ok(message, 'a cross-instance session must be reported, not used');
      assert.match(message, /landfall\.other-company\.com/, 'names the instance the session belongs to');
      assert.match(message, /api\.landfalls\.ai/, 'names the instance now targeted');
      assert.match(message, /landfall login/, 'offers a runnable fix');
    },
  );
});

test('a credential predating instance tracking is treated as a mismatch, never assumed to match', async () => {
  // The real shape found on the operator's machine: a valid-looking credential
  // with no `instance` field at all. Assuming it matches would send a token to
  // a deployment that never issued it.
  await withCredentials(
    { access_token: 'x', expires_at: Date.now() + 600_000, org_slug: 'landfall', iss: 'landfall-core' },
    async () => {
      const message = await explainInstanceMismatch(HOSTED);
      assert.ok(message, 'an untracked credential must not be silently trusted');
      assert.match(message, /predates instance tracking/i);
      assert.match(message, /landfall login/);
    },
  );
});

test('a session for the instance in use passes silently', async () => {
  await withCredentials(
    { access_token: 'x', expires_at: Date.now() + 600_000, instance: { api: HOSTED.api } },
    async () => {
      assert.equal(await explainInstanceMismatch(HOSTED), null);
    },
  );
});

test('no cached session at all is not a mismatch', async () => {
  await withCredentials(null, async () => {
    assert.equal(await explainInstanceMismatch(HOSTED), null);
  });
});
