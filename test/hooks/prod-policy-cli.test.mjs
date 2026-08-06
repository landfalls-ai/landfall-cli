// prod-policy-cli.test.mjs — `landfall hooks policy` and `landfall hooks
// pre-tool-use` as the host actually runs them: a real spawned process, a real
// policy file, and a real HTTP server standing in for Landfall (#233).
//
// The unit tests inject a fake `confirm`, so they prove the handler's logic.
// This file proves the thing that logic exists for, with nothing stubbed: a
// spawned hook has no controlling terminal, therefore nobody can have pressed
// `y`, therefore the socket must stay silent. If that ever stops being true,
// the request count here goes to 1.
import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { promises as fs } from 'node:fs';
import path from 'node:path';
import { withSandbox, runCli, seedSession, writeJson } from '../install/helpers.mjs';

const POLICY = {
  version: 1,
  rules: [
    {
      id: 'kubectl-prod',
      description: 'kubectl aimed at production',
      command: 'kubectl',
      allOf: ['--context=prod'],
      category: 'kubernetes',
      entityHints: ['prod-cluster'],
    },
  ],
};

function policyFile(homeDir) {
  return path.join(homeDir, '.config', 'landfall', 'prod-policy.json');
}

/** A stand-in Landfall that records every request it receives. */
async function withRecordingApi(fn) {
  const requests = [];
  const server = http.createServer((req, res) => {
    const chunks = [];
    req.on('data', (c) => chunks.push(c));
    req.on('end', () => {
      requests.push({ url: req.url, body: Buffer.concat(chunks).toString('utf8') });
      res.writeHead(200, { 'content-type': 'application/json' });
      res.end(JSON.stringify({ decision: 'none' }));
    });
  });
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  try {
    return await fn({ baseUrl: `http://127.0.0.1:${server.address().port}`, requests });
  } finally {
    await new Promise((resolve) => server.close(resolve));
  }
}

test('a matched command sends NOTHING when there is no terminal to confirm on', async () => {
  await withSandbox(async ({ homeDir }) => {
    await seedSession(homeDir);
    await writeJson(policyFile(homeDir), POLICY);

    await withRecordingApi(async ({ baseUrl, requests }) => {
      process.env.LANDFALL_BASE_URL = baseUrl;
      const { stdout, exitCode } = await runCli(['hooks', 'pre-tool-use'], {
        input: JSON.stringify({
          tool_name: 'Bash',
          tool_input: { command: 'kubectl --context=prod get secret db-root -o yaml' },
        }),
      });

      assert.equal(exitCode, 0, 'a PreToolUse hook must never block the tool call');
      assert.equal(stdout, '', 'stdout belongs to the host');
      assert.deepEqual(requests, [], 'an unconfirmed intent reached the network');
    });
  });
});

test('a non-matching command exits 0, silently, and touches nothing', async () => {
  await withSandbox(async ({ homeDir }) => {
    await seedSession(homeDir);
    await writeJson(policyFile(homeDir), POLICY);

    await withRecordingApi(async ({ baseUrl, requests }) => {
      process.env.LANDFALL_BASE_URL = baseUrl;
      const { stdout, stderr, exitCode } = await runCli(['hooks', 'pre-tool-use'], {
        input: JSON.stringify({ tool_name: 'Bash', tool_input: { command: 'git status' } }),
      });

      assert.equal(exitCode, 0);
      assert.equal(stdout, '');
      assert.equal(stderr, '', 'a non-matching command should not say anything at all');
      assert.deepEqual(requests, []);
    });
  });
});

test('hooks policy on a machine with no policy explains that nothing is reported', async () => {
  await withSandbox(async () => {
    const { stdout, exitCode } = await runCli(['hooks', 'policy']);
    assert.equal(exitCode, 0);
    assert.match(stdout, /no policy file/);
    assert.match(stdout, /--init/);
  });
});

test('hooks policy prints the exact payload each rule would send', async () => {
  await withSandbox(async ({ homeDir }) => {
    await writeJson(policyFile(homeDir), POLICY);
    const { stdout, exitCode } = await runCli(['hooks', 'policy']);

    assert.equal(exitCode, 0);
    assert.match(stdout, /kubectl-prod — kubectl aimed at production/);
    assert.match(stdout, /matches: kubectl containing all of "--context=prod"/);
    assert.match(stdout, /sends: \{"category":"kubernetes","entityHints":\["prod-cluster"\],"startedAt":"<when you confirm>"\}/);
    assert.match(stdout, /The command line itself never leaves this machine\./);
  });
});

test('hooks policy reports a rejected rule and exits non-zero', async () => {
  await withSandbox(async ({ homeDir }) => {
    await writeJson(policyFile(homeDir), {
      version: 1,
      rules: [{ id: 'leaky', command: 'kubectl', category: 'kubernetes', entityHints: ['kubectl -n prod exec'] }],
    });
    const { stdout, exitCode } = await runCli(['hooks', 'policy']);

    assert.equal(exitCode, 1);
    assert.match(stdout, /! rule "leaky".*bare identifier/);
    assert.match(stdout, /no usable rules/);
  });
});

test('hooks policy --init writes a starter and never overwrites an existing one', async () => {
  await withSandbox(async ({ homeDir }) => {
    const first = await runCli(['hooks', 'policy', '--init']);
    assert.equal(first.exitCode, 0);
    assert.match(first.stdout, /wrote starter policy/);

    const written = await fs.readFile(policyFile(homeDir), 'utf8');
    assert.match(written, /\$comment/);

    const second = await runCli(['hooks', 'policy', '--init']);
    assert.match(second.stdout, /already exists: .*\(not overwritten\)/);
    assert.equal(await fs.readFile(policyFile(homeDir), 'utf8'), written);
  });
});

test('the starter policy is itself valid — `--init` then `policy` never reports an error', async () => {
  await withSandbox(async () => {
    await runCli(['hooks', 'policy', '--init']);
    const { stdout, exitCode } = await runCli(['hooks', 'policy']);
    assert.equal(exitCode, 0);
    assert.doesNotMatch(stdout, /^\s*!/m);
  });
});
