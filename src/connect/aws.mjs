// connect/aws.mjs — the AWS half of `landfall connect aws` (feature 081,
// landfalls-ai/landfall#1168): template pinning + checksum, the customer-CLI
// invocations, validators, and the printable artifacts (terraform snippet,
// member instructions).
//
// TRUST BOUNDARY, stated plainly: this module hands bytes to a shell command
// that creates IAM resources in the CUSTOMER'S account, under the CUSTOMER'S
// own credentials. Landfall never reads, stores, or transmits an AWS
// credential (the spec's FR-008), and the bytes it hands over are accepted
// ONLY from the pinned release tag, verified against the sha256 recorded here
// at CLI-release time (FR-009 — the same externally-consumed-surface
// discipline as the feature-056 bootstrap URL). A mismatch runs NOTHING.
//
// Every process invocation is `spawn` with an argv ARRAY and `shell: false` —
// no value this module handles is ever interpolated into a shell string.
import { createHash } from 'node:crypto';
import { spawn } from 'node:child_process';

/** The one template this CLI build will hand to the customer's AWS tooling.
 * Changing any field is a reviewed CLI release, never a runtime decision. */
export const TEMPLATE_PIN = Object.freeze({
  repo: 'landfalls-ai/landfall-aws-onboarding',
  tag: 'v0.1.0',
  path: 'cloudformation/landfall-readonly-role.yaml',
  sha256: 'c075540b168619f61416d6a5474d222474b38e5040158c2625fc5afcba784ae7',
});

export const DEFAULT_STACK_NAME = 'landfall-onboarding';
export const DEFAULT_MEMBER_ROLE_NAME = 'landfall-readonly';

export const templateUrl = (pin = TEMPLATE_PIN) =>
  `https://raw.githubusercontent.com/${pin.repo}/${pin.tag}/${pin.path}`;

export function validateRoleArn(value) {
  return /^arn:aws[a-z-]*:iam::\d{12}:role\/.+$/.test(String(value ?? '').trim());
}

export function validateAccountId(value) {
  return /^\d{12}$/.test(String(value ?? '').trim());
}

/**
 * Fetch the pinned template and verify its checksum. Throws with an
 * ACTIONABLE message on any failure — and the caller runs nothing after a
 * throw (SC-004: a tampered or unfetchable template ⇒ zero AWS commands).
 */
export async function fetchTemplate(fetchImpl = globalThis.fetch, pin = TEMPLATE_PIN) {
  let response;
  try {
    response = await fetchImpl(templateUrl(pin));
  } catch (e) {
    throw new Error(
      `could not fetch the pinned role template (${templateUrl(pin)}): ${e?.message ?? e}\n` +
        `Check your network, or use the manual path: https://docs.landfalls.ai/integrations/aws`,
    );
  }
  if (!response.ok) {
    throw new Error(
      `the pinned role template answered HTTP ${response.status} (${templateUrl(pin)}).\n` +
        `Use the manual path instead: https://docs.landfalls.ai/integrations/aws`,
    );
  }
  const text = await response.text();
  const digest = createHash('sha256').update(text, 'utf8').digest('hex');
  if (digest !== pin.sha256) {
    throw new Error(
      `the fetched role template does not match the checksum this CLI release pinned ` +
        `(expected ${pin.sha256}, got ${digest}). Refusing to run it. ` +
        `Update the CLI (a newer release may pin a newer template), or use the manual path: ` +
        `https://docs.landfalls.ai/integrations/aws`,
    );
  }
  return text;
}

/**
 * Run one command (argv array, shell:false), capturing output. Injectable so
 * tests never execute anything. ENOENT is translated to `null` so callers can
 * produce the "aws CLI is not installed" message instead of a stack trace.
 */
export function defaultRun(cmd, args, { input } = {}) {
  return new Promise((resolve) => {
    let child;
    try {
      child = spawn(cmd, args, { shell: false, stdio: ['pipe', 'pipe', 'pipe'] });
    } catch {
      resolve(null);
      return;
    }
    let stdout = '';
    let stderr = '';
    child.stdout.on('data', (d) => (stdout += d));
    child.stderr.on('data', (d) => (stderr += d));
    child.on('error', () => resolve(null)); // ENOENT lands here
    child.on('close', (code) => resolve({ code: code ?? 1, stdout, stderr }));
    if (input) child.stdin.write(input);
    child.stdin.end();
  });
}

/**
 * Preflight the CUSTOMER's own AWS tooling: the binary exists, and their
 * credentials resolve — surfacing WHICH account the role would be created in
 * before anything is mutated. Returns `{accountId, arn}` or throws the
 * FR-014 actionable prose.
 */
export async function awsPreflight(run = defaultRun) {
  const version = await run('aws', ['--version']);
  if (version === null) {
    throw new Error(
      `the "aws" CLI is not installed (or not on PATH). Role creation runs under YOUR own AWS ` +
        `tooling — install it (https://aws.amazon.com/cli/) and re-run, or use --terraform.`,
    );
  }
  const identity = await run('aws', ['sts', 'get-caller-identity', '--output', 'json']);
  if (identity === null || identity.code !== 0) {
    throw new Error(
      `your AWS credentials did not resolve (aws sts get-caller-identity failed).\n` +
        `${(identity?.stderr ?? '').trim()}\n` +
        `Configure credentials for the account you want to connect (aws configure / SSO / env) and re-run.`,
    );
  }
  let parsed;
  try {
    parsed = JSON.parse(identity.stdout);
  } catch {
    throw new Error('could not parse the aws CLI identity output — is your aws CLI unusually old?');
  }
  return { accountId: String(parsed.Account ?? ''), arn: String(parsed.Arn ?? '') };
}

/** Deploy (create-or-update — CloudFormation's own idempotency) the pinned role stack. */
export async function deployRoleStack(
  run,
  { templateFile, stackName, principalArn, externalId, region, roleName },
) {
  const args = [
    'cloudformation',
    'deploy',
    '--template-file',
    templateFile,
    '--stack-name',
    stackName,
    '--capabilities',
    'CAPABILITY_NAMED_IAM',
    '--no-fail-on-empty-changeset', // a re-run with nothing to change is success, not an error
    '--parameter-overrides',
    `LandfallPrincipalArn=${principalArn}`,
    `ExternalId=${externalId}`,
    ...(roleName ? [`RoleName=${roleName}`] : []),
    ...(region ? ['--region', region] : []),
  ];
  const result = await run('aws', args);
  if (result === null || result.code !== 0) {
    throw new Error(
      `creating the role stack failed (aws cloudformation deploy).\n` +
        `${(result?.stderr ?? '').trim()}\n` +
        `Your AWS credentials must be allowed to create an IAM role. The stack (if partially ` +
        `created) lives in YOUR account as "${stackName}" — fix the cause and re-run; deploy ` +
        `updates in place.`,
    );
  }
  return result;
}

/** Read the created role's ARN back off the stack outputs. */
export async function readRoleArn(run, { stackName, region }) {
  const result = await run('aws', [
    'cloudformation',
    'describe-stacks',
    '--stack-name',
    stackName,
    '--query',
    "Stacks[0].Outputs[?OutputKey=='RoleArn'].OutputValue",
    '--output',
    'text',
    ...(region ? ['--region', region] : []),
  ]);
  const arn = result?.stdout?.trim();
  if (result === null || result.code !== 0 || !validateRoleArn(arn)) {
    throw new Error(
      `the stack deployed but its RoleArn output could not be read.\n` +
        `${(result?.stderr ?? '').trim()}\n` +
        `Resume with: aws cloudformation describe-stacks --stack-name ${stackName} — then ` +
        `register the role on the integrations page, or re-run this command.`,
    );
  }
  return arn;
}

/** The pinned Terraform-module snippet for `--terraform` (FR-012). */
export function terraformSnippet({ principalArn, externalId, pin = TEMPLATE_PIN }) {
  return [
    `module "landfall_onboarding" {`,
    `  source                 = "github.com/${pin.repo}//terraform?ref=${pin.tag}"`,
    `  landfall_principal_arn = "${principalArn}"`,
    `  external_id            = "${externalId}"`,
    `}`,
    ``,
    `output "landfall_role_arn" { value = module.landfall_onboarding.role_arn }`,
  ].join('\n');
}

/**
 * Per-member instructions for a management connection (FR-013).
 *
 * Deliberately NOT the pinned customer template: that template REQUIRES an
 * external-id trust condition, and Landfall's management→member chained
 * assumption presents no external id (the platform's 079 adapter, by design —
 * the confused-deputy protection lives on the CUSTOMER↔LANDFALL hop, not on a
 * hop between two roles the customer owns). A member role is therefore a plain
 * two-command creation: trust = the management role, policy = ReadOnlyAccess.
 */
export function memberInstructions({ managementRoleArn, memberRoleName, members }) {
  const trust = JSON.stringify({
    Version: '2012-10-17',
    Statement: [
      {
        Effect: 'Allow',
        Principal: { AWS: managementRoleArn },
        Action: 'sts:AssumeRole',
      },
    ],
  });
  const lines = [
    `For EACH member account below, with credentials FOR THAT ACCOUNT, create the member role`,
    `(trusts your management role; read-only):`,
    ``,
  ];
  for (const accountId of members) {
    lines.push(
      `  # account ${accountId}:`,
      `  aws iam create-role --role-name ${memberRoleName} \\`,
      `    --assume-role-policy-document '${trust}'`,
      `  aws iam attach-role-policy --role-name ${memberRoleName} \\`,
      `    --policy-arn arn:aws:iam::aws:policy/ReadOnlyAccess`,
      ``,
    );
  }
  lines.push(
    `Until a member's role exists, reads addressed to that account fail with the member named —`,
    `an undone member is a stated next step, not a silent failure.`,
  );
  return lines.join('\n');
}
