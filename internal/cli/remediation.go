// remediation.go — `landfall remediation approve` (feature
// 20260812-010632, T030 in the Node source; ported here for T055,
// contracts/incident-sim-and-override.md §2, research.md D6).
//
// HUMAN-TYPED ONLY, never an MCP tool. Constitution Principle V (this
// feature's plan.md Constitution Check, and the Node source's own header in
// src/remediation/commands.mjs): an MCP tool is, by construction, something
// an LLM agent can call on its own initiative mid-conversation. Exposing the
// corroboration-gate override there would let an agent talk itself past the
// exact gate that feature exists to add, collapsing "propose, human
// approves" back into "propose, agent can also just approve." A CLI
// subcommand is a human typing an explicit command with an explicit reason
// string — the same authenticated-human action as clicking "override and
// approve" in the browser, just through a different terminal.
//
// This file MUST NOT be imported by, or have any function reachable from,
// internal/tools or internal/mcp. Do not add such a code path.
//
// Uses the CLI's OWN authenticated session (internal/auth, `landfall
// login`), the same pattern `connect aws` (connect_aws.go) already
// establishes — NOT the edge-bridge bearer token (internal/client), which is
// scoped to agent contributions, not human approvals.
//
// Real endpoint shape (corrected in the Node source from an earlier draft's
// shorthand by reading the actual controller): `POST /o/:slug/incidents/
// :incidentId/remediation/proposals/:remediationId/approve` (not
// `/remediations/...`). `version` is typed non-optional in the controller's
// ApproveBody interface, but the SERVICE layer only checks it when truthy
// (`if (version && version !== state.version)`) — so an omitted version is
// safe at runtime and lets this command work without first fetching the
// proposal's exact current version string; `--version` is offered for the
// caller who already knows it.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/landfalls-ai/landfall-cli/internal/auth"
	"github.com/landfalls-ai/landfall-cli/internal/instance"
	"github.com/spf13/cobra"
)

// RemediationOverride carries an explicit --override "<reason>" — a distinct
// type (rather than a plain string) so "no --override was passed" (nil) and
// "an --override with an empty reason was passed" (non-nil, Reason == "")
// are two different, distinguishable states, matching the Node source's
// `flags.override` being either absent or `{reason}`.
type RemediationOverride struct {
	Reason string
}

// RemediationApproveFlags is the parsed
// `remediation approve <remediationId> --incident <id> [...]` flag set.
type RemediationApproveFlags struct {
	RemediationID string
	Incident      string
	Org           string
	Version       string
	Override      *RemediationOverride
}

// ParseRemediationApproveFlags parses flags from argv (already past the
// "remediation approve" words).
func ParseRemediationApproveFlags(argv []string) (RemediationApproveFlags, error) {
	args := append([]string(nil), argv...)

	// takeValue mirrors the Node source's own helper: find the FIRST
	// occurrence of name, remove it (and its value, if any) from args, and
	// return the value (empty when the flag was the last token with no
	// value following it — still "present", just valueless).
	takeValue := func(name string) (string, bool) {
		for i, a := range args {
			if a == name {
				value := ""
				removeCount := 1
				if i+1 < len(args) {
					value = args[i+1]
					removeCount = 2
				}
				args = append(args[:i], args[i+removeCount:]...)
				return value, true
			}
		}
		return "", false
	}

	var flags RemediationApproveFlags
	flags.Incident, _ = takeValue("--incident")
	flags.Org, _ = takeValue("--org")
	flags.Version, _ = takeValue("--version")
	if reason, ok := takeValue("--override"); ok {
		flags.Override = &RemediationOverride{Reason: reason}
	}

	var positionals []string
	for _, a := range args {
		if !strings.HasPrefix(a, "--") {
			positionals = append(positionals, a)
		}
	}
	if len(positionals) > 0 {
		flags.RemediationID = positionals[0]
	}

	if flags.RemediationID == "" {
		return RemediationApproveFlags{}, errors.New(
			`usage: landfall remediation approve <remediationId> --incident <incidentId> [--org <slug>] [--override "<reason>"]`)
	}
	if flags.Incident == "" {
		return RemediationApproveFlags{}, errors.New(
			"landfall remediation approve requires --incident <incidentId> — a remediation id alone does not identify which incident it belongs to")
	}
	return flags, nil
}

// RemediationApproveDeps are RunRemediationApprove's injectable
// dependencies. A nil field takes the real-world default; tests override
// everything so nothing in a test makes a real HTTP call.
type RemediationApproveDeps struct {
	Doer       httpDoer
	GetToken   func(ctx context.Context) string
	GetOrgSlug func() string
	BaseURL    string
	Env        func(string) string
}

// remediationApproveErrorBody is the shape the server's refusal responses
// carry — only some of these fields are populated for any given refusal.
type remediationApproveErrorBody struct {
	Error     string `json:"error"`
	Message   string `json:"message"`
	Shortfall string `json:"shortfall"`
}

// RunRemediationApprove is the command body. Returns the process exit code:
// 0 on the server's res.ok, 1 on refusal (missing session, session/org
// mismatch, or any server-side refusal). Missing subcommand/flags are the
// caller's responsibility (exit 2), same as bin/landfall.mjs's dispatch.
func RunRemediationApprove(ctx context.Context, flags RemediationApproveFlags, ui *UI, deps RemediationApproveDeps) int {
	if ui == nil {
		ui = New()
	}
	doer := deps.Doer
	if doer == nil {
		doer = http.DefaultClient
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
	baseURL := deps.BaseURL
	if baseURL == "" {
		baseURL = envFn("LANDFALL_BASE_URL")
	}
	if baseURL == "" {
		baseURL = instance.DefaultInstance().API
	}
	baseURL = strings.TrimRight(baseURL, "/")

	// All output — progress and refusal alike — goes through ui.Log (stderr,
	// "[landfall] "-prefixed). This matches the Node source's own dispatch,
	// which wires BOTH log and error to the same prefixed-stderr helper for
	// this command (bin/landfall.mjs's `remediation` branch: `{ log, error:
	// log }`, where `log` is the top-level
	// `process.stderr.write('[landfall] ' + ...)` helper) — unlike
	// `connect aws`, whose dispatch leaves `log` at its console.log/stdout
	// default. See this task's final report for the citation.
	say := ui.Log

	// 1. Session, before anything touches the network — same order
	// connect aws already establishes (FR-005 there; the same principle
	// applies here).
	token := getToken(ctx)
	if token == "" {
		say("Not signed in. Run: landfall login   — then re-run: landfall remediation approve …")
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

	body := map[string]any{}
	if flags.Version != "" {
		body["version"] = flags.Version
	}
	if flags.Override != nil {
		body["override"] = map[string]string{"reason": flags.Override.Reason}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		say("could not encode the approve request: %v", err)
		return 1
	}

	url := fmt.Sprintf("%s/o/%s/incidents/%s/remediation/proposals/%s/approve", baseURL, slug, flags.Incident, flags.RemediationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		say("could not build the approve request: %v", err)
		return 1
	}
	req.Header.Set("authorization", "Bearer "+token)
	req.Header.Set("content-type", "application/json")

	res, err := doer.Do(req)
	if err != nil {
		say("the approve request failed: %v", err)
		return 1
	}
	defer func() { _ = res.Body.Close() }()
	data, _ := io.ReadAll(res.Body)

	if res.StatusCode >= 200 && res.StatusCode < 300 { // the real endpoint answers 202 on success
		if flags.Override != nil {
			say("✓ Approved (override recorded: reason=\"%s\")", flags.Override.Reason)
		} else {
			say("✓ Approved.")
		}
		return 0
	}

	var payload remediationApproveErrorBody
	_ = json.Unmarshal(data, &payload) // no/unparseable body leaves payload zero-valued

	if res.StatusCode == http.StatusForbidden && payload.Error == "override_requires_human_session" {
		say("Refused: the override can only be invoked from a human-authenticated session, never an API key. " +
			"Sign in with `landfall login` and re-run without a scripted credential.")
		return 1
	}
	if res.StatusCode == http.StatusBadRequest && payload.Error == "remediation_not_admitted" {
		shortfall := payload.Shortfall
		if shortfall == "" {
			shortfall = "insufficient corroboration"
		}
		hint := `Corroborate the claim first, or re-run with --override "<reason>" if you are genuinely the only one who can act right now.`
		if flags.Override != nil {
			hint = "An override was supplied but was not accepted — see the message above."
		}
		say("Refused: this proposal's hypothesis is not yet admitted — %s.\n%s", shortfall, hint)
		return 1
	}
	if res.StatusCode == http.StatusBadRequest && payload.Error == "override_reason_required" {
		say(`Refused: --override was passed with no reason. Try: --override "why you are overriding this"`)
		return 1
	}

	suffix := ""
	if payload.Error != "" {
		suffix += ": " + payload.Error
	}
	if payload.Message != "" {
		suffix += " — " + payload.Message
	}
	say("Refused (HTTP %d)%s", res.StatusCode, suffix)
	return 1
}

// newRemediationCommand wires `landfall remediation approve <id> [flags]`
// into the command tree. Per this file's own header: human-typed only, never
// reachable from internal/tools or internal/mcp — this constructor is called
// exactly once, from root.go's AddCommand, alongside every other real
// top-level command.
func newRemediationCommand(ui *UI) *cobra.Command {
	remediation := newCommand(ui, "remediation", func(*cobra.Command, []string) error {
		ui.Log(`usage: landfall remediation approve <remediationId> --incident <incidentId> [--org <slug>] [--override "<reason>"]`)
		return usage()
	})
	remediation.DisableFlagParsing = true

	approve := newCommand(ui, "approve", func(cmd *cobra.Command, args []string) error {
		flags, err := ParseRemediationApproveFlags(args)
		if err != nil {
			ui.Log("%s", err.Error())
			return usage()
		}
		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		if code := RunRemediationApprove(ctx, flags, ui, RemediationApproveDeps{}); code != 0 {
			return &exitError{code: code}
		}
		return nil
	})
	approve.DisableFlagParsing = true
	remediation.AddCommand(approve)
	return remediation
}
