package cli

// connect_aws_test.go ports test/connect.test.mjs's 18 cases: the SC-007
// failure-mode matrix + the happy path for `landfall connect aws`, with the
// customer-tooling boundary FAKED (injected run/doer/prompt) so nothing here
// executes an AWS command or touches the network.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

const (
	testPrincipal = "arn:aws:iam::692539599137:role/landfall-dev-app-instance"
	testRoleArn   = "arn:aws:iam::111122223333:role/landfall-readonly"
	testTemplate  = "AWSTemplateFormatVersion: 2010-09-09\n# fake template for tests\n"
)

func testPin() TemplatePin {
	sum := sha256.Sum256([]byte(testTemplate))
	pin := DefaultTemplatePin
	pin.SHA256 = hex.EncodeToString(sum[:])
	return pin
}

// fakeDoer adapts a plain func to httpDoer.
type fakeDoer func(req *http.Request) (*http.Response, error)

func (f fakeDoer) Do(req *http.Request) (*http.Response, error) { return f(req) }

func jsonResponse(status int, body any) *http.Response {
	data, _ := json.Marshal(body)
	return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(data)), Header: make(http.Header)}
}

func rawResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

type apiCallRecord struct {
	URL    string
	Method string
	Body   []byte
}

// connectHarness is the capturing test harness around RunConnectAWS's
// injectable deps — the Go analogue of connect.test.mjs's harness().
type connectHarness struct {
	errBuf bytes.Buffer

	infoStatus, listStatus, createStatus, healthStatus int
	infoBody, listBody, createBody, healthBody         map[string]any

	runs     [][]string
	runFn    runFunc
	apiCalls []apiCallRecord
	prompt   func(ctx context.Context, q string) (string, error)
	getToken func(ctx context.Context) string
	getSlug  func() string
	pin      TemplatePin
}

func newConnectHarness() *connectHarness {
	h := &connectHarness{
		infoStatus: 200,
		infoBody: map[string]any{
			"available": true, "principalArn": testPrincipal,
			"accountId": "692539599137", "suggestedRegion": "us-east-1",
		},
		listStatus:   200,
		listBody:     map[string]any{"connections": []any{}},
		createStatus: 201,
		createBody:   map[string]any{"connectionId": "aws-111122223333"},
		healthStatus: 200,
		healthBody: map[string]any{
			"ok": true, "status": "connected",
			"detail": "Authenticated to AWS account 111122223333",
		},
		pin:      testPin(),
		getToken: func(ctx context.Context) string { return "tok-123" },
		getSlug:  func() string { return "acme" },
	}
	h.runFn = h.defaultRun
	return h
}

func (h *connectHarness) defaultRun(_ context.Context, cmd string, args []string, _ string) *runResult {
	h.runs = append(h.runs, append([]string{cmd}, args...))
	joined := strings.Join(args, " ")
	switch {
	case len(args) > 0 && args[0] == "--version":
		return &runResult{Code: 0, Stdout: "aws-cli/2.17.0"}
	case strings.HasPrefix(joined, "sts get-caller-identity"):
		body, _ := json.Marshal(map[string]string{"Account": "111122223333", "Arn": "arn:aws:iam::111122223333:user/dev"})
		return &runResult{Code: 0, Stdout: string(body)}
	case len(args) > 1 && args[0] == "cloudformation" && args[1] == "deploy":
		return &runResult{Code: 0}
	case len(args) > 1 && args[0] == "cloudformation" && args[1] == "describe-stacks":
		return &runResult{Code: 0, Stdout: testRoleArn + "\n"}
	default:
		return &runResult{Code: 1, Stderr: "unexpected: " + joined}
	}
}

func (h *connectHarness) Do(req *http.Request) (*http.Response, error) {
	url := req.URL.String()
	if strings.Contains(url, "raw.githubusercontent.com") {
		return rawResponse(200, testTemplate), nil
	}
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
	}
	h.apiCalls = append(h.apiCalls, apiCallRecord{URL: url, Method: req.Method, Body: body})

	switch {
	case strings.Contains(url, "onboarding-info"):
		return jsonResponse(h.infoStatus, h.infoBody), nil
	case strings.HasSuffix(url, "/connections") && req.Method == http.MethodGet:
		return jsonResponse(h.listStatus, h.listBody), nil
	case strings.HasSuffix(url, "/connections"):
		return jsonResponse(h.createStatus, h.createBody), nil
	default: // health-check
		return jsonResponse(h.healthStatus, h.healthBody), nil
	}
}

func (h *connectHarness) ui() *UI { return &UI{Out: io.Discard, Err: &h.errBuf} }

func (h *connectHarness) errText() string { return h.errBuf.String() }

func (h *connectHarness) deps() ConnectAWSDeps {
	return ConnectAWSDeps{
		Doer:       fakeDoer(h.Do),
		Run:        h.runFn,
		Prompt:     h.prompt,
		GetToken:   h.getToken,
		GetOrgSlug: h.getSlug,
		BaseURL:    "https://api.test",
		Env:        func(string) string { return "" },
		Pin:        h.pin,
	}
}

func findAPICall(calls []apiCallRecord, method, urlSuffix string) *apiCallRecord {
	for i := range calls {
		c := &calls[i]
		if c.Method == method && strings.HasSuffix(c.URL, urlSuffix) {
			return c
		}
	}
	return nil
}

func countAPICalls(calls []apiCallRecord, method, urlSuffix string) int {
	n := 0
	for _, c := range calls {
		if (method == "" || c.Method == method) && strings.HasSuffix(c.URL, urlSuffix) {
			n++
		}
	}
	return n
}

type connectionRequestBody struct {
	ConnectionID string `json:"connectionId"`
	Label        string `json:"label"`
	Config       struct {
		AuthMode       string              `json:"authMode"`
		RoleArn        string              `json:"roleArn"`
		ExternalID     string              `json:"externalId"`
		Region         string              `json:"region"`
		MemberRoleName string              `json:"memberRoleName"`
		MemberAccounts []map[string]string `json:"memberAccounts"`
	} `json:"config"`
}

// ── flag parsing ────────────────────────────────────────────────────────────

func TestParseConnectAWSFlags_Refusals(t *testing.T) {
	if _, err := ParseConnectAWSFlags([]string{"--wat"}); err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("expected unknown-flag error, got %v", err)
	}
	if _, err := ParseConnectAWSFlags([]string{"--member", "111122223333"}); err == nil || !strings.Contains(err.Error(), "--member requires --management") {
		t.Fatalf("expected member-requires-management error, got %v", err)
	}
	if _, err := ParseConnectAWSFlags([]string{"--management"}); err == nil || !strings.Contains(err.Error(), "at least one --member") {
		t.Fatalf("expected management-needs-member error, got %v", err)
	}
	if _, err := ParseConnectAWSFlags([]string{"--management", "--member", "123"}); err == nil || !strings.Contains(err.Error(), "12-digit") {
		t.Fatalf("expected 12-digit error, got %v", err)
	}
	ok, err := ParseConnectAWSFlags([]string{"--management", "--member", "111122223333,444455556666", "--name", "prod"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(ok.Members, []string{"111122223333", "444455556666"}) {
		t.Fatalf("members = %v", ok.Members)
	}
	if ok.Name != "prod" {
		t.Fatalf("name = %q", ok.Name)
	}
}

func TestValidators(t *testing.T) {
	if !validateRoleArn(testRoleArn) {
		t.Fatal("expected a valid role ARN to validate")
	}
	if validateRoleArn("arn:aws:iam::123:role/x") {
		t.Fatal("expected a malformed role ARN to fail validation")
	}
	if !validateAccountID("111122223333") {
		t.Fatal("expected a valid account id to validate")
	}
	if validateAccountID("ctf{nope}") {
		t.Fatal("expected a malformed account id to fail validation")
	}
}

// ── the SC-007 failure matrix ───────────────────────────────────────────────

func TestRunConnectAWS_NoSession(t *testing.T) {
	h := newConnectHarness()
	h.getToken = func(context.Context) string { return "" }
	code := RunConnectAWS(context.Background(), ConnectAWSFlags{}, h.ui(), h.deps())
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(h.errText(), "landfall login") {
		t.Fatalf("errs = %q, want login instruction", h.errText())
	}
	if len(h.apiCalls) != 0 {
		t.Fatalf("apiCalls = %v, want none before a session check", h.apiCalls)
	}
	if len(h.runs) != 0 {
		t.Fatalf("runs = %v, want none before a session check", h.runs)
	}
}

func TestRunConnectAWS_OrgMismatch(t *testing.T) {
	h := newConnectHarness()
	code := RunConnectAWS(context.Background(), ConnectAWSFlags{Org: "other-org"}, h.ui(), h.deps())
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(h.errText(), `signed in to "acme", not "other-org"`) {
		t.Fatalf("errs = %q", h.errText())
	}
	if len(h.apiCalls) != 0 {
		t.Fatalf("apiCalls = %v, want none — never re-targets", h.apiCalls)
	}
}

func TestRunConnectAWS_OldPlatform404(t *testing.T) {
	h := newConnectHarness()
	h.infoStatus = 404
	code := RunConnectAWS(context.Background(), ConnectAWSFlags{}, h.ui(), h.deps())
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(h.errText(), "does not support automated AWS onboarding yet") {
		t.Fatalf("errs = %q", h.errText())
	}
	if len(h.runs) != 0 {
		t.Fatalf("runs = %v, want none", h.runs)
	}
}

func TestRunConnectAWS_Forbidden(t *testing.T) {
	h := newConnectHarness()
	h.infoStatus = 403
	RunConnectAWS(context.Background(), ConnectAWSFlags{}, h.ui(), h.deps())
	if !strings.Contains(h.errText(), "organization administrator") {
		t.Fatalf("errs = %q", h.errText())
	}
}

func TestRunConnectAWS_NotAvailable(t *testing.T) {
	h := newConnectHarness()
	h.infoBody = map[string]any{"available": false, "reason": "no AWS identity here"}
	code := RunConnectAWS(context.Background(), ConnectAWSFlags{}, h.ui(), h.deps())
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(h.errText(), "no AWS identity here") {
		t.Fatalf("errs = %q", h.errText())
	}
	if !strings.Contains(h.errText(), "keys-mode") {
		t.Fatalf("errs = %q", h.errText())
	}
	if len(h.runs) != 0 {
		t.Fatalf("runs = %v, want none", h.runs)
	}
}

func TestRunConnectAWS_MissingAWSBinary(t *testing.T) {
	h := newConnectHarness()
	h.runFn = func(context.Context, string, []string, string) *runResult { return nil }
	code := RunConnectAWS(context.Background(), ConnectAWSFlags{}, h.ui(), h.deps())
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(h.errText(), `"aws" CLI is not installed`) {
		t.Fatalf("errs = %q", h.errText())
	}
	if countAPICalls(h.apiCalls, "", "/connections") != 0 {
		t.Fatalf("apiCalls = %v, want no registration attempt", h.apiCalls)
	}
}

func TestRunConnectAWS_CredentialsDoNotResolve(t *testing.T) {
	h := newConnectHarness()
	h.runFn = func(_ context.Context, cmd string, args []string, _ string) *runResult {
		if len(args) > 0 && args[0] == "--version" {
			return &runResult{Code: 0, Stdout: "aws-cli/2"}
		}
		return &runResult{Code: 255, Stderr: "Unable to locate credentials"}
	}
	code := RunConnectAWS(context.Background(), ConnectAWSFlags{}, h.ui(), h.deps())
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(h.errText(), "credentials did not resolve") {
		t.Fatalf("errs = %q", h.errText())
	}
	if !strings.Contains(h.errText(), "Unable to locate credentials") {
		t.Fatalf("errs = %q", h.errText())
	}
}

func TestRunConnectAWS_ChecksumMismatch(t *testing.T) {
	h := newConnectHarness()
	h.pin.SHA256 = strings.Repeat("f", 64)
	code := RunConnectAWS(context.Background(), ConnectAWSFlags{}, h.ui(), h.deps())
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(h.errText(), "does not match the checksum") {
		t.Fatalf("errs = %q", h.errText())
	}
	for _, r := range h.runs {
		if strings.Contains(strings.Join(r, " "), "cloudformation") {
			t.Fatalf("cloudformation ran after a checksum mismatch: %v", r)
		}
	}
}

func TestRunConnectAWS_DeployFailure(t *testing.T) {
	h := newConnectHarness()
	h.runFn = func(_ context.Context, cmd string, args []string, _ string) *runResult {
		if len(args) > 0 && args[0] == "--version" {
			return &runResult{Code: 0, Stdout: "aws-cli/2"}
		}
		if len(args) > 0 && args[0] == "sts" {
			body, _ := json.Marshal(map[string]string{"Account": "111122223333", "Arn": "x"})
			return &runResult{Code: 0, Stdout: string(body)}
		}
		return &runResult{Code: 1, Stderr: "AccessDenied: iam:CreateRole"}
	}
	code := RunConnectAWS(context.Background(), ConnectAWSFlags{}, h.ui(), h.deps())
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	err := h.errText()
	if !strings.Contains(err, "AccessDenied: iam:CreateRole") {
		t.Fatalf("errs = %q, want the tool's own words", err)
	}
	if !strings.Contains(err, "allowed to create an IAM role") {
		t.Fatalf("errs = %q, want our interpretation", err)
	}
	if !strings.Contains(err, "re-run") {
		t.Fatalf("errs = %q, want a resume instruction", err)
	}
	if countAPICalls(h.apiCalls, http.MethodPost, "") != 0 {
		t.Fatalf("apiCalls = %v, want nothing registered", h.apiCalls)
	}
}

func TestRunConnectAWS_NameCollision(t *testing.T) {
	h := newConnectHarness()
	h.listBody = map[string]any{"connections": []any{map[string]any{"connectionId": "aws-111122223333"}}}
	code := RunConnectAWS(context.Background(), ConnectAWSFlags{}, h.ui(), h.deps())
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(h.errText(), `"aws-111122223333" already exists`) {
		t.Fatalf("errs = %q", h.errText())
	}
	if countAPICalls(h.apiCalls, http.MethodPost, "/connections") != 0 {
		t.Fatalf("apiCalls = %v, want nothing registered", h.apiCalls)
	}
}

// ── the happy path ──────────────────────────────────────────────────────────

func TestRunConnectAWS_HappyPath(t *testing.T) {
	h := newConnectHarness()
	code := RunConnectAWS(context.Background(), ConnectAWSFlags{}, h.ui(), h.deps())
	if code != 0 {
		t.Fatalf("code = %d, want 0 (errs: %s)", code, h.errText())
	}

	wantKinds := []string{"aws --version", "aws sts", "aws cloudformation", "aws cloudformation"}
	if len(h.runs) != len(wantKinds) {
		t.Fatalf("runs = %v, want %d entries", h.runs, len(wantKinds))
	}
	for i, r := range h.runs {
		got := r[0]
		if len(r) > 1 {
			got += " " + r[1]
		}
		if got != wantKinds[i] {
			t.Fatalf("run[%d] = %q, want %q", i, got, wantKinds[i])
		}
	}

	create := findAPICall(h.apiCalls, http.MethodPost, "/connections")
	if create == nil {
		t.Fatal("no POST /connections call recorded")
	}
	var body connectionRequestBody
	if err := json.Unmarshal(create.Body, &body); err != nil {
		t.Fatalf("could not decode create body: %v", err)
	}
	if body.ConnectionID != "aws-111122223333" {
		t.Fatalf("connectionId = %q", body.ConnectionID)
	}
	if !strings.HasPrefix(body.Config.RoleArn, "arn:aws:iam::111122223333:role/") {
		t.Fatalf("roleArn = %q", body.Config.RoleArn)
	}
	if body.Config.AuthMode != "role" {
		t.Fatalf("authMode = %q", body.Config.AuthMode)
	}
	if len(body.Config.ExternalID) < 22 {
		t.Fatalf("externalId = %q, want >=22 chars of entropy", body.Config.ExternalID)
	}

	deployFound := false
	for _, r := range h.runs {
		if len(r) > 2 && r[1] == "cloudformation" && r[2] == "deploy" {
			deployFound = true
			want := "ExternalId=" + body.Config.ExternalID
			has := false
			for _, a := range r {
				if a == want {
					has = true
				}
			}
			if !has {
				t.Fatalf("deploy args = %v, want %q", r, want)
			}
		}
	}
	if !deployFound {
		t.Fatal("no cloudformation deploy call recorded")
	}

	if !strings.Contains(h.errText(), "Connected ✓") {
		t.Fatalf("errs = %q", h.errText())
	}
	if !strings.Contains(h.errText(), "111122223333") {
		t.Fatalf("errs = %q", h.errText())
	}

	// FR-007: the external id never reaches any output.
	if strings.Contains(h.errText(), body.Config.ExternalID) {
		t.Fatal("external id leaked into command output")
	}
}

func TestRunConnectAWS_RegionOrder(t *testing.T) {
	h := newConnectHarness()
	RunConnectAWS(context.Background(), ConnectAWSFlags{Region: "eu-west-1"}, h.ui(), h.deps())
	for _, r := range h.runs {
		if len(r) > 2 && r[1] == "cloudformation" && r[2] == "deploy" {
			hasRegionFlag, hasRegionValue := false, false
			for _, a := range r {
				if a == "--region" {
					hasRegionFlag = true
				}
				if a == "eu-west-1" {
					hasRegionValue = true
				}
			}
			if !hasRegionFlag || !hasRegionValue {
				t.Fatalf("deploy args = %v, want --region eu-west-1", r)
			}
			return
		}
	}
	t.Fatal("no cloudformation deploy call recorded")
}

// ── --terraform (FR-012) ────────────────────────────────────────────────────

func TestRunConnectAWS_Terraform(t *testing.T) {
	h := newConnectHarness()
	answers := []string{"not-an-arn", testRoleArn}
	idx := 0
	h.prompt = func(context.Context, string) (string, error) {
		a := answers[idx]
		idx++
		return a, nil
	}
	code := RunConnectAWS(context.Background(), ConnectAWSFlags{Terraform: true}, h.ui(), h.deps())
	if code != 0 {
		t.Fatalf("code = %d, want 0 (errs: %s)", code, h.errText())
	}
	if len(h.runs) != 0 {
		t.Fatalf("runs = %v, want none — no aws CLI at all", h.runs)
	}
	printed := h.errText()
	if !strings.Contains(printed, "ref="+DefaultTemplatePin.Tag) {
		t.Fatalf("printed = %q, want the pinned tag", printed)
	}
	if !strings.Contains(printed, testPrincipal) {
		t.Fatalf("printed = %q, want the principal", printed)
	}
	if !strings.Contains(printed, "not an IAM role ARN") {
		t.Fatalf("printed = %q, want the re-prompt", printed)
	}
	create := findAPICall(h.apiCalls, http.MethodPost, "/connections")
	if create == nil {
		t.Fatal("no POST /connections call recorded")
	}
	var body connectionRequestBody
	if err := json.Unmarshal(create.Body, &body); err != nil {
		t.Fatalf("could not decode create body: %v", err)
	}
	if body.Config.RoleArn != testRoleArn {
		t.Fatalf("roleArn = %q, want the pasted ARN", body.Config.RoleArn)
	}
}

func TestTerraformSnippet(t *testing.T) {
	s := terraformSnippet(DefaultTemplatePin, testPrincipal, strings.Repeat("x", 32))
	if !strings.Contains(s, "ref=v0.1.0") {
		t.Fatalf("snippet = %q, want the pinned tag", s)
	}
	if !strings.Contains(s, testPrincipal) {
		t.Fatalf("snippet = %q, want the principal", s)
	}
	if !strings.Contains(s, strings.Repeat("x", 32)) {
		t.Fatalf("snippet = %q, want the external id", s)
	}
}

// ── --management (FR-013) ───────────────────────────────────────────────────

func TestRunConnectAWS_Management(t *testing.T) {
	h := newConnectHarness()
	code := RunConnectAWS(context.Background(), ConnectAWSFlags{
		Members:        []string{"444455556666"},
		Management:     true,
		MemberRoleName: "landfall-readonly",
	}, h.ui(), h.deps())
	if code != 0 {
		t.Fatalf("code = %d, want 0 (errs: %s)", code, h.errText())
	}
	create := findAPICall(h.apiCalls, http.MethodPost, "/connections")
	if create == nil {
		t.Fatal("no POST /connections call recorded")
	}
	var body connectionRequestBody
	if err := json.Unmarshal(create.Body, &body); err != nil {
		t.Fatalf("could not decode create body: %v", err)
	}
	if body.Config.MemberRoleName != "landfall-readonly" {
		t.Fatalf("memberRoleName = %q", body.Config.MemberRoleName)
	}
	want := []map[string]string{{"accountId": "444455556666"}}
	if !reflect.DeepEqual(body.Config.MemberAccounts, want) {
		t.Fatalf("memberAccounts = %v, want %v", body.Config.MemberAccounts, want)
	}
	printed := h.errText()
	if !strings.Contains(printed, "account 444455556666") {
		t.Fatalf("printed = %q", printed)
	}
	if !strings.Contains(printed, "iam create-role") {
		t.Fatalf("printed = %q", printed)
	}
}

func TestMemberInstructions(t *testing.T) {
	s := memberInstructions(testRoleArn, "landfall-readonly", []string{"444455556666"})
	if !strings.Contains(s, testRoleArn) {
		t.Fatalf("instructions = %q, want the management role ARN", s)
	}
	if !strings.Contains(s, "ReadOnlyAccess") {
		t.Fatalf("instructions = %q, want the ReadOnlyAccess policy", s)
	}
	if strings.Contains(s, "ExternalId") {
		t.Fatal("the management→member hop must present no external id")
	}
}
