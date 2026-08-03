// helpers.mjs — shared test fixtures for the `landfall install`/`uninstall` test suite
// (specs/049-cli-mcp-harness-installer, T001).
//
// Every adapter test needs two things: a machine that isn't the real one running the
// test (so we never touch a developer's actual ~/.cursor/mcp.json), and, for the two
// harnesses driven by shelling out to their own CLI (Claude Code, Codex), a way to
// observe what was invoked without the real `claude`/`codex` binaries being installed.
import { promises as fs } from 'node:fs';
import { spawn } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import os from 'node:os';
import path from 'node:path';

const CLI_PATH = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', '..', 'bin', 'landfall.mjs');

/**
 * Run `fn({ homeDir, pathDir })` with HOME/USERPROFILE/APPDATA/XDG_CONFIG_HOME
 * redirected into a fresh temp directory, and a temp directory prepended onto
 * PATH for stub executables. Restores the real environment and removes the
 * temp directory afterward, even if `fn` throws.
 */
// The minimal set of directories a stubbed `which`/`where` and a spawned
// child process still need to function, deliberately excluding the rest of
// the real machine's PATH — otherwise a harness genuinely installed on the
// machine RUNNING the tests (e.g. this very `claude` binary) would leak into
// detect() and make the test suite's result depend on whoever's laptop runs it.
function minimalSystemPath() {
  return process.platform === 'win32'
    ? [process.env.SystemRoot ? path.join(process.env.SystemRoot, 'System32') : 'C:\\Windows\\System32']
    : ['/usr/bin', '/bin', '/usr/sbin', '/sbin'];
}

/**
 * Run `fn({ homeDir, pathDir })` with HOME/USERPROFILE/APPDATA/XDG_CONFIG_HOME
 * redirected into a fresh temp directory, PATH replaced by a temp stub
 * directory plus the minimal system PATH (see {@link minimalSystemPath}), and
 * app-bundle detection (`LANDFALL_TEST_APP_ROOT`) redirected into the same
 * temp tree — so nothing already installed on the machine running the tests
 * can be detected as if it were on the sandboxed one. Restores the real
 * environment and removes the temp directory afterward, even if `fn` throws.
 */
export async function withSandbox(fn) {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'landfall-install-test-'));
  const homeDir = path.join(root, 'home');
  const pathDir = path.join(root, 'bin');
  const appRoot = path.join(root, 'Applications');
  await fs.mkdir(homeDir, { recursive: true });
  await fs.mkdir(pathDir, { recursive: true });
  await fs.mkdir(appRoot, { recursive: true });

  const savedEnv = { ...process.env };
  process.env.HOME = homeDir;
  process.env.USERPROFILE = homeDir;
  process.env.APPDATA = path.join(homeDir, 'AppData', 'Roaming');
  process.env.LOCALAPPDATA = path.join(homeDir, 'AppData', 'Local');
  process.env.XDG_CONFIG_HOME = path.join(homeDir, '.config');
  process.env.PATH = [pathDir, ...minimalSystemPath()].join(path.delimiter);
  process.env.LANDFALL_TEST_APP_ROOT = appRoot;

  try {
    return await fn({ homeDir, pathDir, appRoot });
  } finally {
    for (const key of Object.keys(process.env)) delete process.env[key];
    Object.assign(process.env, savedEnv);
    await fs.rm(root, { recursive: true, force: true });
  }
}

/**
 * Drop a stub executable named `name` onto `pathDir` that records every
 * invocation's argv (one JSON array per line) to a log file, then exits 0
 * (or with `exitCode` / prints `stdout` if given, for negative-path tests).
 * Returns the log file path and a `readInvocations()` helper.
 */
export async function stubExecutable(pathDir, name, { exitCode = 0, stdout = '' } = {}) {
  const logPath = path.join(pathDir, `${name}.invocations.log`);
  const scriptPath = path.join(pathDir, name);
  // Plain CommonJS body (no top-level `import`): the stub lives in a bare
  // temp directory with no package.json, so Node's default module type
  // applies regardless of this package's own "type": "module".
  const script = [
    '#!/usr/bin/env node',
    `require('node:fs').appendFileSync(${JSON.stringify(logPath)}, JSON.stringify(process.argv.slice(2)) + '\\n');`,
    stdout ? `process.stdout.write(${JSON.stringify(stdout)});` : '',
    `process.exit(${exitCode});`,
  ]
    .filter(Boolean)
    .join('\n');
  await fs.writeFile(scriptPath, script, { mode: 0o755 });
  await fs.chmod(scriptPath, 0o755);

  return {
    scriptPath,
    async readInvocations() {
      const text = await fs.readFile(logPath, 'utf8').catch(() => '');
      return text
        .split('\n')
        .filter(Boolean)
        .map((line) => JSON.parse(line));
    },
  };
}

/** Read + JSON.parse a file, or return `undefined` if it doesn't exist. */
export async function readJson(filePath) {
  try {
    return JSON.parse(await fs.readFile(filePath, 'utf8'));
  } catch (err) {
    if (err.code === 'ENOENT') return undefined;
    throw err;
  }
}

export async function writeJson(filePath, value) {
  await fs.mkdir(path.dirname(filePath), { recursive: true });
  await fs.writeFile(filePath, JSON.stringify(value, null, 2) + '\n', 'utf8');
}

/**
 * Seed a valid cached `landfall login` session under the sandboxed
 * XDG_CONFIG_HOME, so a spawned CLI process treats itself as signed in
 * without ever running the real (interactive, network-bound) login flow.
 * Mirrors the shape src/auth.mjs's `saveSession` writes.
 */
export async function seedSession(homeDir) {
  const credPath = path.join(homeDir, '.config', 'landfall', 'credentials.json');
  await writeJson(credPath, {
    access_token: 'test-token',
    expires_at: Date.now() + 3_600_000,
    org_slug: 'acme',
    iss: 'landfall-core',
  });
}

/**
 * Spawn the real `landfall` CLI binary (for true end-to-end contract tests)
 * with the current (sandboxed) process env, optionally feeding `stdin` lines
 * for interactive prompts. Resolves with `{ stdout, stderr, exitCode }`.
 */
export function runCli(args, { input } = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [CLI_PATH, ...args], { env: process.env });
    let stdout = '';
    let stderr = '';
    child.stdout.on('data', (d) => (stdout += d));
    child.stderr.on('data', (d) => (stderr += d));
    child.on('error', reject);
    child.on('close', (exitCode) => resolve({ stdout, stderr, exitCode }));
    if (input != null) {
      child.stdin.write(input);
    }
    child.stdin.end();
  });
}
