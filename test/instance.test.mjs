// Feature 092: the single instance notion.
//
// These tests exist because the bug they prevent was not a typo. Four separate
// features each added their own answer to "where is Landfall", in good faith,
// because there was no shared one. The precedence assertions below are the
// contract that keeps existing local development working while customers get a
// reachable default; the guard test in no-hardcoded-address.test.mjs is what
// stops a fifth private default appearing.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { promises as fs } from 'node:fs';
import path from 'node:path';
import os from 'node:os';

import {
  resolveInstance,
  parseAddress,
  saveNomination,
  clearNomination,
  describeFailure,
  REACHABILITY,
  DEFAULT_INSTANCE,
} from '../src/instance.mjs';

/** Each test gets its own config dir, so nothing touches the developer's own. */
async function withTempConfig(fn) {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), 'landfall-instance-'));
  const prev = process.env.XDG_CONFIG_HOME;
  process.env.XDG_CONFIG_HOME = dir;
  try {
    return await fn(dir);
  } finally {
    if (prev === undefined) delete process.env.XDG_CONFIG_HOME;
    else process.env.XDG_CONFIG_HOME = prev;
    await fs.rm(dir, { recursive: true, force: true });
  }
}

test('defaults to the hosted instance, and names no environment', async () => {
  await withTempConfig(async () => {
    const instance = await resolveInstance({ env: {} });
    assert.equal(instance.web, 'https://app.landfalls.ai');
    assert.equal(instance.api, 'https://api.landfalls.ai');
    assert.equal(instance.docs, 'https://docs.landfalls.ai');
    assert.equal(instance.source, 'default');
    // The whole point of Q1 option A: the shipped value must survive a future
    // production cutover without a release, so it may not name an environment.
    for (const address of [instance.web, instance.api, instance.docs]) {
      assert.ok(!/\bdev\b/.test(address), `${address} must not name an environment`);
      assert.ok(!/localhost|127\.0\.0\.1/.test(address), `${address} must not be a local address`);
    }
  });
});

test('per-endpoint env vars win over everything, so local development is unchanged', async () => {
  await withTempConfig(async () => {
    await saveNomination({ name: 'custom', web: 'https://nominated.example', api: 'https://nominated.example', docs: DEFAULT_INSTANCE.docs });
    const instance = await resolveInstance({
      url: 'https://flag.example',
      env: { LANDFALL_WEB_URL: 'http://localhost:5173', LANDFALL_BASE_URL: 'http://localhost:3001' },
    });
    assert.equal(instance.web, 'http://localhost:5173');
    assert.equal(instance.api, 'http://localhost:3001');
    assert.match(instance.source, /^env:/);
  });
});

test('--url beats a saved nomination', async () => {
  await withTempConfig(async () => {
    await saveNomination({ name: 'custom', web: 'https://saved.example', api: 'https://saved.example', docs: DEFAULT_INSTANCE.docs });
    const instance = await resolveInstance({ url: 'https://flag.example', env: {} });
    assert.equal(instance.web, 'https://flag.example');
    assert.equal(instance.source, 'flag');
  });
});

test('a saved nomination beats the built-in default, and survives a new invocation', async () => {
  await withTempConfig(async () => {
    await saveNomination({ name: 'custom', web: 'https://saved.example', api: 'https://saved.example', docs: DEFAULT_INSTANCE.docs });
    const first = await resolveInstance({ env: {} });
    assert.equal(first.web, 'https://saved.example');
    assert.equal(first.source, 'config');
    // A separate resolve is a separate "invocation" as far as this module is
    // concerned: nothing is memoised, so persistence is what carries it.
    const second = await resolveInstance({ env: {} });
    assert.equal(second.web, 'https://saved.example');
  });
});

test('reset returns to the default', async () => {
  await withTempConfig(async () => {
    await saveNomination({ name: 'custom', web: 'https://saved.example', api: 'https://saved.example', docs: DEFAULT_INSTANCE.docs });
    await clearNomination();
    const instance = await resolveInstance({ env: {} });
    assert.equal(instance.web, DEFAULT_INSTANCE.web);
    assert.equal(instance.source, 'default');
  });
});

test('a nomination missing a field is completed from the level below, never left undefined', async () => {
  await withTempConfig(async () => {
    await saveNomination({ name: 'custom', web: 'https://partial.example' });
    const instance = await resolveInstance({ env: {} });
    assert.equal(instance.web, 'https://partial.example');
    assert.equal(instance.api, 'https://partial.example'); // falls back to web
    assert.equal(instance.docs, DEFAULT_INSTANCE.docs); // docs are not per-deployment
    for (const value of Object.values(instance)) assert.notEqual(value, undefined);
  });
});

test('a malformed config file is ignored rather than breaking every command', async () => {
  await withTempConfig(async (dir) => {
    await fs.mkdir(path.join(dir, 'landfall'), { recursive: true });
    await fs.writeFile(path.join(dir, 'landfall', 'config.json'), '{ not json', 'utf8');
    const instance = await resolveInstance({ env: {} });
    assert.equal(instance.web, DEFAULT_INSTANCE.web);
  });
});

test('addresses are validated, and normalised without a trailing slash', () => {
  assert.equal(parseAddress('https://example.com/'), 'https://example.com');
  assert.equal(parseAddress('https://example.com///'), 'https://example.com');
  assert.throws(() => parseAddress(''), /empty/i);
  assert.throws(() => parseAddress('not-a-url'), /not a valid URL/i);
  assert.throws(() => parseAddress('ftp://example.com'), /http or https/i);
  // Plain http to a remote host would put a bearer token on the wire in clear.
  assert.throws(() => parseAddress('http://example.com'), /clear text/i);
  // ...but local development is exactly this, and must stay silent.
  assert.equal(parseAddress('http://localhost:5173'), 'http://localhost:5173');
  assert.equal(parseAddress('http://127.0.0.1:3001'), 'http://127.0.0.1:3001');
});

test('every failure message says what was tried, why, and how to fix it', () => {
  const instance = { ...DEFAULT_INSTANCE, source: 'default' };
  for (const status of [REACHABILITY.UNRESOLVED, REACHABILITY.REFUSED, REACHABILITY.NOT_LANDFALL]) {
    const message = describeFailure(status, instance);
    assert.ok(message, `${status} must produce a message`);
    assert.ok(message.includes(instance.web), 'must name the address that was tried');
    assert.match(message, /landfall login --url|landfall instance reset/, 'must offer a runnable fix');
  }
});

test('a dead local address is named as such, not reported as a generic failure', () => {
  // The exact case the operator hit: LANDFALL_WEB_URL pointing at a dev server
  // that is not running. A bare "connection refused" is what made it a dead end.
  const local = { name: 'local', web: 'http://localhost:5173', api: 'http://localhost:3001', docs: DEFAULT_INSTANCE.docs, source: 'env' };
  const message = describeFailure(REACHABILITY.LOCAL_DEAD, local);
  assert.match(message, /local development address/i);
  assert.match(message, /nothing is listening/i);
  assert.match(message, /unset LANDFALL_WEB_URL/);
});

test('an ambiguous timeout produces no fatal message, so a slow link does not block sign-in', () => {
  assert.equal(describeFailure(REACHABILITY.TIMEOUT, DEFAULT_INSTANCE), null);
  assert.equal(describeFailure(REACHABILITY.OK, DEFAULT_INSTANCE), null);
});
