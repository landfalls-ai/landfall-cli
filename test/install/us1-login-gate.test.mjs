// us1-login-gate.test.mjs — T010: no cached session → the login flow runs
// before any harness is detected/presented for selection. Tested against
// runInstall directly (dependency-injected) rather than spawning the CLI, so
// this never touches the real (interactive, network-bound) login flow.
import test from 'node:test';
import assert from 'node:assert/strict';
import { runInstall } from '../../src/install/commands.mjs';

function fakeHarness(overrides = {}) {
  return {
    id: 'h1',
    displayName: 'H1',
    detect: async () => true,
    install: async () => ({ status: 'configured' }),
    ...overrides,
  };
}

test('runInstall triggers login before detecting any harness when no token is cached', async () => {
  const calls = [];
  let signedIn = false;
  const harness = fakeHarness({ detect: async () => (calls.push('detect'), true) });

  const result = await runInstall([], {
    harnesses: [harness],
    login: async () => {
      calls.push('login');
      signedIn = true;
    },
    getCachedAccessToken: async () => (signedIn ? 'tok' : null),
    promptSelectionFn: async (items) => items.map((i) => i.id),
    isOnPathFn: async () => true,
  });

  assert.deepEqual(calls, ['login', 'detect']);
  assert.equal(result.outcomes[0].status, 'configured');
});

test('runInstall does not call login when a session is already cached', async () => {
  const calls = [];
  const harness = fakeHarness();

  await runInstall([], {
    harnesses: [harness],
    login: async () => calls.push('login'),
    getCachedAccessToken: async () => 'already-signed-in',
    promptSelectionFn: async (items) => items.map((i) => i.id),
    isOnPathFn: async () => true,
  });

  assert.deepEqual(calls, []);
});

test('runInstall aborts with exit code 2 when login never produces a token', async () => {
  const result = await runInstall([], {
    harnesses: [fakeHarness()],
    login: async () => {},
    getCachedAccessToken: async () => null,
  });

  assert.equal(result.exitCode, 2);
  assert.match(result.usageError, /sign-in/);
  assert.deepEqual(result.outcomes, []);
});
