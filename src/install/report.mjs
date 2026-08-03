// report.mjs — the Installation Report: the single place that turns an array
// of per-harness AdapterOutcomes into the exact output shape promised by
// specs/049-cli-mcp-harness-installer/contracts/cli.md, and the exit code
// that goes with it.

/** Every status a `landfall install` outcome can carry. */
export const INSTALL_STATUSES = Object.freeze([
  'not-detected',
  'configured',
  'would-configure', // --dry-run stand-in for `configured`
  'already-installed',
  'skipped', // user did not select a detected harness
  'conflict',
  'failed',
]);

/** Every status a `landfall uninstall` outcome can carry. */
export const UNINSTALL_STATUSES = Object.freeze([
  'not-installed',
  'removed',
  'left-in-place',
  'failed',
]);

/**
 * Render one AdapterOutcome as the contract's single output line:
 *   `<display-name>: <status>[ — <detail>][ (<config-path>)]`
 */
export function formatOutcomeLine({ displayName, status, detail, configPath }) {
  let line = `${displayName}: ${status}`;
  if (detail) line += ` — ${detail}`;
  if (configPath) line += ` (${configPath})`;
  return line;
}

/** Render the full Installation Report, one line per outcome, in the given order. */
export function formatReport(outcomes) {
  return outcomes.map(formatOutcomeLine).join('\n');
}

/**
 * The `landfall install` / `landfall uninstall` exit code convention from
 * contracts/cli.md: 1 if any outcome is `failed`, else 0. Usage errors
 * (bad --only value, aborted login) are a separate, earlier exit(2) the
 * caller raises directly — this only looks at completed outcomes.
 */
export function exitCodeForOutcomes(outcomes) {
  return outcomes.some((o) => o.status === 'failed') ? 1 : 0;
}
