// connect/commands.mjs — `landfall connect aws` (feature 081,
// landfalls-ai/landfall#1168): one command from the CLI's pinned session to a
// healthy role-based AWS connection. Zero copy-paste, zero AWS credentials
// held by Landfall, zero new dependencies.
//
// Every step either completed or didn't, and every failure says what
// completed, what didn't, and the one action that resumes (FR-011/FR-014).
// The org is the SESSION's own pin (feature 043 — a session belongs to exactly
// one organization); `--org` is only a guard against operating on the wrong
// one, never a re-target.
import { randomBytes } from 'node:crypto';
import { writeFile, mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import {
  DEFAULT_MEMBER_ROLE_NAME,
  DEFAULT_STACK_NAME,
  awsPreflight,
  defaultRun,
  deployRoleStack,
  fetchTemplate,
  memberInstructions,
  readRoleArn,
  terraformSnippet,
  validateAccountId,
  validateRoleArn,
} from './aws.mjs';
import { getCachedAccessToken, getCachedOrgSlug } from '../auth.mjs';

const DEFAULT_API = 'https://api.landfalls.ai';

/** Parse `connect aws` flags from argv (already past the subcommand words). */
export function parseConnectFlags(argv) {
  const flags = { members: [] };
  for (let i = 0; i < argv.length; i += 1) {
    const a = argv[i];
    if (a === '--terraform') flags.terraform = true;
    else if (a === '--management') flags.management = true;
    else if (a === '--org') flags.org = argv[++i];
    else if (a === '--name') flags.name = argv[++i];
    else if (a === '--region') flags.region = argv[++i];
    else if (a === '--member') flags.members.push(...String(argv[++i] ?? '').split(',').filter(Boolean));
    else if (a === '--member-role-name') flags.memberRoleName = argv[++i];
    else return { error: `unknown flag for connect aws: ${a}` };
  }
  if (flags.members.length > 0 && !flags.management) {
    return { error: '--member requires --management' };
  }
  if (flags.management && flags.members.length === 0) {
    return { error: '--management needs at least one --member <accountId>' };
  }
  for (const m of flags.members) {
    if (!validateAccountId(m)) {
      return { error: `--member accounts are 12-digit AWS account ids (got "${m}")` };
    }
  }
  return { flags };
}

/**
 * The whole flow. `deps` are injectable for tests: {fetchImpl, run, prompt,
 * log, error, getToken, getSlug, baseUrl, env}. Returns a process exit code.
 */
export async function runConnectAws(flags, deps = {}) {
  const log = deps.log ?? console.log;
  const error = deps.error ?? console.error;
  const fetchImpl = deps.fetchImpl ?? globalThis.fetch;
  const run = deps.run ?? defaultRun;
  const getToken = deps.getToken ?? getCachedAccessToken;
  const getSlug = deps.getSlug ?? getCachedOrgSlug;
  const env = deps.env ?? process.env;
  const baseUrl = (deps.baseUrl ?? env.LANDFALL_BASE_URL ?? DEFAULT_API).replace(/\/$/, '');

  // 1. Session (FR-005) — before anything touches AWS or the network.
  const token = await getToken();
  if (!token) {
    error('Not signed in. Run: landfall login   — then re-run: landfall connect aws');
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
  log(`Connecting AWS to organization: ${slug}`);

  const api = async (method, p, body) => {
    const res = await fetchImpl(`${baseUrl}${p}`, {
      method,
      headers: { authorization: `Bearer ${token}`, 'content-type': 'application/json' },
      ...(body ? { body: JSON.stringify(body) } : {}),
    });
    return res;
  };

  // 2. The platform's principal (FR-015: a 404 is an OLD platform, not a bug).
  const infoRes = await api('GET', `/o/${slug}/integrations/aws/onboarding-info`);
  if (infoRes.status === 404) {
    error(
      'This Landfall deployment does not support automated AWS onboarding yet (no onboarding-info ' +
        'endpoint). Use the manual path: https://docs.landfalls.ai/integrations/aws',
    );
    return 1;
  }
  if (infoRes.status === 401 || infoRes.status === 403) {
    error('Connecting integrations needs an organization administrator. Ask an admin to run this, or to grant you admin.');
    return 1;
  }
  if (!infoRes.ok) {
    error(`onboarding-info answered HTTP ${infoRes.status} — try again, or use the manual path.`);
    return 1;
  }
  const info = await infoRes.json();
  if (!info.available) {
    error(
      `Automated role onboarding is not available on this deployment: ${info.reason}\n` +
        'Use keys-mode setup instead: https://docs.landfalls.ai/integrations/aws',
    );
    return 1;
  }

  // 3. External id — local, strong, never echoed or logged (FR-007).
  const externalId = randomBytes(24).toString('base64url');

  let roleArn;
  let accountId;
  const region = flags.region ?? info.suggestedRegion; // then the customer's own aws default
  if (flags.terraform) {
    // 4a. Terraform shops (FR-012): print the pinned snippet, take the ARN back.
    log('Apply this in your Terraform (pinned release), then paste the role ARN it outputs:\n');
    log(terraformSnippet({ principalArn: info.principalArn, externalId }));
    log('');
    const prompt = deps.prompt;
    if (!prompt) {
      error('no interactive prompt available for --terraform in this environment');
      return 1;
    }
    for (;;) {
      const pasted = (await prompt('role ARN: ')).trim();
      if (validateRoleArn(pasted)) {
        roleArn = pasted;
        break;
      }
      error('that is not an IAM role ARN (expected arn:aws:iam::<12 digits>:role/<name>) — try again');
    }
    accountId = /::(\d{12}):/.exec(roleArn)?.[1] ?? '';
  } else {
    // 4b. Default path: the CUSTOMER's own aws CLI creates the role (FR-008).
    const identity = await awsPreflight(run).catch((e) => {
      error(String(e.message ?? e));
      return null;
    });
    if (!identity) return 1;
    accountId = identity.accountId;
    log(`Creating the read-only role in AWS account ${accountId}${region ? ` (region ${region})` : ' (your aws CLI default region)'} …`);

    // Template: pinned + checksum-verified BEFORE any use (FR-009/SC-004).
    let template;
    try {
      template = await fetchTemplate(fetchImpl, deps.pin);
    } catch (e) {
      error(String(e.message ?? e));
      return 1;
    }
    const dir = await mkdtemp(path.join(tmpdir(), 'landfall-connect-'));
    const templateFile = path.join(dir, 'landfall-readonly-role.yaml');
    await writeFile(templateFile, template, 'utf8');
    try {
      await deployRoleStack(run, {
        templateFile,
        stackName: DEFAULT_STACK_NAME,
        principalArn: info.principalArn,
        externalId,
        region,
      });
      roleArn = await readRoleArn(run, { stackName: DEFAULT_STACK_NAME, region });
    } catch (e) {
      error(String(e.message ?? e));
      return 1;
    } finally {
      await rm(dir, { recursive: true, force: true }).catch(() => {});
    }
    log(`Role created: ${roleArn}`);
  }

  // 5. Register — collision check FIRST (FR-011: refuse, never overwrite).
  const connectionId = flags.name ?? `aws-${accountId}`;
  const listing = await api('GET', `/o/${slug}/integrations/aws/connections`);
  if (listing.ok) {
    const existing = (await listing.json())?.connections ?? [];
    if (existing.some((c) => c.connectionId === connectionId)) {
      error(
        `A connection named "${connectionId}" already exists on ${slug}. ` +
          `Re-run with --name <different-name>, or remove the existing connection first. Nothing was changed.`,
      );
      return 1;
    }
  }
  const config = {
    authMode: 'role',
    roleArn,
    externalId,
    ...(region ? { region } : {}),
    ...(flags.management
      ? {
          memberRoleName: flags.memberRoleName ?? DEFAULT_MEMBER_ROLE_NAME,
          memberAccounts: flags.members.map((m) => ({ accountId: m })),
        }
      : {}),
  };
  const created = await api('POST', `/o/${slug}/integrations/aws/connections`, {
    connectionId,
    label: `AWS ${accountId}`,
    config,
  });
  if (!created.ok) {
    let reason = '';
    try {
      reason = (await created.json())?.message ?? '';
    } catch {
      /* no body */
    }
    error(
      `The role exists in your account (${roleArn}) but registering it failed ` +
        `(HTTP ${created.status}${reason ? `: ${reason}` : ''}).\n` +
        `Resume with: landfall connect aws --terraform   (paste the same role ARN) — or register it on the integrations page.`,
    );
    return 1;
  }

  // 6. Health check — proves the assumption works and names the account (FR-010).
  const health = await api('POST', `/o/${slug}/integrations/aws/health-check`, { connectionId });
  const verdict = health.ok ? await health.json() : null;
  if (verdict?.ok) {
    log(`Connected ✓ ${verdict.detail ?? `AWS account ${accountId}`}`);
  } else {
    error(
      `The connection "${connectionId}" is registered but its health check did not pass` +
        `${verdict?.detail ? `: ${verdict.detail}` : ''}.\n` +
        `Common cause: the role was created moments ago and IAM is still propagating — ` +
        `re-check from the integrations page in a minute.`,
    );
    return 1;
  }

  // 7. Member follow-ups (FR-013): an undone member is a stated next step.
  if (flags.management) {
    log('');
    log(
      memberInstructions({
        managementRoleArn: roleArn,
        memberRoleName: flags.memberRoleName ?? DEFAULT_MEMBER_ROLE_NAME,
        members: flags.members,
      }),
    );
  }
  return 0;
}
