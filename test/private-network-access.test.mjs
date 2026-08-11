// private-network-access.test.mjs
//
// A real, live-confirmed bug (2026-08-11, found while driving `landfall
// login` end to end against real Chrome): the loopback callback listener's
// CORS preflight was missing `Access-Control-Allow-Private-Network: true`.
// Chrome's Private Network Access policy gives an HTTPS page fetching a
// private/loopback address (this listener) a SEPARATE preflight check on top
// of ordinary CORS, and silently fails the whole request — a generic
// `TypeError: Failed to fetch`, no distinguishing detail — unless the
// preflight response carries this header. Without it, `landfall login` hung
// on "Connecting your command line…" until its own 5-minute timeout, in
// EVERY Chromium browser enforcing PNA. Firefox/Safari don't enforce PNA,
// which is exactly why this was never caught by hand-testing there.
//
// `login()` itself is not driven end to end here — see
// test/cli-refresh.test.mjs's header comment for why (it really does spawn
// `open`/`xdg-open`). preflightHeaders() is pulled out specifically so the
// header can be asserted directly, and the second test below is the same
// "wiring" discipline test/instance-mismatch.test.mjs established: a correct
// function nobody calls proves nothing.
import test from 'node:test';
import assert from 'node:assert/strict';
import { promises as fs } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import { preflightHeaders } from '../src/auth.mjs';

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

test('the preflight response allows Chrome Private Network Access', () => {
  const headers = preflightHeaders('https://app.landfalls.ai');
  assert.equal(headers['access-control-allow-private-network'], 'true');
});

test('the preflight response still carries ordinary CORS for the specific web origin', () => {
  const headers = preflightHeaders('https://app.landfalls.ai');
  assert.equal(headers['access-control-allow-origin'], 'https://app.landfalls.ai');
  assert.match(headers['access-control-allow-methods'], /POST/);
});

test('preflightHeaders is actually used by the loopback listener, not dead code', async () => {
  const src = await fs.readFile(path.join(REPO_ROOT, 'src', 'auth.mjs'), 'utf8');
  assert.match(
    src,
    /res\.writeHead\(204,\s*preflightHeaders\(webUrl\)\)/,
    'the OPTIONS branch in login() must build its response with preflightHeaders(), ' +
      'or a header fixed only in the export is not actually fixed for a real browser',
  );
});
