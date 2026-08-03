// no-monorepo-coupling.test.mjs — structural guard for data-model.md's invariant
// (specs/050-extract-cli-homebrew, FR-004/SC-002/SC-005): this repo must never
// reference a path that only exists inside the private `landfall` monorepo
// (`libs/`, `apps/`) or escape its own root via a relative path. A future PR
// that reintroduces that coupling fails CI here instead of silently working
// only inside a monorepo checkout.
import test from 'node:test';
import assert from 'node:assert/strict';
import { promises as fs } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const THIS_FILE = fileURLToPath(import.meta.url);
const REPO_ROOT = path.resolve(path.dirname(THIS_FILE), '..');
const SCAN_DIRS = ['bin', 'src', 'test'];
const FORBIDDEN = [/(^|[\s'"`(])libs\//, /(^|[\s'"`(])apps\//, /\.\.\/\.\.\/\.\./];

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

test('no source file references a monorepo-only path (libs/, apps/, or a deep ../../../ escape)', async () => {
  const files = (await Promise.all(SCAN_DIRS.map((d) => walk(path.join(REPO_ROOT, d)))))
    .flat()
    .filter((f) => f !== THIS_FILE);
  assert.ok(files.length > 0, 'expected to scan at least one file');

  const offenders = [];
  for (const file of files) {
    const text = await fs.readFile(file, 'utf8');
    for (const pattern of FORBIDDEN) {
      if (pattern.test(text)) offenders.push(`${path.relative(REPO_ROOT, file)} matches ${pattern}`);
    }
  }
  assert.deepEqual(offenders, []);
});
