// connect_aws.go — `landfall connect aws` (feature 081,
// landfalls-ai/landfall#1168, ported here for T054): one command from the
// CLI's pinned session to a healthy role-based AWS connection. Zero
// copy-paste, zero AWS credentials held by Landfall, zero new dependencies
// (stdlib only).
//
// Faithful Go port of src/connect/aws.mjs + src/connect/commands.mjs. Every
// step either completed or didn't, and every failure says what completed,
// what didn't, and the one action that resumes (FR-011/FR-014).
//
// TRUST BOUNDARY, stated plainly (ported verbatim from aws.mjs's header):
// this file hands bytes to a subprocess that creates IAM resources in the
// CUSTOMER'S account, under the CUSTOMER'S own credentials. Landfall never
// reads, stores, or transmits an AWS credential (FR-008), and the bytes it
// hands over are accepted ONLY from the pinned release tag, verified against
// the sha256 recorded in DefaultTemplatePin (FR-009). A checksum mismatch
// runs NOTHING.
//
// Every subprocess invocation goes through exec.CommandContext with an argv
// SLICE — never a shell string — matching the Node source's
// `spawn(cmd, argv, {shell:false})` discipline throughout.
package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/landfalls-ai/landfall-cli/internal/auth"
	"github.com/landfalls-ai/landfall-cli/internal/instance"
)

// TemplatePin identifies the exact pinned CloudFormation role template this
// CLI build will hand to the customer's AWS tooling. Changing any field is a
// reviewed CLI release, never a runtime decision.
type TemplatePin struct {
	Repo   string
	Tag    string
	Path   string
	SHA256 string
}

// DefaultTemplatePin is the one template this CLI build trusts — repo, tag,
// path, and sha256 copied verbatim from src/connect/aws.mjs's TEMPLATE_PIN.
var DefaultTemplatePin = TemplatePin{
	Repo:   "landfalls-ai/landfall-aws-onboarding",
	Tag:    "v0.1.0",
	Path:   "cloudformation/landfall-readonly-role.yaml",
	SHA256: "c075540b168619f61416d6a5474d222474b38e5040158c2625fc5afcba784ae7",
}

// DefaultStackName is the CloudFormation stack name `connect aws` deploys.
const DefaultStackName = "landfall-onboarding"

// DefaultMemberRoleName is the role name member-account instructions use
// when --member-role-name is not given.
const DefaultMemberRoleName = "landfall-readonly"

var (
	roleArnPattern        = regexp.MustCompile(`^arn:aws[a-z-]*:iam::\d{12}:role/.+$`)
	accountIDPattern      = regexp.MustCompile(`^\d{12}$`)
	accountFromArnPattern = regexp.MustCompile(`::(\d{12}):`)
)

func templateURL(pin TemplatePin) string {
	return fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s", pin.Repo, pin.Tag, pin.Path)
}

func validateRoleArn(value string) bool {
	return roleArnPattern.MatchString(strings.TrimSpace(value))
}

func validateAccountID(value string) bool {
	return accountIDPattern.MatchString(strings.TrimSpace(value))
}

func accountIDFromArn(arn string) string {
	m := accountFromArnPattern.FindStringSubmatch(arn)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// httpDoer is the injectable HTTP transport. *http.Client satisfies it.
type httpDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// fetchTemplate fetches the pinned template and verifies its checksum.
// Returns an error with ACTIONABLE text on any failure — and the caller runs
// nothing after an error (SC-004: a tampered or unfetchable template ⇒ zero
// AWS commands).
func fetchTemplate(ctx context.Context, doer httpDoer, pin TemplatePin, docsBase string) (string, error) {
	url := templateURL(pin)
	fetchFailed := func(cause error) error {
		return fmt.Errorf(
			"could not fetch the pinned role template (%s): %v\n"+
				"Check your network, or use the manual path: %s/integrations/aws", url, cause, docsBase)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fetchFailed(err)
	}
	res, err := doer.Do(req)
	if err != nil {
		return "", fetchFailed(err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf(
			"the pinned role template answered HTTP %d (%s).\n"+
				"Use the manual path instead: %s/integrations/aws", res.StatusCode, url, docsBase)
	}
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return "", fetchFailed(err)
	}
	digest := sha256.Sum256(data)
	digestHex := hex.EncodeToString(digest[:])
	if digestHex != pin.SHA256 {
		return "", fmt.Errorf(
			"the fetched role template does not match the checksum this CLI release pinned "+
				"(expected %s, got %s). Refusing to run it. "+
				"Update the CLI (a newer release may pin a newer template), or use the manual path: "+
				"%s/integrations/aws", pin.SHA256, digestHex, docsBase)
	}
	return string(data), nil
}

// runResult is one completed subprocess invocation's captured output.
type runResult struct {
	Code   int
	Stdout string
	Stderr string
}

// runFunc executes one external command with an argv SLICE (never a shell
// string) and captures its output. A nil result means the executable could
// not be started at all (not found, permission denied, …) — matching the
// Node source's `child.on('error') → resolve(null)`, which is how ENOENT
// (aws CLI not installed) is distinguished from a real non-zero exit.
type runFunc func(ctx context.Context, cmd string, args []string, stdin string) *runResult

// defaultRunFunc is the real subprocess runner: argv array only, never a
// shell string (exec.CommandContext already never invokes a shell).
func defaultRunFunc(ctx context.Context, cmd string, args []string, stdin string) *runResult {
	c := exec.CommandContext(ctx, cmd, args...)
	var stdoutBuf, stderrBuf bytes.Buffer
	c.Stdout = &stdoutBuf
	c.Stderr = &stderrBuf
	if stdin != "" {
		c.Stdin = strings.NewReader(stdin)
	}
	err := c.Run()
	if err == nil {
		return &runResult{Code: 0, Stdout: stdoutBuf.String(), Stderr: stderrBuf.String()}
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return &runResult{Code: exitErr.ExitCode(), Stdout: stdoutBuf.String(), Stderr: stderrBuf.String()}
	}
	// Not found (ENOENT), permission denied, etc. Node's defaultRun collapses
	// every spawn-time error to null, not just ENOENT — do the same.
	return nil
}

// awsPreflight preflights the CUSTOMER's own AWS tooling: the binary exists,
// and their credentials resolve — surfacing WHICH account the role would be
// created in before anything is mutated.
func awsPreflight(ctx context.Context, run runFunc) (accountID, arn string, err error) {
	version := run(ctx, "aws", []string{"--version"}, "")
	if version == nil {
		return "", "", fmt.Errorf(
			`the "aws" CLI is not installed (or not on PATH). Role creation runs under YOUR own AWS ` +
				`tooling — install it (https://aws.amazon.com/cli/) and re-run, or use --terraform.`)
	}

	identity := run(ctx, "aws", []string{"sts", "get-caller-identity", "--output", "json"}, "")
	if identity == nil || identity.Code != 0 {
		stderr := ""
		if identity != nil {
			stderr = strings.TrimSpace(identity.Stderr)
		}
		return "", "", fmt.Errorf(
			"your AWS credentials did not resolve (aws sts get-caller-identity failed).\n%s\n"+
				"Configure credentials for the account you want to connect (aws configure / SSO / env) and re-run.",
			stderr)
	}

	var parsed struct {
		Account string `json:"Account"`
		Arn     string `json:"Arn"`
	}
	if jsonErr := json.Unmarshal([]byte(identity.Stdout), &parsed); jsonErr != nil {
		return "", "", errors.New("could not parse the aws CLI identity output — is your aws CLI unusually old?")
	}
	return parsed.Account, parsed.Arn, nil
}

// deployParams is deployRoleStack's argument bundle.
type deployParams struct {
	TemplateFile string
	StackName    string
	PrincipalArn string
	ExternalID   string
	Region       string
	RoleName     string
}

// deployRoleStack deploys (create-or-update — CloudFormation's own
// idempotency) the pinned role stack.
func deployRoleStack(ctx context.Context, run runFunc, p deployParams) error {
	args := []string{
		"cloudformation", "deploy",
		"--template-file", p.TemplateFile,
		"--stack-name", p.StackName,
		"--capabilities", "CAPABILITY_NAMED_IAM",
		"--no-fail-on-empty-changeset", // a re-run with nothing to change is success, not an error
		"--parameter-overrides",
		"LandfallPrincipalArn=" + p.PrincipalArn,
		"ExternalId=" + p.ExternalID,
	}
	if p.RoleName != "" {
		args = append(args, "RoleName="+p.RoleName)
	}
	if p.Region != "" {
		args = append(args, "--region", p.Region)
	}
	result := run(ctx, "aws", args, "")
	if result == nil || result.Code != 0 {
		stderr := ""
		if result != nil {
			stderr = strings.TrimSpace(result.Stderr)
		}
		return fmt.Errorf(
			"creating the role stack failed (aws cloudformation deploy).\n%s\n"+
				"Your AWS credentials must be allowed to create an IAM role. The stack (if partially "+
				"created) lives in YOUR account as \"%s\" — fix the cause and re-run; deploy "+
				"updates in place.", stderr, p.StackName)
	}
	return nil
}

// stackParams is readRoleArn's argument bundle.
type stackParams struct {
	StackName string
	Region    string
}

// readRoleArn reads the created role's ARN back off the stack outputs.
func readRoleArn(ctx context.Context, run runFunc, p stackParams) (string, error) {
	args := []string{
		"cloudformation", "describe-stacks",
		"--stack-name", p.StackName,
		"--query", "Stacks[0].Outputs[?OutputKey=='RoleArn'].OutputValue",
		"--output", "text",
	}
	if p.Region != "" {
		args = append(args, "--region", p.Region)
	}
	result := run(ctx, "aws", args, "")
	arn := ""
	if result != nil {
		arn = strings.TrimSpace(result.Stdout)
	}
	if result == nil || result.Code != 0 || !validateRoleArn(arn) {
		stderr := ""
		if result != nil {
			stderr = strings.TrimSpace(result.Stderr)
		}
		return "", fmt.Errorf(
			"the stack deployed but its RoleArn output could not be read.\n%s\n"+
				"Resume with: aws cloudformation describe-stacks --stack-name %s — then "+
				"register the role on the integrations page, or re-run this command.", stderr, p.StackName)
	}
	return arn, nil
}

// terraformSnippet is the pinned Terraform-module snippet for --terraform
// (FR-012).
func terraformSnippet(pin TemplatePin, principalArn, externalID string) string {
	return strings.Join([]string{
		`module "landfall_onboarding" {`,
		fmt.Sprintf(`  source                 = "github.com/%s//terraform?ref=%s"`, pin.Repo, pin.Tag),
		fmt.Sprintf(`  landfall_principal_arn = "%s"`, principalArn),
		fmt.Sprintf(`  external_id            = "%s"`, externalID),
		`}`,
		``,
		`output "landfall_role_arn" { value = module.landfall_onboarding.role_arn }`,
	}, "\n")
}

// memberInstructions are the per-member instructions for a management
// connection (FR-013).
//
// Deliberately NOT the pinned customer template: that template REQUIRES an
// external-id trust condition, and Landfall's management→member chained
// assumption presents no external id (the confused-deputy protection lives on
// the CUSTOMER↔LANDFALL hop, not on a hop between two roles the customer
// owns). A member role is therefore a plain two-command creation: trust = the
// management role, policy = ReadOnlyAccess.
func memberInstructions(managementRoleArn, memberRoleName string, members []string) string {
	trust, _ := json.Marshal(map[string]any{
		"Version": "2012-10-17",
		"Statement": []map[string]any{
			{
				"Effect":    "Allow",
				"Principal": map[string]string{"AWS": managementRoleArn},
				"Action":    "sts:AssumeRole",
			},
		},
	})

	lines := []string{
		`For EACH member account below, with credentials FOR THAT ACCOUNT, create the member role`,
		`(trusts your management role; read-only):`,
		``,
	}
	for _, accountID := range members {
		lines = append(lines,
			fmt.Sprintf(`  # account %s:`, accountID),
			fmt.Sprintf(`  aws iam create-role --role-name %s \`, memberRoleName),
			fmt.Sprintf(`    --assume-role-policy-document '%s'`, string(trust)),
			fmt.Sprintf(`  aws iam attach-role-policy --role-name %s \`, memberRoleName),
			`    --policy-arn arn:aws:iam::aws:policy/ReadOnlyAccess`,
			``,
		)
	}
	lines = append(lines,
		`Until a member's role exists, reads addressed to that account fail with the member named —`,
		`an undone member is a stated next step, not a silent failure.`,
	)
	return strings.Join(lines, "\n")
}

// ConnectAWSFlags is the parsed `connect aws` flag set.
type ConnectAWSFlags struct {
	Terraform      bool
	Management     bool
	Org            string
	Name           string
	Region         string
	Members        []string
	MemberRoleName string
}

// ParseConnectAWSFlags parses `connect aws` flags from argv (already past the
// "connect aws" words).
func ParseConnectAWSFlags(argv []string) (ConnectAWSFlags, error) {
	var flags ConnectAWSFlags
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		switch a {
		case "--terraform":
			flags.Terraform = true
		case "--management":
			flags.Management = true
		case "--org":
			i++
			if i < len(argv) {
				flags.Org = argv[i]
			}
		case "--name":
			i++
			if i < len(argv) {
				flags.Name = argv[i]
			}
		case "--region":
			i++
			if i < len(argv) {
				flags.Region = argv[i]
			}
		case "--member":
			i++
			value := ""
			if i < len(argv) {
				value = argv[i]
			}
			for _, m := range strings.Split(value, ",") {
				if m != "" {
					flags.Members = append(flags.Members, m)
				}
			}
		case "--member-role-name":
			i++
			if i < len(argv) {
				flags.MemberRoleName = argv[i]
			}
		default:
			return ConnectAWSFlags{}, fmt.Errorf("unknown flag for connect aws: %s", a)
		}
	}
	if len(flags.Members) > 0 && !flags.Management {
		return ConnectAWSFlags{}, errors.New("--member requires --management")
	}
	if flags.Management && len(flags.Members) == 0 {
		return ConnectAWSFlags{}, errors.New("--management needs at least one --member <accountId>")
	}
	for _, m := range flags.Members {
		if !validateAccountID(m) {
			return ConnectAWSFlags{}, fmt.Errorf("--member accounts are 12-digit AWS account ids (got %q)", m)
		}
	}
	return flags, nil
}

// onboardingInfo is the platform's /integrations/aws/onboarding-info reply.
type onboardingInfo struct {
	Available       bool   `json:"available"`
	Reason          string `json:"reason"`
	PrincipalArn    string `json:"principalArn"`
	SuggestedRegion string `json:"suggestedRegion"`
}

// ConnectAWSDeps are RunConnectAWS's injectable dependencies. A nil field
// takes the real-world default; tests override some or all so nothing in a
// test executes an AWS command or touches the network.
type ConnectAWSDeps struct {
	Doer       httpDoer
	Run        runFunc
	Prompt     func(ctx context.Context, question string) (string, error)
	GetToken   func(ctx context.Context) string
	GetOrgSlug func() string
	BaseURL    string
	Env        func(string) string
	Pin        TemplatePin
}

// RunConnectAWS runs the whole 7-step `connect aws` flow and returns the
// process exit code (0 success, 1 any failure branch — flag/usage errors are
// the caller's responsibility, exit 2, same as bin/landfall.mjs's dispatch).
func RunConnectAWS(ctx context.Context, flags ConnectAWSFlags, ui *UI, deps ConnectAWSDeps) int {
	if ui == nil {
		ui = New()
	}
	doer := deps.Doer
	if doer == nil {
		doer = http.DefaultClient
	}
	run := deps.Run
	if run == nil {
		run = defaultRunFunc
	}
	getToken := deps.GetToken
	if getToken == nil {
		getToken = func(ctx context.Context) string { return auth.GetCachedAccessToken(ctx, nil) }
	}
	getSlug := deps.GetOrgSlug
	if getSlug == nil {
		getSlug = auth.GetCachedOrgSlug
	}
	envFn := deps.Env
	if envFn == nil {
		envFn = os.Getenv
	}
	pin := deps.Pin
	if pin == (TemplatePin{}) {
		pin = DefaultTemplatePin
	}
	baseURL := deps.BaseURL
	if baseURL == "" {
		baseURL = envFn("LANDFALL_BASE_URL")
	}
	if baseURL == "" {
		baseURL = instance.DefaultInstance().API
	}
	baseURL = strings.TrimRight(baseURL, "/")
	docsBase := instance.DefaultInstance().Docs

	// Every human-facing line — progress and refusal alike — goes through
	// ui.Log (stderr, "[landfall] "-prefixed, root.go's Go equivalent of
	// bin/landfall.mjs's own log() helper): this command is not in
	// contracts/cli-commands.md's verified stdout inventory, so its output
	// follows the CLI-wide human-readable/stderr convention, rather than the
	// Node reference's un-prefixed console.log default for THIS specific
	// command (which the contract review treats as an oversight in
	// bin/landfall.mjs's `connect` dispatch branch not overriding `log` the
	// way its `remediation` branch does — see this task's final report for
	// the citation).
	say := ui.Log

	// 1. Session (FR-005) — before anything touches AWS or the network.
	token := getToken(ctx)
	if token == "" {
		say("Not signed in. Run: landfall login   — then re-run: landfall connect aws")
		return 1
	}
	slug := getSlug()
	if slug == "" {
		say("This session has no organization pin. Run: landfall login   (and pick your organization)")
		return 1
	}
	if flags.Org != "" && flags.Org != slug {
		say("This session is signed in to \"%s\", not \"%s\".\n"+
			"Run: landfall login   against %s first — a session is pinned to one organization.",
			slug, flags.Org, flags.Org)
		return 1
	}
	say("Connecting AWS to organization: %s", slug)

	apiCall := func(method, path string, body any) (int, []byte, error) {
		var reader io.Reader
		if body != nil {
			encoded, err := json.Marshal(body)
			if err != nil {
				return 0, nil, err
			}
			reader = bytes.NewReader(encoded)
		}
		req, err := http.NewRequestWithContext(ctx, method, baseURL+path, reader)
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set("authorization", "Bearer "+token)
		req.Header.Set("content-type", "application/json")
		res, err := doer.Do(req)
		if err != nil {
			return 0, nil, err
		}
		defer func() { _ = res.Body.Close() }()
		data, err := io.ReadAll(res.Body)
		if err != nil {
			return res.StatusCode, nil, err
		}
		return res.StatusCode, data, nil
	}

	// 2. The platform's principal (FR-015: a 404 is an OLD platform, not a bug).
	infoStatus, infoBody, err := apiCall(http.MethodGet, fmt.Sprintf("/o/%s/integrations/aws/onboarding-info", slug), nil)
	if err != nil {
		say("onboarding-info request failed: %v", err)
		return 1
	}
	if infoStatus == http.StatusNotFound {
		say("This Landfall deployment does not support automated AWS onboarding yet (no onboarding-info "+
			"endpoint). Use the manual path: %s/integrations/aws", docsBase)
		return 1
	}
	if infoStatus == http.StatusUnauthorized || infoStatus == http.StatusForbidden {
		say("Connecting integrations needs an organization administrator. Ask an admin to run this, or to grant you admin.")
		return 1
	}
	if infoStatus < 200 || infoStatus >= 300 {
		say("onboarding-info answered HTTP %d — try again, or use the manual path.", infoStatus)
		return 1
	}
	var info onboardingInfo
	if err := json.Unmarshal(infoBody, &info); err != nil {
		say("onboarding-info returned an unparseable response — try again, or use the manual path.")
		return 1
	}
	if !info.Available {
		say("Automated role onboarding is not available on this deployment: %s\n"+
			"Use keys-mode setup instead: %s/integrations/aws", info.Reason, docsBase)
		return 1
	}

	// 3. External id — local, strong, never echoed or logged (FR-007).
	externalIDBytes := make([]byte, 24)
	if _, err := rand.Read(externalIDBytes); err != nil {
		say("could not generate a secure external id: %v", err)
		return 1
	}
	externalID := base64.RawURLEncoding.EncodeToString(externalIDBytes)

	region := flags.Region
	if region == "" {
		region = info.SuggestedRegion
	}

	var roleArn, accountID string
	if flags.Terraform {
		// 4a. Terraform shops (FR-012): print the pinned snippet, take the ARN back.
		say("Apply this in your Terraform (pinned release), then paste the role ARN it outputs:\n")
		say("%s", terraformSnippet(pin, info.PrincipalArn, externalID))
		say("")
		if deps.Prompt == nil {
			say("no interactive prompt available for --terraform in this environment")
			return 1
		}
		for {
			pasted, err := deps.Prompt(ctx, "role ARN: ")
			if err != nil {
				say("could not read the role ARN: %v", err)
				return 1
			}
			pasted = strings.TrimSpace(pasted)
			if validateRoleArn(pasted) {
				roleArn = pasted
				break
			}
			say("that is not an IAM role ARN (expected arn:aws:iam::<12 digits>:role/<name>) — try again")
		}
		accountID = accountIDFromArn(roleArn)
	} else {
		// 4b. Default path: the CUSTOMER's own aws CLI creates the role (FR-008).
		accountID, _, err = awsPreflight(ctx, run)
		if err != nil {
			say("%s", err.Error())
			return 1
		}
		if region != "" {
			say("Creating the read-only role in AWS account %s (region %s) …", accountID, region)
		} else {
			say("Creating the read-only role in AWS account %s (your aws CLI default region) …", accountID)
		}

		// Template: pinned + checksum-verified BEFORE any use (FR-009/SC-004).
		template, err := fetchTemplate(ctx, doer, pin, docsBase)
		if err != nil {
			say("%s", err.Error())
			return 1
		}
		dir, err := os.MkdirTemp("", "landfall-connect-")
		if err != nil {
			say("could not create a working directory: %v", err)
			return 1
		}
		defer func() { _ = os.RemoveAll(dir) }()
		templateFile := filepath.Join(dir, "landfall-readonly-role.yaml")
		if err := os.WriteFile(templateFile, []byte(template), 0o600); err != nil {
			say("could not write the role template: %v", err)
			return 1
		}
		if err := deployRoleStack(ctx, run, deployParams{
			TemplateFile: templateFile,
			StackName:    DefaultStackName,
			PrincipalArn: info.PrincipalArn,
			ExternalID:   externalID,
			Region:       region,
		}); err != nil {
			say("%s", err.Error())
			return 1
		}
		roleArn, err = readRoleArn(ctx, run, stackParams{StackName: DefaultStackName, Region: region})
		if err != nil {
			say("%s", err.Error())
			return 1
		}
		say("Role created: %s", roleArn)
	}

	// 5. Register — collision check FIRST (FR-011: refuse, never overwrite).
	connectionID := flags.Name
	if connectionID == "" {
		connectionID = "aws-" + accountID
	}
	listStatus, listBody, listErr := apiCall(http.MethodGet, fmt.Sprintf("/o/%s/integrations/aws/connections", slug), nil)
	if listErr == nil && listStatus >= 200 && listStatus < 300 {
		var listing struct {
			Connections []struct {
				ConnectionID string `json:"connectionId"`
			} `json:"connections"`
		}
		if json.Unmarshal(listBody, &listing) == nil {
			for _, c := range listing.Connections {
				if c.ConnectionID == connectionID {
					say("A connection named \"%s\" already exists on %s. "+
						"Re-run with --name <different-name>, or remove the existing connection first. Nothing was changed.",
						connectionID, slug)
					return 1
				}
			}
		}
	}

	config := map[string]any{
		"authMode":   "role",
		"roleArn":    roleArn,
		"externalId": externalID,
	}
	if region != "" {
		config["region"] = region
	}
	memberRoleName := flags.MemberRoleName
	if memberRoleName == "" {
		memberRoleName = DefaultMemberRoleName
	}
	if flags.Management {
		config["memberRoleName"] = memberRoleName
		memberAccounts := make([]map[string]string, len(flags.Members))
		for i, m := range flags.Members {
			memberAccounts[i] = map[string]string{"accountId": m}
		}
		config["memberAccounts"] = memberAccounts
	}

	createStatus, createBody, createErr := apiCall(http.MethodPost, fmt.Sprintf("/o/%s/integrations/aws/connections", slug), map[string]any{
		"connectionId": connectionID,
		"label":        "AWS " + accountID,
		"config":       config,
	})
	if createErr != nil || createStatus < 200 || createStatus >= 300 {
		reason := ""
		if createErr == nil {
			var payload struct {
				Message string `json:"message"`
			}
			if json.Unmarshal(createBody, &payload) == nil {
				reason = payload.Message
			}
		}
		reasonSuffix := ""
		if reason != "" {
			reasonSuffix = ": " + reason
		}
		say("The role exists in your account (%s) but registering it failed "+
			"(HTTP %d%s).\nResume with: landfall connect aws --terraform   (paste the same role ARN) — or register it on the integrations page.",
			roleArn, createStatus, reasonSuffix)
		return 1
	}

	// 6. Health check — proves the assumption works and names the account (FR-010).
	healthStatus, healthBody, healthErr := apiCall(http.MethodPost, fmt.Sprintf("/o/%s/integrations/aws/health-check", slug), map[string]any{
		"connectionId": connectionID,
	})
	var verdict *struct {
		Ok     bool   `json:"ok"`
		Detail string `json:"detail"`
	}
	if healthErr == nil && healthStatus >= 200 && healthStatus < 300 {
		var v struct {
			Ok     bool   `json:"ok"`
			Detail string `json:"detail"`
		}
		if json.Unmarshal(healthBody, &v) == nil {
			verdict = &v
		}
	}
	if verdict != nil && verdict.Ok {
		detail := verdict.Detail
		if detail == "" {
			detail = fmt.Sprintf("AWS account %s", accountID)
		}
		say("Connected ✓ %s", detail)
	} else {
		detailSuffix := ""
		if verdict != nil && verdict.Detail != "" {
			detailSuffix = ": " + verdict.Detail
		}
		say("The connection \"%s\" is registered but its health check did not pass%s.\n"+
			"Common cause: the role was created moments ago and IAM is still propagating — "+
			"re-check from the integrations page in a minute.", connectionID, detailSuffix)
		return 1
	}

	// 7. Member follow-ups (FR-013): an undone member is a stated next step.
	if flags.Management {
		say("")
		say("%s", memberInstructions(roleArn, memberRoleName, flags.Members))
	}
	return 0
}
