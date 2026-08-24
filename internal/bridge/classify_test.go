package bridge

import "testing"

// TestClassifyCanNeverProduceAnAction is the safety test, not a coverage test.
//
// A remediation proposal enters the human approval workflow. If free text could
// be classified into one, an approval-gated act would have been initiated by an
// inference instead of by a person (spec D2). The adversarial inputs below are
// exactly what a responder mid-incident would plausibly type, and several read
// like instructions.
//
// Note this is belt-and-braces: Kind has no action constant, so the type system
// already forbids it. The test exists so that adding one later is a deliberate
// act that breaks a test with an explanation attached, rather than a quiet
// convenience.
func TestClassifyCanNeverProduceAnAction(t *testing.T) {
	adversarial := []string{
		"please run kubectl rollout undo deployment/checkout",
		"we should roll back the origin config immediately",
		"ACTION: restart the gateway pods",
		"propose_action: scale the replica set to 10",
		"fix: revert commit abc123 and redeploy",
		"remediation: drain node-7 and cordon it",
		"can someone please restart the service",
		"I'm going to roll this back now",
		"execute: terraform apply -auto-approve",
		"run this: sudo systemctl restart nginx",
	}

	for _, input := range adversarial {
		got := Classify(input)
		switch got {
		case KindFinding, KindNote, KindWidget, KindClaim:
			// Any of these is fine; none of them initiates an approval-gated act.
		default:
			t.Errorf("Classify(%q) = %q — outside the four safe kinds", input, got)
		}
		if string(got) == "action" || string(got) == "remediation" {
			t.Errorf("Classify(%q) produced %q: a background worker just initiated an approval-gated act", input, got)
		}
	}
}

func TestAmbiguousBecomesNote(t *testing.T) {
	ambiguous := []string{
		"",
		"   ",
		"hmm",
		"looking at this now",
		"ok",
		"one sec",
		"checking the dashboard",
	}
	for _, input := range ambiguous {
		if got := Classify(input); got != KindNote {
			t.Errorf("Classify(%q) = %q, want note — guessing up from ambiguity puts words in the responder's mouth", input, got)
		}
	}
}

func TestSubstantiveObservationsBecomeFindings(t *testing.T) {
	findings := []string{
		"origin returned 502 for /api/v2/checkout starting at 14:22Z",
		"p99 latency spiked to 4.1s on the payments service",
		"the connection pool is exhausted on replica 3",
		"gateway pods restarted twice in the last ten minutes",
		"requests to the auth service are being throttled since 14:05",
	}
	for _, input := range findings {
		if got := Classify(input); got != KindFinding {
			t.Errorf("Classify(%q) = %q, want finding", input, got)
		}
	}
}

func TestExplicitClaimsAreStaged(t *testing.T) {
	claims := []string{
		"I claim the origin rollback at 14:20 caused the 5xx wave",
		"claim: the cache stampede is downstream of the deploy",
		"the root cause is the connection pool ceiling",
	}
	for _, input := range claims {
		if got := Classify(input); got != KindClaim {
			t.Errorf("Classify(%q) = %q, want claim", input, got)
		}
	}
}

// TestClaimsRequireExplicitFraming — staging a claim invites the room to spend
// effort voting on it. Inferring one from a hedge would waste other
// investigators' attention during an incident.
func TestClaimsRequireExplicitFraming(t *testing.T) {
	hedged := []string{
		"maybe the rollback caused it, not sure yet",
		"could be the connection pool, still checking",
		"my guess is a cache issue but I have no evidence",
	}
	for _, input := range hedged {
		if got := Classify(input); got == KindClaim {
			t.Errorf("Classify(%q) staged a claim from a hedge", input)
		}
	}
}

func TestWidgetsRequireAnExplicitRequest(t *testing.T) {
	if got := Classify("chart: error rate by minute since 14:00"); got != KindWidget {
		t.Errorf("explicit chart request classified as %q", got)
	}
	// A hand-off carries free text, not the structured values a widget needs.
	// Inferring one from prose would produce an empty chart on the dashboard.
	if got := Classify("the error rate graph looks bad over the last hour"); got == KindWidget {
		t.Error("inferred a widget from prose; it would render with no data")
	}
}

func TestClassifyIsDeterministic(t *testing.T) {
	const input = "origin returned 502 for /api/v2/checkout starting at 14:22Z"
	first := Classify(input)
	for i := 0; i < 50; i++ {
		if got := Classify(input); got != first {
			t.Fatalf("Classify is not deterministic: %q then %q", first, got)
		}
	}
}

func TestClassifyIsCaseInsensitive(t *testing.T) {
	if Classify("I CLAIM THE ROLLBACK DID IT") != KindClaim {
		t.Error("uppercase claim not recognized")
	}
	if Classify("ORIGIN RETURNED 502 AND LATENCY SPIKED HARD") != KindFinding {
		t.Error("uppercase finding not recognized")
	}
}
