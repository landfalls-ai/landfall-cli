package cli

// hookspolicy_test.go — `landfall hooks policy [--init]`, the reporting half of
// `test/hooks/prod-policy-cli.test.mjs` (#233).
//
// #233's acceptance criterion is that "the engineer can read exactly what leaves
// the machine". These assert the printed text against that claim literally: the
// payload line must be the EXACT request body, and the closing line must say
// what does not leave.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func policyFilePath(home string) string {
	return filepath.Join(home, ".config", "landfall", "prod-policy.json")
}

// runPolicy runs the command through the real Cobra-facing entry point and
// returns everything printed to stdout plus the exit code.
func runPolicy(t *testing.T, argv ...string) (string, int) {
	t.Helper()
	out, _ := captureStreams(t, "")
	code := exitCodeOf(t, runHooksPolicyCommand(hookUI(), append([]string{"policy"}, argv...)))
	return out.String(), code
}

func TestHooksPolicyOnAMachineWithNoPolicyExplainsThatNothingIsReported(t *testing.T) {
	installSandbox(t)
	out, code := runPolicy(t)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{"no policy file", "--init"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
}

func TestHooksPolicyPrintsTheExactPayloadEachRuleWouldSend(t *testing.T) {
	home := installSandbox(t)
	writeHookPolicy(t, home)

	out, code := runPolicy(t)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{
		"kubectl-prod — kubectl aimed at production",
		`matches: kubectl containing all of "--context=prod"`,
		`sends: {"category":"kubernetes","entityHints":["prod-cluster"],"startedAt":"<when you confirm>"}`,
		"Nothing else is sent. The command line itself never leaves this machine.",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
}

func TestHooksPolicyPrintsTheNoneOfClauseAndTheAnyInvocationCase(t *testing.T) {
	home := installSandbox(t)
	dir := filepath.Join(home, ".config", "landfall")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"version":1,"rules":[
	  {"id":"kubectl-prod","command":"kubectl","allOf":["--context=prod"],"noneOf":["--dry-run"],"category":"kubernetes"},
	  {"id":"any-psql","command":"psql","category":"database"}
	]}`
	if err := os.WriteFile(filepath.Join(dir, "prod-policy.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	out, code := runPolicy(t)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out, `unless it contains any of "--dry-run"`) {
		t.Fatalf("missing the noneOf clause:\n%s", out)
	}
	// An empty allOf means every invocation of that program, and says so.
	if !strings.Contains(out, "matches: psql (any invocation)") {
		t.Fatalf("missing the any-invocation case:\n%s", out)
	}
	if !strings.Contains(out, `sends: {"category":"database","entityHints":[],"startedAt":"<when you confirm>"}`) {
		t.Fatalf("a rule with no hints must still print its exact body:\n%s", out)
	}
}

func TestHooksPolicyReportsARejectedRuleAndExitsNonZero(t *testing.T) {
	home := installSandbox(t)
	dir := filepath.Join(home, ".config", "landfall")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"version":1,"rules":[{"id":"leaky","command":"kubectl","category":"kubernetes",` +
		`"entityHints":["kubectl -n prod exec"]}]}`
	if err := os.WriteFile(filepath.Join(dir, "prod-policy.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	out, code := runPolicy(t)
	if code != 1 {
		t.Fatalf("a policy the engineer believes covers them and does not must not look like a success: exit %d", code)
	}
	if !strings.Contains(out, `! rule "leaky"`) || !strings.Contains(out, "bare identifier") {
		t.Fatalf("got:\n%s", out)
	}
	if !strings.Contains(out, "no usable rules") {
		t.Fatalf("got:\n%s", out)
	}
}

func TestHooksPolicyInitWritesAStarterAndNeverOverwritesAnExistingOne(t *testing.T) {
	home := installSandbox(t)

	out, code := runPolicy(t, "--init")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out, "wrote starter policy") {
		t.Fatalf("got:\n%s", out)
	}
	written, err := os.ReadFile(policyFilePath(home))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), "$comment") {
		t.Fatalf("the starter must document itself:\n%s", written)
	}
	// 0600, like the cached session it sits beside.
	info, err := os.Stat(policyFilePath(home))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}

	out, code = runPolicy(t, "--init")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out, "already exists:") || !strings.Contains(out, "(not overwritten)") {
		t.Fatalf("overwriting a policy someone tuned is the worst possible outcome:\n%s", out)
	}
	again, err := os.ReadFile(policyFilePath(home))
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(written) {
		t.Fatal("not one byte may change")
	}
}

func TestTheStarterPolicyIsItselfValidInitThenPolicyNeverReportsAnError(t *testing.T) {
	installSandbox(t)
	if _, code := runPolicy(t, "--init"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	out, code := runPolicy(t)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "!") {
			t.Fatalf("the starter reported an error:\n%s", out)
		}
	}
	// And the second run, after --init already found the file, still prints the
	// rules rather than stopping at "already exists".
	out, code = runPolicy(t, "--init")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out, "example-kubectl-prod-context") {
		t.Fatalf("a refused --init must still print the policy it refused to overwrite:\n%s", out)
	}
}
