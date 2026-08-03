// prompt.mjs — the minimal interactive selection UI for `landfall install`
// (specs/049-cli-mcp-harness-installer/research.md R4). A small readline-based
// per-item confirm, not a full raw-mode checkbox widget: it's simple to test
// (feed it lines) and matches this library's near-zero dependency convention.
//
// Deliberately consumes `input` as an async iterator (one line per question)
// rather than repeated `readline.question()` calls: `question()` from
// `node:readline/promises` is unreliable once the underlying stream reaches
// EOF (exactly what a piped, non-interactive `stdin` does immediately after
// all its lines are written) — a later `question()` call can hang forever
// instead of resolving with the already-buffered line. Iterating the stream
// directly has no such failure mode and works identically for a real TTY.
import readline from 'node:readline';

/**
 * Ask the user, one at a time, whether to select each item (all pre-checked
 * — pressing Enter accepts the default). Returns the array of selected item
 * ids, preserving `items`' order.
 *
 * @param {{id: string, label: string}[]} items
 * @param {{skipPrompt?: boolean, input?: NodeJS.ReadableStream, output?: NodeJS.WritableStream}} [opts]
 *   `skipPrompt: true` (the `--yes` flag) selects every item with no I/O at all.
 */
export async function promptSelection(items, { skipPrompt = false, input = process.stdin, output = process.stdout } = {}) {
  if (skipPrompt || items.length === 0) return items.map((i) => i.id);

  const rl = readline.createInterface({ input });
  const lines = rl[Symbol.asyncIterator]();
  const selected = [];
  try {
    for (const item of items) {
      output.write(`Configure ${item.label}? [Y/n] `);
      const { value, done } = await lines.next();
      const answer = done ? '' : String(value).trim().toLowerCase();
      if (!answer.startsWith('n')) selected.push(item.id);
    }
  } finally {
    rl.close();
  }
  return selected;
}
