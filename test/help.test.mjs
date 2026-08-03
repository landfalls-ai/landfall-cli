// help.test.mjs — v0.1.1 fix: `landfall --help`/`-h`/`help` must print usage and exit 0
// instead of falling through to `serve` (which would hang waiting on an MCP handshake).
// Found while writing homebrew-landfall's Formula test block (specs/050-extract-cli-homebrew).
import test from 'node:test';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const CLI_PATH = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', 'bin', 'landfall.mjs');

function run(args) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [CLI_PATH, ...args]);
    let stdout = '';
    child.stdout.on('data', (d) => (stdout += d));
    child.on('error', reject);
    child.on('close', (exitCode) => resolve({ stdout, exitCode }));
    child.stdin.end();
  });
}

for (const args of [['--help'], ['-h'], ['help']]) {
  test(`landfall ${args[0]} prints usage and exits 0`, async () => {
    const { stdout, exitCode } = await run(args);
    assert.equal(exitCode, 0);
    assert.match(stdout, /Usage: landfall <command>/);
    assert.match(stdout, /install \[--yes\]/); // feature 049's commands are documented too
  });
}
