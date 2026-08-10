// no-hardcoded-address.test.mjs — structural guard for feature 092
// (specs/092-cli-instance-address, contracts §7, SC-004).
//
// `src/instance.mjs` is the ONLY file allowed to name a Landfall address or a
// local development URL. Everything else asks it.
//
// This guard exists because the bug it prevents already happened, and not
// through carelessness: spec 050 extracted this CLI from the monorepo with its
// local-development defaults intact, and then FOUR features each added their
// own default in good faith, because there was no shared one to reach for. By
// the time it was noticed, `landfall login` opened a developer's Vite dev
// server for every customer, and `connect aws` pointed at a hostname that had
// never existed. Review did not catch it four times; a failing build will.
//
// Modelled on this repo's existing no-monorepo-coupling.test.mjs, which guards
// spec 050's invariant the same way.
import test from 'node:test';
import assert from 'node:assert/strict';
import { promises as fs } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const THIS_FILE = fileURLToPath(import.meta.url);
const REPO_ROOT = path.resolve(path.dirname(THIS_FILE), '..');
const SCAN_DIRS = ['bin', 'src'];

/** The one file permitted to name an address, relative to the repo root. */
const SOURCE_OF_TRUTH = path.join('src', 'instance.mjs');

/**
 * A Landfall hostname, or a localhost/loopback URL used as a DEFAULT for
 * reaching Landfall. Both are "an answer to where Landfall is", and both
 * belong in exactly one file.
 *
 * Deliberately narrow: it matches addresses, not the words. A comment may
 * discuss `localhost` (several do, explaining this very history) without
 * tripping this, because comments are stripped first.
 */
const ADDRESS_PATTERNS = [
  { name: 'a Landfall hostname', re: /https?:\/\/[a-z0-9.-]*landfalls\.ai/i },
  { name: 'a loopback URL with a port', re: /https?:\/\/(localhost|127\.0\.0\.1|\[::1\]):\d+/i },
];

/**
 * The CLI binds its OWN loopback listener during sign-in, to receive the
 * browser's callback. `http://127.0.0.1` with no port (the port is chosen by
 * the OS at bind time) is that listener's origin, not an answer to "where is
 * Landfall", so it is not what this guard is about.
 *
 * Narrowly allowed rather than broadly ignored: a port-bearing loopback URL
 * like `http://localhost:5173` is still caught everywhere, including in this
 * same file, because that IS a default for reaching Landfall.
 */
const ALLOWED = [/https?:\/\/127\.0\.0\.1(?![:\d])/];

async function walk(dir) {
  const entries = await fs.readdir(dir, { withFileTypes: true });
  const files = [];
  for (const entry of entries) {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) files.push(...(await walk(full)));
    else if (entry.name.endsWith('.mjs')) files.push(full);
  }
  return files;
}

/** Strip line and block comments, so prose explaining the history is allowed
 * while executable code carrying an address is not. */
function stripComments(source) {
  return source.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^[ \t]*\/\/.*$/gm, '');
}

test('only src/instance.mjs names a Landfall or localhost address', async () => {
  const files = (await Promise.all(SCAN_DIRS.map((d) => walk(path.join(REPO_ROOT, d))))).flat();
  assert.ok(files.length > 0, 'expected to scan at least one source file');

  const offenders = [];
  for (const file of files) {
    const relative = path.relative(REPO_ROOT, file);
    if (relative === SOURCE_OF_TRUTH) continue;
    let code = stripComments(await fs.readFile(file, 'utf8'));
    for (const allowed of ALLOWED) code = code.replace(new RegExp(allowed, 'gi'), '');
    for (const { name, re } of ADDRESS_PATTERNS) {
      const match = code.match(re);
      if (match) offenders.push(`${relative} contains ${name}: ${match[0]}`);
    }
  }

  assert.deepEqual(
    offenders,
    [],
    'Every address must come from src/instance.mjs.\n' +
      'Import DEFAULT_INSTANCE or call resolveInstance() instead of writing an address here.\n' +
      `Found:\n  ${offenders.join('\n  ')}`,
  );
});

test('the source of truth ships no environment-specific default', async () => {
  // A default naming an environment ("dev") would become permanent the moment
  // it was published, since changing it needs a release AND every user to
  // upgrade. That is the whole reason the hosted default is environment-neutral.
  const code = stripComments(await fs.readFile(path.join(REPO_ROOT, SOURCE_OF_TRUTH), 'utf8'));
  const defaults = code.match(/https:\/\/[a-z0-9.-]*landfalls\.ai/gi) ?? [];
  assert.ok(defaults.length > 0, 'expected the hosted default to be defined here');
  for (const address of defaults) {
    assert.ok(
      !/(^|[.-])(dev|staging|test)\./i.test(address.replace('https://', '')),
      `${address} names an environment; the shipped default must be environment-neutral`,
    );
  }
});
