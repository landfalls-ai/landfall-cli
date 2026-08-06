// policy.mjs — the visible local allow-list that decides whether a command the
// engineer is about to run counts as "touching production", and — this is the
// part that matters — what may be said about it (#233, story #192).
//
// ── The property this file exists to guarantee ────────────────────────────
// Story #192's hard constraint is that a *classified intent* leaves the
// machine, never a raw command line. #231 enforces that at the server's edge
// with a `.strict()` schema whose `entityHints` cannot hold a space. This file
// enforces something stronger, one layer earlier:
//
//   NOTHING DERIVED FROM THE COMMAND LINE IS EVER PUT INTO THE PAYLOAD.
//
// A rule's `category` and `entityHints` are literals the engineer typed into
// this file. The command line is only ever an input to a boolean — does this
// rule match, yes or no. There is no extraction step, no tokenizer, no "pull
// the namespace out of the -n flag". That is what makes the acceptance
// criterion ("the engineer can read exactly what leaves the machine") literally
// true rather than approximately true: the set of things that can leave is the
// set of strings visible in this file, and it can be read in full with
// `landfall hooks policy`.
//
// ── Matching is literal, never heuristic ──────────────────────────────────
// #192 says "explicit allow-list of prod-identifying patterns (not heuristics)"
// and #233 repeats it. So a rule matches on:
//
//   command  the program name, compared exactly against the first token of the
//            command line (basename, so `/usr/local/bin/kubectl` still matches
//            `kubectl`)
//   allOf    literal substrings that must ALL appear
//   noneOf   literal substrings that must NONE appear
//
// No regular expressions — not as a style preference but because a regex in a
// user-editable file that runs on every Bash call is both an unreviewable
// heuristic and a denial-of-service surface against the engineer's own shell.
//
// A rule that fails validation is SKIPPED and reported, never silently
// repaired: a policy file the engineer believes says one thing while it does
// another is the failure mode this whole design is built to avoid.
import { promises as fs } from 'node:fs';
import path from 'node:path';
import { xdgConfigHome } from '../install/platform.mjs';

/**
 * The classification vocabulary, mirroring `INTENT_CATEGORIES` in the server's
 * shared contracts package (`edge-intent.ts`). It is duplicated rather than shared
 * because this repo is deliberately standalone (feature 050) and depends on
 * nothing in the monorepo — but it is a CLOSED list at both ends, so a rule
 * naming a category the server would reject is caught here, on the engineer's
 * machine, instead of as a 400 in the middle of an incident.
 */
export const INTENT_CATEGORIES = [
  'kubernetes',
  'cloud',
  'database',
  'logs',
  'deployment',
  'network',
  'other',
];

/**
 * The same bound the server's `EntityHintSchema` applies: start alphanumeric,
 * then identifier punctuation only — no whitespace, no shell metacharacter.
 * Checked here so an unsendable hint is a policy-file error the engineer sees
 * at rest, not a rejected request during an outage.
 */
export const entityHintPattern = /^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,127}$/;

/** The server caps the list at 10; a longer one is a dump, not a classification. */
export const MAX_ENTITY_HINTS = 10;

/**
 * The policy file lives beside the cached session (`credentials.json`) under
 * the XDG config home, because it is the same kind of thing: per-user local
 * state for this CLI. It is NOT in the repo being worked on — a policy that
 * travelled with a checkout would let a cloned repository decide what an
 * engineer's machine reports.
 */
export function policyPath() {
  return path.join(xdgConfigHome(), 'landfall', 'prod-policy.json');
}

/** A commented starter policy, written by `landfall hooks policy --init`. */
export function starterPolicy() {
  return {
    $comment:
      'Landfall prod-investigation allow-list. A command matches only if `command` ' +
      'equals its program name and every string in `allOf` appears in it. ONLY the ' +
      '`category` and `entityHints` written here are ever sent — never the command line. ' +
      'Run `landfall hooks policy` to print exactly what each rule would send.',
    version: 1,
    rules: [
      {
        id: 'example-kubectl-prod-context',
        description: 'kubectl aimed at the production cluster',
        command: 'kubectl',
        allOf: ['--context=prod'],
        category: 'kubernetes',
        entityHints: ['prod-cluster'],
      },
    ],
  };
}

/**
 * Validate one rule. Returns `{rule}` or `{error}` — never a partially
 * corrected rule.
 */
export function validateRule(raw, index) {
  const where = `rule ${raw?.id ? `"${raw.id}"` : `#${index + 1}`}`;
  if (!raw || typeof raw !== 'object') return { error: `${where}: not an object` };
  if (typeof raw.id !== 'string' || !raw.id.trim()) return { error: `${where}: missing "id"` };
  if (typeof raw.command !== 'string' || !raw.command.trim()) {
    return { error: `${where}: missing "command" (the program name to match)` };
  }
  if (!INTENT_CATEGORIES.includes(raw.category)) {
    return { error: `${where}: "category" must be one of ${INTENT_CATEGORIES.join(', ')}` };
  }

  const literals = (value, key) => {
    if (value === undefined) return [];
    if (!Array.isArray(value) || value.some((s) => typeof s !== 'string' || !s.length)) return null;
    return value;
  };
  const allOf = literals(raw.allOf, 'allOf');
  const noneOf = literals(raw.noneOf, 'noneOf');
  if (allOf === null) return { error: `${where}: "allOf" must be a list of non-empty strings` };
  if (noneOf === null) return { error: `${where}: "noneOf" must be a list of non-empty strings` };

  const hints = raw.entityHints ?? [];
  if (!Array.isArray(hints)) return { error: `${where}: "entityHints" must be a list` };
  if (hints.length > MAX_ENTITY_HINTS) {
    return { error: `${where}: at most ${MAX_ENTITY_HINTS} entity hints (found ${hints.length})` };
  }
  for (const hint of hints) {
    if (typeof hint !== 'string' || !entityHintPattern.test(hint)) {
      return {
        error: `${where}: entity hint ${JSON.stringify(hint)} is not a bare identifier — no whitespace, no shell metacharacters`,
      };
    }
  }

  return {
    rule: {
      id: raw.id,
      description: typeof raw.description === 'string' ? raw.description : '',
      command: raw.command,
      allOf,
      noneOf,
      category: raw.category,
      entityHints: [...hints],
    },
  };
}

/**
 * Read and validate the policy file.
 *
 * A missing file is NOT an error: it is the shipped state, and it means this
 * feature does nothing at all. Returns `{exists, rules, errors}` — `errors`
 * carries every rejected rule so the caller can print them; the surviving rules
 * are still usable, because one bad rule should not disarm the others.
 */
export async function loadPolicy(filePath = policyPath()) {
  let text;
  try {
    text = await fs.readFile(filePath, 'utf8');
  } catch (err) {
    if (err.code === 'ENOENT') return { exists: false, rules: [], errors: [] };
    return { exists: true, rules: [], errors: [`cannot read ${filePath}: ${err.message}`] };
  }

  let parsed;
  try {
    parsed = JSON.parse(text);
  } catch (err) {
    return { exists: true, rules: [], errors: [`${filePath} is not valid JSON: ${err.message}`] };
  }
  if (!Array.isArray(parsed?.rules)) {
    return { exists: true, rules: [], errors: [`${filePath}: expected a "rules" array`] };
  }

  const rules = [];
  const errors = [];
  parsed.rules.forEach((raw, i) => {
    const { rule, error } = validateRule(raw, i);
    if (error) errors.push(error);
    else rules.push(rule);
  });
  return { exists: true, rules, errors };
}

/**
 * The program name a command line invokes: basename of the command word.
 *
 * Two shapes are handled beyond "first token", both because missing them would
 * make a rule quietly not fire on a command the engineer plainly meant it to:
 *
 *   VAR=value kubectl …   leading environment assignments are part of the same
 *                         simple command in POSIX shell grammar, and
 *                         `KUBECONFIG=… kubectl` is how people actually reach a
 *                         second cluster
 *   "/opt/my tools/kubectl"   a quoted command word, taken whole
 *
 * Deliberately NOT handled: wrappers (`sudo kubectl`, `env kubectl`, `xargs`),
 * pipelines, and `;`-separated lists. Those invoke a different program, and a
 * matcher that looked "through" them would be exactly the heuristic #192 rules
 * out. An engineer who runs prod commands under `sudo` writes a `sudo` rule,
 * and can see that they did.
 */
export function programName(commandLine) {
  let rest = String(commandLine ?? '').trim();
  // Skip leading NAME=value assignments (unquoted values only — a quoted value
  // may contain spaces, and guessing where it ends is parsing).
  while (/^[A-Za-z_][A-Za-z0-9_]*=[^\s'"]*(\s|$)/.test(rest)) {
    rest = rest.slice(rest.search(/\s|$/)).trimStart();
  }
  if (!rest) return '';

  const quote = rest[0] === '"' || rest[0] === "'" ? rest[0] : null;
  const word = quote
    ? rest.slice(1, rest.indexOf(quote, 1) < 0 ? undefined : rest.indexOf(quote, 1))
    : rest.split(/\s+/)[0];
  if (!word) return '';
  return word.split(/[/\\]/).pop() ?? '';
}

/** True when `rule` matches `commandLine`. Pure, literal, allocation-light. */
export function ruleMatches(rule, commandLine) {
  const line = String(commandLine ?? '');
  if (programName(line) !== rule.command) return false;
  if (rule.allOf.some((needle) => !line.includes(needle))) return false;
  if (rule.noneOf.some((needle) => line.includes(needle))) return false;
  return true;
}

/** The first matching rule, in file order, or null. File order is the engineer's. */
export function matchCommand(rules, commandLine) {
  return rules.find((rule) => ruleMatches(rule, commandLine)) ?? null;
}

/**
 * The EXACT request body a matched rule produces — the whole payload, derived
 * only from the rule plus a timestamp. There is deliberately no `commandLine`
 * parameter: this function cannot see the command line, so it cannot leak it,
 * and that is checked by a test rather than trusted.
 */
export function intentFor(rule, startedAt) {
  return {
    category: rule.category,
    entityHints: [...rule.entityHints],
    startedAt: (startedAt ?? new Date()).toISOString(),
  };
}
