package hooks

// policy_test.go — the local prod allow-list (#233, story #192), a port of
// `test/hooks/prod-policy.test.mjs`.
//
// Two properties are worth more than the rest of this file put together, and
// both are adversarial:
//
//  1. NO PART OF A COMMAND LINE CAN REACH THE PAYLOAD. Not by extraction, not
//     through a hint, not through a category. Tested by feeding command lines
//     full of secrets and asserting the produced body is byte-identical to the
//     one produced from the rule alone.
//  2. MATCHING IS LITERAL. A rule that reads like a regex is treated as text, so
//     a policy cannot silently widen into a heuristic.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// kubectlRule is the shared fixture, as raw JSON-shaped data (the form
// ValidateRule actually receives).
func kubectlRule() map[string]any {
	return map[string]any{
		"id":          "kubectl-prod",
		"command":     "kubectl",
		"allOf":       []any{"--context=prod"},
		"category":    "kubernetes",
		"entityHints": []any{"prod-cluster"},
	}
}

func withOverrides(base map[string]any, overrides map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overrides {
		out[k] = v
	}
	return out
}

func ruleOf(t *testing.T, raw map[string]any) Rule {
	t.Helper()
	rule, err := ValidateRule(raw, 0)
	if err != "" {
		t.Fatalf("expected a valid rule, got: %s", err)
	}
	return *rule
}

// writePolicyFile writes a policy file and returns its path.
func writePolicyFile(t *testing.T, value any) string {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, PolicyFileName)
	var body []byte
	switch v := value.(type) {
	case string:
		body = []byte(v)
	default:
		var err error
		body, err = json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(file, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

// --- the property: the command line cannot leak ------------------------------

func TestThePayloadIsBuiltOnlyFromTheRuleNoCommandLineCanChangeIt(t *testing.T) {
	rule := ruleOf(t, kubectlRule())
	at := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	baseline := IntentFor(rule, at)

	// Every one of these MATCHES the rule, so each one really does produce a
	// report. None of them may alter a byte of it.
	hostile := []string{
		"kubectl --context=prod exec -it payments-7f9 -- env",
		"kubectl --context=prod get secret db-root -o jsonpath={.data.password}",
		"kubectl --context=prod -n customer-pii logs pod/ssn-export-42",
		"AWS_SECRET_ACCESS_KEY=wJalrXUt kubectl --context=prod apply -f -",
		"kubectl --context=prod get po # internal-hostname.corp.example",
	}
	for _, line := range hostile {
		if !RuleMatches(rule, line) {
			t.Fatalf("expected a match for: %s", line)
		}
		if got := IntentFor(rule, at); !reflect.DeepEqual(got, baseline) {
			t.Fatalf("%q changed the payload: %+v", line, got)
		}
	}

	// And the payload is exactly the three contract keys, nothing more.
	body, err := json.Marshal(baseline)
	if err != nil {
		t.Fatal(err)
	}
	var keyed map[string]any
	if err := json.Unmarshal(body, &keyed); err != nil {
		t.Fatal(err)
	}
	if len(keyed) != 3 {
		t.Fatalf("payload has %d keys: %s", len(keyed), body)
	}
	want := `{"category":"kubernetes","entityHints":["prod-cluster"],"startedAt":"2026-08-04T12:00:00.000Z"}`
	if string(body) != want {
		t.Fatalf("got  %s\nwant %s", body, want)
	}
}

func TestIntentForCannotSeeACommandLineAtAllArityIsTheGuarantee(t *testing.T) {
	// A signature that accepts the command line is a signature that can leak it.
	// This is the structural version of the test above: it fails the moment
	// somebody adds a parameter, before any extraction logic can be written.
	//
	// The Node original asserts `intentFor.length === 2`; Go has no runtime
	// arity, so the check is on the function's TYPE — which is the stronger
	// statement, since it also pins what those two parameters are.
	// The explicit type is the assertion — inferring it (QF1011) would make this
	// line a no-op that passes whatever IntentFor's signature becomes.
	var _ func(Rule, time.Time) Intent = IntentFor //nolint:staticcheck // QF1011: the type IS the test
}

func TestEveryHintARuleCanSendSurvivesTheServerSideEntityHintBound(t *testing.T) {
	rule := ruleOf(t, withOverrides(kubectlRule(), map[string]any{
		"entityHints": []any{"prod-cluster", "api.prod.example.com", "svc/payments-7f9"},
	}))
	for _, hint := range IntentFor(rule, time.Unix(0, 0)).EntityHints {
		if !EntityHintPattern.MatchString(hint) {
			t.Fatalf("%q is outside the server's bound", hint)
		}
		if strings.ContainsAny(hint, " \t\n") {
			t.Fatalf("%q carries whitespace", hint)
		}
	}
}

// --- validation refuses anything the server would reject ---------------------

func TestAHintThatCouldCarryACommandLineIsRefusedAtLoadTime(t *testing.T) {
	smuggles := []string{
		"kubectl -n prod exec pod", // whitespace
		"prod; rm -rf /",           // metacharacter + whitespace
		"$(cat /etc/passwd)",       // substitution
		"`id`",                     // backticks
		"-rf",                      // leading flag
		strings.Repeat("a", 129),   // over the length bound
		"pods|grep secret",         // pipe
	}
	for _, hint := range smuggles {
		rule, err := ValidateRule(withOverrides(kubectlRule(), map[string]any{"entityHints": []any{hint}}), 0)
		if rule != nil {
			t.Fatalf("expected %q to be refused", hint)
		}
		if !strings.Contains(err, "bare identifier") {
			t.Fatalf("%q: %s", hint, err)
		}
	}
}

func TestACategoryOutsideTheClosedVocabularyIsRefused(t *testing.T) {
	_, err := ValidateRule(withOverrides(kubectlRule(), map[string]any{"category": "exfiltrate"}), 0)
	if !strings.Contains(err, "category") {
		t.Fatalf("got %q", err)
	}
	// …and the vocabulary is exactly the server's.
	want := []string{"kubernetes", "cloud", "database", "logs", "deployment", "network", "other"}
	if !reflect.DeepEqual(IntentCategories(), want) {
		t.Fatalf("got %v", IntentCategories())
	}
}

func TestMoreThanTenHintsIsRefused(t *testing.T) {
	hints := make([]any, 11)
	for i := range hints {
		hints[i] = "svc-" + string(rune('0'+i%10))
	}
	_, err := ValidateRule(withOverrides(kubectlRule(), map[string]any{"entityHints": hints}), 0)
	if !strings.Contains(err, "at most 10") {
		t.Fatalf("got %q", err)
	}
}

func TestARuleMissingIDOrCommandIsRefusedAndNamesItselfInTheError(t *testing.T) {
	_, err := ValidateRule(map[string]any{"command": "kubectl", "category": "logs"}, 3)
	if !strings.Contains(err, `#4: missing "id"`) {
		t.Fatalf("got %q", err)
	}
	_, err = ValidateRule(map[string]any{"id": "x", "category": "logs"}, 0)
	if !strings.Contains(err, `"x": missing "command"`) {
		t.Fatalf("got %q", err)
	}
}

// --- matching is literal, and only literal -----------------------------------

func TestARuleMatchesOnTheProgramNameNotOnTheStringAppearingAnywhere(t *testing.T) {
	rule := ruleOf(t, kubectlRule())
	cases := map[string]bool{
		"kubectl --context=prod get po":                true,
		"/usr/local/bin/kubectl --context=prod get po": true,
		`echo "kubectl --context=prod get po"`:         false, // the program is echo
		"kubectx --context=prod":                       false,
	}
	for line, want := range cases {
		if got := RuleMatches(rule, line); got != want {
			t.Fatalf("%q → %v, want %v", line, got, want)
		}
	}
}

func TestAllOfNeedlesAreLiteralTextNeverPatterns(t *testing.T) {
	rule := ruleOf(t, withOverrides(kubectlRule(), map[string]any{"allOf": []any{"--context=prod.*"}}))
	if !RuleMatches(rule, "kubectl --context=prod.* get po") {
		t.Fatal("the literal text must match")
	}
	// As a regex this would match; as literal text it must not.
	if RuleMatches(rule, "kubectl --context=production get po") {
		t.Fatal("a policy must not silently widen into a heuristic")
	}
}

func TestEveryAllOfNeedleMustAppearAndNoneOfVetoesTheMatch(t *testing.T) {
	rule := ruleOf(t, withOverrides(kubectlRule(), map[string]any{
		"allOf":  []any{"--context=prod", "delete"},
		"noneOf": []any{"--dry-run"},
	}))
	cases := map[string]bool{
		"kubectl --context=prod delete po/x":                  true,
		"kubectl --context=prod get po":                       false,
		"kubectl --context=prod delete po/x --dry-run=client": false,
	}
	for line, want := range cases {
		if got := RuleMatches(rule, line); got != want {
			t.Fatalf("%q → %v, want %v", line, got, want)
		}
	}
}

func TestAnEmptyAllOfMeansEveryInvocationOfThatProgram(t *testing.T) {
	rule := ruleOf(t, map[string]any{"id": "any-psql", "command": "psql", "category": "database"})
	if !RuleMatches(rule, "psql -h localhost") {
		t.Fatal("an empty allOf matches every invocation")
	}
	if len(rule.AllOf) != 0 {
		t.Fatalf("got %v", rule.AllOf)
	}
	if got := IntentFor(rule, time.Unix(0, 0)).EntityHints; len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestMatchCommandReturnsTheFirstMatchingRuleInFileOrder(t *testing.T) {
	rules := []Rule{
		ruleOf(t, map[string]any{"id": "first", "command": "aws", "allOf": []any{"--profile=prod"}, "category": "cloud"}),
		ruleOf(t, map[string]any{"id": "second", "command": "aws", "allOf": []any{"--profile=prod"}, "category": "logs"}),
	}
	got := MatchCommand(rules, "aws --profile=prod s3 ls")
	if got == nil || got.ID != "first" {
		t.Fatalf("got %+v", got)
	}
	if MatchCommand(rules, "aws --profile=staging s3 ls") != nil {
		t.Fatal("a non-matching command matches nothing")
	}
}

func TestProgramNameToleratesPathsQuotesAndLeadingWhitespace(t *testing.T) {
	cases := map[string]string{
		"  /opt/bin/psql -h db":      "psql",
		`"/opt/my tools/kubectl"`:    "kubectl", // no split-on-space surprise
		"":                           "",
		"   ":                        "",
		"KUBECONFIG=/tmp/k kubectl":  "kubectl", // a leading NAME=value assignment
		"A=1 B=2 kubectl get po":     "kubectl",
		`'/opt/my tools/psql' -h db`: "psql",
	}
	for line, want := range cases {
		if got := ProgramName(line); got != want {
			t.Fatalf("ProgramName(%q) = %q, want %q", line, got, want)
		}
	}
}

func TestProgramNameDoesNotLookThroughWrappers(t *testing.T) {
	// Deliberately NOT handled: `sudo`, `env`, `xargs`, pipelines. Those invoke a
	// different program, and a matcher that looked "through" them would be
	// exactly the heuristic #192 rules out. An engineer who runs prod commands
	// under sudo writes a `sudo` rule, and can see that they did.
	for line, want := range map[string]string{
		"sudo kubectl --context=prod get po":  "sudo",
		"env KUBECONFIG=x kubectl get po":     "env",
		"ls | xargs kubectl delete":           "ls",
		"cd /tmp; kubectl --context=prod get": "cd",
	} {
		if got := ProgramName(line); got != want {
			t.Fatalf("ProgramName(%q) = %q, want %q", line, got, want)
		}
	}
}

// --- loading -----------------------------------------------------------------

func TestAMissingPolicyFileIsTheShippedStateNotAnError(t *testing.T) {
	got := LoadPolicy(filepath.Join(t.TempDir(), "landfall-absent-policy.json"))
	if got.Exists || len(got.Rules) != 0 || len(got.Errors) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestOneBadRuleIsReportedAndSkippedTheGoodOnesStillLoad(t *testing.T) {
	file := writePolicyFile(t, map[string]any{
		"version": 1,
		"rules": []any{
			kubectlRule(),
			withOverrides(kubectlRule(), map[string]any{"id": "bad", "category": "nope"}),
		},
	})
	got := LoadPolicy(file)
	if len(got.Rules) != 1 || got.Rules[0].ID != "kubectl-prod" {
		t.Fatalf("rules %+v", got.Rules)
	}
	if len(got.Errors) != 1 || !strings.Contains(got.Errors[0], `"bad"`) {
		t.Fatalf("errors %v", got.Errors)
	}
}

func TestMalformedJSONReportsAndLoadsNothingAtAllItNeverGuesses(t *testing.T) {
	file := writePolicyFile(t, `{ "rules": [`)
	got := LoadPolicy(file)
	if !got.Exists || len(got.Rules) != 0 {
		t.Fatalf("got %+v", got)
	}
	if len(got.Errors) == 0 || !strings.Contains(got.Errors[0], "not valid JSON") {
		t.Fatalf("errors %v", got.Errors)
	}
}

func TestAPolicyWithoutARulesArrayYieldsNoRules(t *testing.T) {
	file := writePolicyFile(t, map[string]any{"version": 1})
	got := LoadPolicy(file)
	if len(got.Rules) != 0 {
		t.Fatalf("rules %+v", got.Rules)
	}
	if len(got.Errors) == 0 || !strings.Contains(got.Errors[0], `expected a "rules" array`) {
		t.Fatalf("errors %v", got.Errors)
	}
}

func TestThePolicyFileLivesBesideTheCachedSessionNotInTheRepo(t *testing.T) {
	// A policy that travelled with a checkout would let a cloned repository
	// decide what an engineer's machine reports.
	t.Setenv("XDG_CONFIG_HOME", "/tmp/lf-config-fixture")
	if got := PolicyPath(); got != filepath.Join("/tmp/lf-config-fixture", "landfall", "prod-policy.json") {
		t.Fatalf("got %q", got)
	}
}

func TestTheStarterPolicyIsItselfValid(t *testing.T) {
	// `--init` then `policy` must never report an error — a starter file that
	// does not load is the worst possible first impression of a command that
	// reads like an explanation.
	file := filepath.Join(t.TempDir(), PolicyFileName)
	if err := os.WriteFile(file, StarterPolicyJSON(), 0o600); err != nil {
		t.Fatal(err)
	}
	got := LoadPolicy(file)
	if !got.Exists || len(got.Errors) != 0 || len(got.Rules) != 1 {
		t.Fatalf("got %+v", got)
	}
	if !strings.Contains(string(StarterPolicyJSON()), "$comment") {
		t.Fatal("the starter must document itself")
	}
	// And the comment says the one thing the whole design rests on.
	if !strings.Contains(string(StarterPolicyJSON()), "never the command line") {
		t.Fatal("the starter must say what does NOT leave")
	}
}
