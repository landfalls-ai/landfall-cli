// remediation/commands.mjs — `landfall remediation approve` (feature
// 20260812-010632, T030, contracts/incident-sim-and-override.md §2,
// research.md D6).
//
// HUMAN-TYPED ONLY, never an MCP tool. Constitution Principle V: an MCP tool
// is, by construction, something an LLM agent can call on its own initiative
// mid-conversation. Exposing the corroboration-gate override there would let
// an agent talk itself past the exact gate that feature exists to add,
// collapsing "propose, human approves" back into "propose, agent can also
// just approve." A CLI subcommand is a human typing an explicit command with
// an explicit reason string — the same authenticated-human action as clicking
// "override and approve" in the browser, just through a different terminal.
// Uses the CLI's own OWN authenticated session (`landfall login`), the same
// pattern `landfall connect aws` (feature 081) already established — NOT the
// edge-bridge bearer token (`EdgeBridgeClient`/`join_war_room`), which is
// scoped to agent contributions, not human approvals.
//
// Real endpoint shape, corrected from an earlier draft's shorthand by reading
// the actual controller: `POST /o/:slug/incidents/:incidentId/remediation/
// proposals/:remediationId/approve` (not `/remediations/...`). `version` is
// typed non-optional in the controller's ApproveBody interface, but the
// SERVICE layer only checks it when truthy (`if (version && version !==
// state.version)`) — so an omitted version is safe at runtime and lets this
// command work without first fetching the proposal's exact current version
// string; `--version` is offered for the caller who already knows it.
import { getCachedAccessToken, getCachedOrgSlug } from '../auth.mjs';
import { DEFAULT_INSTANCE } from '../instance.mjs';

/** Parse `remediation approve <remediationId> --incident <id> [...]` flags. */
export function parseRemediationApproveFlags(argv) {
  const args = [...argv];
  const flags = {};
  const takeValue = (name) => {
    const i = args.indexOf(name);
    if (i < 0) return null;
    const value = args[i + 1] ?? null;
    args.splice(i, value === null ? 1 : 2);
    return value;
  };
  flags.incident = takeValue('--incident');
  flags.org = takeValue('--org');
  flags.version = takeValue('--version');
  const override = takeValue('--override');
  if (override !== null) flags.override = { reason: override };

  const positionals = args.filter((a) => !a.startsWith('--'));
  flags.remediationId = positionals[0] ?? null;

  if (!flags.remediationId) return { error: 'usage: landfall remediation approve <remediationId> --incident <incidentId> [--org <slug>] [--override "<reason>"]' };
  if (!flags.incident) return { error: 'landfall remediation approve requires --incident <incidentId> — a remediation id alone does not identify which incident it belongs to' };
  return { flags };
}

/**
 * The command body. `deps` are injectable for tests (same convention as
 * `runConnectAws`): {fetchImpl, log, error, getToken, getSlug, baseUrl, env}.
 * Returns a process exit code.
 */
export async function runRemediationApprove(flags, deps = {}) {
  const log = deps.log ?? console.log;
  const error = deps.error ?? console.error;
  const fetchImpl = deps.fetchImpl ?? globalThis.fetch;
  const getToken = deps.getToken ?? getCachedAccessToken;
  const getSlug = deps.getSlug ?? getCachedOrgSlug;
  const env = deps.env ?? process.env;
  const baseUrl = (deps.baseUrl ?? env.LANDFALL_BASE_URL ?? DEFAULT_INSTANCE.api).replace(/\/$/, '');

  // 1. Session, before anything touches the network — same order connect aws
  // already established (FR-005 there, the same principle applies here).
  const token = await getToken();
  if (!token) {
    error('Not signed in. Run: landfall login   — then re-run: landfall remediation approve …');
    return 1;
  }
  const slug = await getSlug();
  if (!slug) {
    error('This session has no organization pin. Run: landfall login   (and pick your organization)');
    return 1;
  }
  if (flags.org && flags.org !== slug) {
    error(
      `This session is signed in to "${slug}", not "${flags.org}".\n` +
        `Run: landfall login   against ${flags.org} first — a session is pinned to one organization.`,
    );
    return 1;
  }

  const body = { ...(flags.version ? { version: flags.version } : {}), ...(flags.override ? { override: flags.override } : {}) };
  const url = `${baseUrl}/o/${slug}/incidents/${flags.incident}/remediation/proposals/${flags.remediationId}/approve`;
  const res = await fetchImpl(url, {
    method: 'POST',
    headers: { authorization: `Bearer ${token}`, 'content-type': 'application/json' },
    body: JSON.stringify(body),
  });

  if (res.ok) { // the real endpoint answers 202 on success; res.ok already covers it
    log(
      flags.override
        ? `✓ Approved (override recorded: reason="${flags.override.reason}")`
        : '✓ Approved.',
    );
    return 0;
  }

  let payload = {};
  try {
    payload = await res.json();
  } catch {
    /* no/unparseable body */
  }

  if (res.status === 403 && payload?.error === 'override_requires_human_session') {
    error(
      'Refused: the override can only be invoked from a human-authenticated session, never an API key. ' +
        'Sign in with `landfall login` and re-run without a scripted credential.',
    );
    return 1;
  }
  if (res.status === 400 && payload?.error === 'remediation_not_admitted') {
    error(
      `Refused: this proposal's hypothesis is not yet admitted — ${payload.shortfall ?? 'insufficient corroboration'}.\n` +
        (flags.override
          ? 'An override was supplied but was not accepted — see the message above.'
          : 'Corroborate the claim first, or re-run with --override "<reason>" if you are genuinely the only one who can act right now.'),
    );
    return 1;
  }
  if (res.status === 400 && payload?.error === 'override_reason_required') {
    error('Refused: --override was passed with no reason. Try: --override "why you are overriding this"');
    return 1;
  }
  error(`Refused (HTTP ${res.status})${payload?.error ? `: ${payload.error}` : ''}${payload?.message ? ` — ${payload.message}` : ''}`);
  return 1;
}
