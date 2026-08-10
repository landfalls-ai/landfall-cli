// THE one place that answers "where is Landfall".
//
// ── WHY THIS FILE EXISTS ──────────────────────────────────────────────────
//
// Before it, there were FOUR separate answers, and a customer could not reach
// Landfall at all. `landfall login` opened `http://localhost:5173` — a
// monorepo developer's Vite dev server — so a Homebrew install printed an
// address, opened a browser, and the browser said ERR_CONNECTION_REFUSED
// while the CLI reported nothing wrong. Meanwhile the API defaulted to
// `http://localhost:3001` in two files, `connect aws` defaulted to
// `https://api.landfalls.ai`, and failure messages linked
// `https://docs.landfalls.ai` — and neither of those last two hostnames
// existed in DNS, which is why `connect aws` had never worked for anyone.
//
// The cause was not carelessness. Spec 050 extracted this CLI from the
// monorepo with its local-development defaults intact, and each feature that
// came later added its own default in good faith because there was no shared
// one to reach for. That is a structural problem, so it has a structural fix:
// this module is the ONLY place allowed to name an address, and
// `test/no-hardcoded-address.test.mjs` fails the build if that stops being
// true.
//
// ── THE PRIORITY THIS FILE INVERTS ────────────────────────────────────────
//
// The old defaults optimised for the handful of people running the whole
// platform on a laptop, at the cost of every customer. For a program people
// install with `brew`, that is backwards. So the DEFAULT is now the hosted
// service, and local development is an explicit opt-in — which costs a
// monorepo developer nothing, because the environment variables they already
// set still win over everything else.
//
// Pure Node built-ins. No new dependency (this CLI has exactly one, and the
// point of a program installed with elevated trust is to keep it that way).
import { promises as fs } from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import http from 'node:http';
import https from 'node:https';

/**
 * The built-in default: the hosted Landfall.
 *
 * These hostnames deliberately name NO environment. The value ships inside
 * every installed copy and can only be changed by cutting a release AND
 * having every user upgrade, so anything environment-shaped ("dev") would
 * become permanent the moment it was published. Environment-neutral names
 * make a future repoint a DNS change instead of a migration.
 */
const HOSTED = Object.freeze({
  name: 'hosted',
  web: 'https://app.landfalls.ai',
  api: 'https://api.landfalls.ai',
  docs: 'https://docs.landfalls.ai',
});

/** Documentation is not per-deployment. A self-hoster reads the same guides,
 * so a custom instance keeps the hosted docs rather than being required to
 * run a docs site just to make error messages resolve. */
const DOCS_FALLBACK = HOSTED.docs;

const CONFIG_FILE = 'config.json';

function configDir() {
  const base = process.env.XDG_CONFIG_HOME || path.join(os.homedir(), '.config');
  return path.join(base, 'landfall');
}

function configPath() {
  return path.join(configDir(), CONFIG_FILE);
}

/** Loopback covers the shapes local development actually uses. */
function isLoopback(hostname) {
  return (
    hostname === 'localhost' ||
    hostname === '127.0.0.1' ||
    hostname === '::1' ||
    hostname === '[::1]' ||
    hostname.endsWith('.localhost')
  );
}

/**
 * Parse and normalise one address.
 *
 * Trailing slashes are stripped HERE so that every consumer can concatenate a
 * path without each one re-deciding, which is the kind of small inconsistency
 * that produces `//cli-auth` in a URL a user is asked to trust.
 *
 * Plain http is refused for a remote host (it would send a bearer token in
 * clear text) but accepted in silence for loopback, because that is simply
 * what local development is and warning about it every time would train
 * people to ignore warnings.
 */
export function parseAddress(value, { label = 'address' } = {}) {
  if (typeof value !== 'string' || value.trim() === '') {
    throw new Error(`The ${label} is empty. Expected a URL such as https://app.landfalls.ai`);
  }
  let url;
  try {
    url = new URL(value.trim());
  } catch {
    throw new Error(
      `The ${label} "${value}" is not a valid URL. Expected something like https://app.landfalls.ai`,
    );
  }
  if (url.protocol !== 'http:' && url.protocol !== 'https:') {
    throw new Error(`The ${label} "${value}" must use http or https, not ${url.protocol}`);
  }
  if (url.protocol === 'http:' && !isLoopback(url.hostname)) {
    throw new Error(
      `The ${label} "${value}" uses plain http over the network, which would send your ` +
        `credentials in clear text. Use https, or a local address if you are running Landfall ` +
        `on this machine.`,
    );
  }
  return url.toString().replace(/\/+$/, '');
}

/** Read the persisted nomination. A broken file is treated as absent and says
 * so, rather than making every command fail until someone finds and deletes
 * it. */
async function readNomination() {
  let raw;
  try {
    raw = await fs.readFile(configPath(), 'utf8');
  } catch {
    return null; // absent is the normal case, not an error
  }
  try {
    const parsed = JSON.parse(raw);
    const instance = parsed?.instance;
    if (!instance || typeof instance !== 'object') return null;
    return instance;
  } catch {
    process.emitWarning(
      `Ignoring ${configPath()}: it is not valid JSON. Run \`landfall instance reset\` to clear it.`,
    );
    return null;
  }
}

/** Persist a nomination. Not a secret, and deliberately NOT in
 * credentials.json: signing out must not discard a self-hoster's choice. */
export async function saveNomination(instance) {
  await fs.mkdir(configDir(), { recursive: true });
  const body = JSON.stringify({ instance }, null, 2);
  await fs.writeFile(configPath(), body, 'utf8');
  return instance;
}

/** Return to the built-in default. Must be possible without uninstalling or
 * hand-editing a file. */
export async function clearNomination() {
  try {
    await fs.unlink(configPath());
    return true;
  } catch {
    return false; // already absent
  }
}

/**
 * Resolve the instance, highest precedence first:
 *
 *   1. per-endpoint environment variables (LANDFALL_WEB_URL, LANDFALL_BASE_URL)
 *   2. a nomination: an explicit --url, LANDFALL_URL, or the persisted config
 *   3. the built-in hosted default
 *
 * Level 1 is deliberately the HIGHEST. It is what monorepo developers already
 * export today, so their setup keeps working byte-for-byte; those variables
 * stop being the default, not the mechanism.
 *
 * Returns every address or throws. It never resolves partially, so no command
 * can end up talking to the hosted API with a locally-nominated web address.
 */
export async function resolveInstance({ url = null, env = process.env } = {}) {
  const nominatedRaw = url ?? env.LANDFALL_URL ?? null;
  const persisted = nominatedRaw ? null : await readNomination();

  let base = { ...HOSTED };
  let source = 'default';

  if (nominatedRaw) {
    const address = parseAddress(nominatedRaw, { label: 'instance address' });
    // A single address nominates BOTH the sign-in surface and the service,
    // which is the common self-hosted shape (one origin). Someone whose
    // deployment splits them uses the per-endpoint variables below, which win.
    base = { name: 'custom', web: address, api: address, docs: DOCS_FALLBACK };
    source = url ? 'flag' : 'env:LANDFALL_URL';
  } else if (persisted) {
    base = {
      name: persisted.name === 'hosted' ? 'hosted' : 'custom',
      web: parseAddress(persisted.web ?? HOSTED.web, { label: 'saved web address' }),
      api: parseAddress(persisted.api ?? persisted.web ?? HOSTED.api, { label: 'saved API address' }),
      docs: parseAddress(persisted.docs ?? DOCS_FALLBACK, { label: 'saved docs address' }),
    };
    source = 'config';
  }

  // Level 1 last, so it overrides whatever the levels below produced.
  const overrides = [];
  if (env.LANDFALL_WEB_URL) {
    base.web = parseAddress(env.LANDFALL_WEB_URL, { label: 'LANDFALL_WEB_URL' });
    overrides.push('LANDFALL_WEB_URL');
  }
  if (env.LANDFALL_BASE_URL) {
    base.api = parseAddress(env.LANDFALL_BASE_URL, { label: 'LANDFALL_BASE_URL' });
    overrides.push('LANDFALL_BASE_URL');
  }
  if (overrides.length > 0) {
    base.name = 'local';
    source = `env:${overrides.join('+')}`;
  }

  return Object.freeze({ ...base, source });
}

// ── Reachability ──────────────────────────────────────────────────────────
//
// The reported defect was not only the wrong address. It was that the CLI
// printed one, opened a browser, and reported nothing, leaving a browser
// error page as the only diagnosis. So failure is CLASSIFIED, not uniform:
// a uniform "could not connect" would trade one unhelpful message for
// another.

/** Bounded so a preflight never dominates the sign-in budget. */
const PREFLIGHT_TIMEOUT_MS = 3_000;

export const REACHABILITY = Object.freeze({
  OK: 'ok',
  UNRESOLVED: 'unresolved',
  REFUSED: 'refused',
  LOCAL_DEAD: 'local-dead',
  TIMEOUT: 'timeout',
  NOT_LANDFALL: 'not-landfall',
});

/**
 * Probe an address. Returns a classification, never throws.
 *
 * A timeout is deliberately NOT fatal to the caller (see `describeFailure`):
 * a strict check would turn a slow link, a proxy, or a captive portal into
 * "the product is broken", which is a worse failure than the one being fixed.
 */
export async function probe(address, { timeoutMs = PREFLIGHT_TIMEOUT_MS } = {}) {
  const url = new URL(address);
  const client = url.protocol === 'https:' ? https : http;
  return new Promise((resolve) => {
    let settled = false;
    const done = (status) => {
      if (!settled) {
        settled = true;
        resolve(status);
      }
    };
    const req = client.request(
      { method: 'HEAD', hostname: url.hostname, port: url.port || undefined, path: '/', timeout: timeoutMs },
      (res) => {
        res.resume();
        // Any HTTP answer proves something is listening and speaking HTTP.
        // Distinguishing "Landfall" from "some other server" is left to the
        // real request that follows; claiming more from a HEAD of / would be
        // guessing.
        done(REACHABILITY.OK);
      },
    );
    req.on('timeout', () => {
      req.destroy();
      done(REACHABILITY.TIMEOUT);
    });
    req.on('error', (err) => {
      if (err?.code === 'ENOTFOUND' || err?.code === 'EAI_AGAIN') return done(REACHABILITY.UNRESOLVED);
      if (err?.code === 'ECONNREFUSED') {
        return done(isLoopback(url.hostname) ? REACHABILITY.LOCAL_DEAD : REACHABILITY.REFUSED);
      }
      done(REACHABILITY.REFUSED);
    });
    req.end();
  });
}

/**
 * Turn a classification into the message contract: WHAT was tried, WHY it
 * failed, and a command that FIXES it. Every one of the three is required;
 * a message missing any of them is what made the original bug a dead end.
 *
 * Returns null when there is nothing to say (reachable, or an ambiguous
 * timeout the caller should warn about but not block on).
 */
export function describeFailure(status, instance) {
  const fixes =
    `\n  If your organization runs its own Landfall:\n` +
    `      landfall login --url https://landfall.example.com --save\n` +
    `  If you are running Landfall locally:\n` +
    `      LANDFALL_WEB_URL=http://localhost:5173 LANDFALL_BASE_URL=http://localhost:3001 landfall login`;

  switch (status) {
    case REACHABILITY.UNRESOLVED:
      return `Could not reach Landfall at ${instance.web}\n  That address did not resolve.${fixes}`;
    case REACHABILITY.REFUSED:
      return `Could not reach Landfall at ${instance.web}\n  The connection was refused.${fixes}`;
    case REACHABILITY.LOCAL_DEAD:
      // The exact case the operator hit, named as itself rather than as a
      // generic connection error.
      return (
        `Could not reach Landfall at ${instance.web}\n` +
        `  That is a local development address, and nothing is listening on it.\n` +
        `  If you meant to use the hosted Landfall, clear the local setting:\n` +
        `      unset LANDFALL_WEB_URL LANDFALL_BASE_URL\n` +
        `      landfall instance reset` +
        fixes
      );
    case REACHABILITY.NOT_LANDFALL:
      return (
        `${instance.web} answered, but it does not look like a Landfall deployment.\n` +
        `  Check the address is the Landfall web app and not something else.${fixes}`
      );
    default:
      return null;
  }
}

/** Exit code for "the instance is wrong or unreachable", kept distinct from a
 * general failure so scripts and onboarding flows can tell "wrong address"
 * from "wrong credentials". */
export const EXIT_UNREACHABLE = 2;

export const DEFAULT_INSTANCE = HOSTED;
