package hooks

// pretooluse_test.go — the hook handler that turns a matched command into a
// declared intent (#233), and the three guarantees it exists to keep. A port of
// `test/hooks/pre-tool-use.test.mjs`.
//
//	nothing leaves without the confirm     — every non-`y` path sends nothing
//	nothing unclassified leaves            — the request body is the rule's
//	non-matching commands cost nothing     — no policy read, no tty, no network

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// preToolUseSpy records every side effect the hook could have, so a test can
// assert on what did NOT happen as easily as on what did.
type preToolUseSpy struct {
	policyReads int
	confirms    []string
	resolves    int
	sends       []sentIntent
	logs        []string
}

type sentIntent struct {
	Target Target `json:"target"`
	Intent Intent `json:"intent"`
}

type spyConfig struct {
	policy    *Policy
	confirmed bool
	target    *Target
	outcome   *DeclaredIntentOutcome
}

func (s *preToolUseSpy) options(cfg spyConfig) PreToolUseOptions {
	policy := Policy{Exists: true, Rules: []Rule{preToolUseRule(nil)}, Errors: nil}
	if cfg.policy != nil {
		policy = *cfg.policy
	}
	target := Target{OK: true, BaseURL: "https://api.example", Slug: "acme", Token: "t"}
	if cfg.target != nil {
		target = *cfg.target
	}
	outcome := DeclaredIntentOutcome{Decision: "none"}
	if cfg.outcome != nil {
		outcome = *cfg.outcome
	}
	return PreToolUseOptions{
		Log:        func(line string) { s.logs = append(s.logs, line) },
		ReadPolicy: func() Policy { s.policyReads++; return policy },
		Confirm: func(question string) bool {
			s.confirms = append(s.confirms, question)
			return cfg.confirmed
		},
		Resolve: func() Target { s.resolves++; return target },
		Send: func(t Target, i Intent) DeclaredIntentOutcome {
			s.sends = append(s.sends, sentIntent{Target: t, Intent: i})
			return outcome
		},
		Now: func() time.Time { return time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC) },
	}
}

func (s *preToolUseSpy) logText() string { return strings.Join(s.logs, "\n") }

// preToolUseRule is the shared kubectl-prod rule, validated exactly as a real
// policy file's would be.
func preToolUseRule(t *testing.T) Rule {
	raw := map[string]any{
		"id":          "kubectl-prod",
		"command":     "kubectl",
		"allOf":       []any{"--context=prod"},
		"category":    "kubernetes",
		"entityHints": []any{"prod-cluster"},
	}
	rule, err := ValidateRule(raw, 0)
	if err != "" {
		if t != nil {
			t.Fatalf("fixture rule is invalid: %s", err)
		}
		panic("pre-tool-use fixture rule is invalid: " + err)
	}
	return *rule
}

func bashEvent(command string) string {
	body, _ := json.Marshal(map[string]any{
		"tool_name":  "Bash",
		"tool_input": map[string]any{"command": command},
	})
	return string(body)
}

func runPreToolUse(t *testing.T, input string, s *preToolUseSpy, cfg spyConfig) PreToolUseOutcome {
	t.Helper()
	opts := s.options(cfg)
	opts.Input = input
	return RunPreToolUse(context.Background(), opts)
}

// --- nothing leaves without the confirm --------------------------------------

func TestAMatchedCommandWithNoConfirmationSendsNothing(t *testing.T) {
	s := &preToolUseSpy{}
	got := runPreToolUse(t, bashEvent("kubectl --context=prod get po"), s, spyConfig{confirmed: false})

	if got.Result != "declined" || got.ExitCode != 0 {
		t.Fatalf("got %+v", got)
	}
	if len(s.confirms) != 1 {
		t.Fatalf("confirms %v", s.confirms)
	}
	if len(s.sends) != 0 {
		t.Fatal("the whole point: nothing leaves without the confirm")
	}
}

func TestThePromptNamesTheRuleAndTheClassificationNeverTheCommand(t *testing.T) {
	s := &preToolUseSpy{}
	runPreToolUse(t, bashEvent("kubectl --context=prod exec -it payments -- cat /run/secrets/db"), s, spyConfig{})

	if len(s.confirms) != 1 {
		t.Fatalf("confirms %v", s.confirms)
	}
	question := s.confirms[0]
	for _, want := range []string{"kubectl-prod", "kubernetes", "prod-cluster"} {
		if !strings.Contains(question, want) {
			t.Fatalf("question is missing %q: %s", want, question)
		}
	}
	for _, leak := range []string{"exec", "secrets", "payments"} {
		if strings.Contains(question, leak) {
			t.Fatalf("the question leaked %q: %s", leak, question)
		}
	}
}

func TestOnConfirmationTheIntentIsSentAndItIsExactlyTheRule(t *testing.T) {
	s := &preToolUseSpy{}
	got := runPreToolUse(t, bashEvent("kubectl --context=prod get secret db-root -o yaml"), s, spyConfig{
		confirmed: true,
		outcome:   &DeclaredIntentOutcome{Decision: "attach", IncidentID: "abc", WorkspaceURL: "https://app/x"},
	})

	if got.Result != "declared" {
		t.Fatalf("got %+v", got)
	}
	if len(s.sends) != 1 {
		t.Fatalf("sends %+v", s.sends)
	}
	want := Intent{Category: "kubernetes", EntityHints: []string{"prod-cluster"}, StartedAt: "2026-08-04T12:00:00.000Z"}
	if s.sends[0].Intent.Category != want.Category ||
		s.sends[0].Intent.StartedAt != want.StartedAt ||
		len(s.sends[0].Intent.EntityHints) != 1 ||
		s.sends[0].Intent.EntityHints[0] != want.EntityHints[0] {
		t.Fatalf("got %+v, want %+v", s.sends[0].Intent, want)
	}

	// No fragment of the command line appears anywhere in what was sent.
	wire, err := json.Marshal(s.sends[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"secret", "db-root", "yaml", "get "} {
		if strings.Contains(string(wire), fragment) {
			t.Fatalf("%q reached the request: %s", fragment, wire)
		}
	}
	if !strings.Contains(s.logText(), "joined the matching war room") {
		t.Fatalf("logs %v", s.logs)
	}
}

// --- a non-matching command costs nothing ------------------------------------

func TestANonShellToolStopsBeforeThePolicyIsEvenRead(t *testing.T) {
	s := &preToolUseSpy{}
	got := runPreToolUse(t, `{"tool_name":"Edit","tool_input":{"file_path":"/etc/hosts"}}`, s, spyConfig{})

	if got.Result != "not-a-shell-command" || got.ExitCode != 0 {
		t.Fatalf("got %+v", got)
	}
	if s.policyReads != 0 || len(s.confirms) != 0 || s.resolves != 0 || len(s.sends) != 0 {
		t.Fatalf("a non-shell tool touched something: %+v", s)
	}
}

func TestAShellCommandMatchingNoRuleReachesNeitherTheTerminalNorTheNetwork(t *testing.T) {
	s := &preToolUseSpy{}
	got := runPreToolUse(t, bashEvent("git status"), s, spyConfig{})

	if got.Result != "no-match" {
		t.Fatalf("got %+v", got)
	}
	if s.policyReads != 1 {
		t.Fatalf("policy reads %d", s.policyReads)
	}
	if len(s.confirms) != 0 || s.resolves != 0 || len(s.sends) != 0 {
		t.Fatalf("a non-matching command touched something: %+v", s)
	}
}

func TestWithNoPolicyFileNothingOnTheMachineIsReportable(t *testing.T) {
	s := &preToolUseSpy{}
	got := runPreToolUse(t, bashEvent("kubectl --context=prod get po"), s, spyConfig{
		policy: &Policy{Exists: false},
	})

	if got.Result != "no-policy" {
		t.Fatalf("got %+v", got)
	}
	if len(s.confirms) != 0 || len(s.sends) != 0 {
		t.Fatalf("%+v", s)
	}
}

func TestAnUnreadablePolicyIsAnnouncedNotSilentlyIgnored(t *testing.T) {
	s := &preToolUseSpy{}
	got := runPreToolUse(t, bashEvent("kubectl --context=prod get po"), s, spyConfig{
		policy: &Policy{Exists: true, Errors: []string{"prod-policy.json is not valid JSON"}},
	})

	if got.Result != "no-policy" {
		t.Fatalf("got %+v", got)
	}
	if !strings.Contains(s.logText(), "not valid JSON") {
		t.Fatalf("logs %v", s.logs)
	}
	if !strings.Contains(s.logText(), "prod-policy: ") {
		t.Fatalf("the announcement must name itself: %v", s.logs)
	}
}

// --- failure modes never disturb the engineer's command ----------------------

func TestAnUnsendableIntentIsNotWorthAPromptAndStillExitsZero(t *testing.T) {
	s := &preToolUseSpy{}
	got := runPreToolUse(t, bashEvent("kubectl --context=prod get po"), s, spyConfig{
		confirmed: true,
		target:    &Target{OK: false, Reason: "not signed in — run `landfall login`"},
	})

	if got.Result != "no-target" || got.ExitCode != 0 {
		t.Fatalf("got %+v", got)
	}
	if len(s.confirms) != 0 {
		t.Fatal("asked nobody: it could not have been sent")
	}
	if len(s.sends) != 0 {
		t.Fatalf("sends %+v", s.sends)
	}
	if !strings.Contains(s.logText(), "not signed in") {
		t.Fatalf("logs %v", s.logs)
	}
	if !strings.Contains(s.logText(), `matched prod policy "kubectl-prod" but`) {
		t.Fatalf("logs %v", s.logs)
	}
}

func TestAServerThatOpenedNothingIsReportedPlainlyExitZero(t *testing.T) {
	s := &preToolUseSpy{}
	got := runPreToolUse(t, bashEvent("kubectl --context=prod get po"), s, spyConfig{
		confirmed: true,
		outcome:   &DeclaredIntentOutcome{Decision: "none", Reason: "declared intent not accepted (HTTP 404)"},
	})

	if got.Result != "declared-none" || got.ExitCode != 0 {
		t.Fatalf("got %+v", got)
	}
	if !strings.Contains(s.logText(), "no war room opened (declared intent not accepted (HTTP 404))") {
		t.Fatalf("logs %v", s.logs)
	}
}

func TestGarbageOnStdinIsInertNeverAnErrorInTheAgentTurn(t *testing.T) {
	for _, payload := range []string{
		"",
		"not json",
		"[]",
		`{"tool_name":"Bash"}`,
		`{"tool_name":"Bash","tool_input":{"command":"   "}}`,
	} {
		s := &preToolUseSpy{}
		got := runPreToolUse(t, payload, s, spyConfig{})
		if got.ExitCode != 0 {
			t.Fatalf("payload %q: exit %d", payload, got.ExitCode)
		}
		if got.Result != "not-a-shell-command" {
			t.Fatalf("payload %q: %s", payload, got.Result)
		}
		if s.policyReads != 0 {
			t.Fatalf("payload %q read the policy", payload)
		}
	}
}

func TestPreToolUseAlwaysExitsZeroOnEveryPath(t *testing.T) {
	// A non-zero PreToolUse exit BLOCKS the tool call in Claude Code, and this
	// hook exists to observe an investigation, not to gate one.
	cases := []struct {
		name  string
		input string
		cfg   spyConfig
	}{
		{"not a shell command", `{"tool_name":"Edit"}`, spyConfig{}},
		{"no policy", bashEvent("kubectl --context=prod get po"), spyConfig{policy: &Policy{Exists: false}}},
		{"no match", bashEvent("git status"), spyConfig{}},
		{"no target", bashEvent("kubectl --context=prod get po"), spyConfig{target: &Target{OK: false, Reason: "x"}}},
		{"declined", bashEvent("kubectl --context=prod get po"), spyConfig{confirmed: false}},
		{"declared", bashEvent("kubectl --context=prod get po"), spyConfig{confirmed: true}},
	}
	for _, c := range cases {
		got := runPreToolUse(t, c.input, &preToolUseSpy{}, c.cfg)
		if got.ExitCode != 0 {
			t.Fatalf("%s: exit %d", c.name, got.ExitCode)
		}
	}
}

func TestPreToolUseWritesNothingToStdoutThroughTheDispatcher(t *testing.T) {
	// stdout stays empty because the host parses it; anything meant for the human
	// goes to stderr through Log.
	var wroteStdout string
	got := RunHookEvent(context.Background(), "pre-tool-use", HookDeps{
		Input: `{"tool_name":"Edit","tool_input":{"file_path":"/etc/hosts"}}`,
		Host:  "claude-code",
		Emit: func(text, channel string) {
			if channel == ChannelStdout {
				wroteStdout += text
			}
		},
	})
	if got.ExitCode != 0 || got.Result != "not-a-shell-command" {
		t.Fatalf("got %+v", got)
	}
	if wroteStdout != "" || got.Stdout != "" {
		t.Fatalf("stdout is the host's channel, got %q / %q", wroteStdout, got.Stdout)
	}
}

func TestAnUnknownHookEventIsStillAUsageError(t *testing.T) {
	got := RunHookEvent(context.Background(), "not-an-event", HookDeps{})
	if got.ExitCode != 2 {
		t.Fatalf("exit %d", got.ExitCode)
	}
	if !strings.Contains(got.Error, "unknown hook event") {
		t.Fatalf("got %q", got.Error)
	}
}

// --- the confirm wording -----------------------------------------------------

func TestConfirmQuestionOmitsTheHintBracketWhenARuleCarriesNoHints(t *testing.T) {
	rule := Rule{ID: "any-psql", Command: "psql", Category: "database", EntityHints: []string{}}
	got := ConfirmQuestion(rule, IntentFor(rule, time.Unix(0, 0)))
	want := `landfall: this looks like a production investigation (any-psql). Report "database" and open a war room for it?`
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestConfirmQuestionListsEveryHintTheRuleWouldSend(t *testing.T) {
	rule := preToolUseRule(t)
	rule.EntityHints = []string{"prod-cluster", "api.prod.example.com"}
	got := ConfirmQuestion(rule, IntentFor(rule, time.Unix(0, 0)))
	want := `landfall: this looks like a production investigation (kubectl-prod). ` +
		`Report "kubernetes" [prod-cluster, api.prod.example.com] and open a war room for it?`
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

// --- declare.go --------------------------------------------------------------

func TestDescribeOutcomeNeverEchoesTheIntent(t *testing.T) {
	cases := []struct {
		outcome DeclaredIntentOutcome
		want    string
	}{
		{DeclaredIntentOutcome{Decision: "attach", WorkspaceURL: "https://app/x"}, "joined the matching war room — https://app/x"},
		{DeclaredIntentOutcome{Decision: "attach"}, "joined the matching war room"},
		{DeclaredIntentOutcome{Decision: "open", WorkspaceURL: "https://app/y"}, "opened a provisional war room — https://app/y"},
		{DeclaredIntentOutcome{Decision: "open"}, "opened a provisional war room"},
		{DeclaredIntentOutcome{Decision: "none", Reason: "declared intent not accepted (HTTP 404)"},
			"no war room opened (declared intent not accepted (HTTP 404))"},
		{DeclaredIntentOutcome{Decision: "none"}, "no war room opened"},
		{DeclaredIntentOutcome{}, "no war room opened"},
	}
	for _, c := range cases {
		if got := DescribeOutcome(c.outcome); got != c.want {
			t.Fatalf("got  %q\nwant %q", got, c.want)
		}
	}
}

func TestResolveTargetRefusesBeforeAnythingCanBeSent(t *testing.T) {
	// Resolved BEFORE the engineer is prompted: asking someone to approve a
	// report that cannot be sent wastes the one interruption this feature gets.
	noToken := ResolveTarget(context.Background(), TargetDeps{
		Env:         func(string) string { return "" },
		ReadToken:   func(context.Context) string { return "" },
		ReadOrgSlug: func() string { return "acme" },
	})
	if noToken.OK || !strings.Contains(noToken.Reason, "not signed in") {
		t.Fatalf("got %+v", noToken)
	}

	noSlug := ResolveTarget(context.Background(), TargetDeps{
		Env:         func(string) string { return "" },
		ReadToken:   func(context.Context) string { return "tok" },
		ReadOrgSlug: func() string { return "" },
	})
	if noSlug.OK || !strings.Contains(noSlug.Reason, "LANDFALL_SLUG") {
		t.Fatalf("got %+v", noSlug)
	}
}

func TestResolveTargetPrefersTheEnvironmentSlugAndTrimsTheBaseURL(t *testing.T) {
	env := map[string]string{"LANDFALL_SLUG": "from-env", "LANDFALL_BASE_URL": "https://api.example/"}
	got := ResolveTarget(context.Background(), TargetDeps{
		Env:         func(k string) string { return env[k] },
		ReadToken:   func(context.Context) string { return "tok" },
		ReadOrgSlug: func() string { return "from-cache" },
	})
	if !got.OK || got.Slug != "from-env" || got.BaseURL != "https://api.example" || got.Token != "tok" {
		t.Fatalf("got %+v", got)
	}
}
