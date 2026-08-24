package spool

import (
	"os"
	"strings"
	"testing"
)

func TestRedactsObviousSecrets(t *testing.T) {
	cases := map[string]string{
		"bearer token":   "curl -H 'Authorization: Bearer eyJhbGciOiJIUzI1NiJ9abc123' https://api",
		"openai key":     "the worker is using sk-abcd1234efgh5678 to call out",
		"github pat":     "cloned with ghp_A1b2C3d4E5f6G7h8 and it failed",
		"slack token":    "webhook posted with xoxb-1234-5678-abcdefgh",
		"aws access key": "AKIAIOSFODNN7EXAMPLE showed up in the logs",
		"password kv":    "connection string had password=hunter2correct",
		"api_key kv":     "api_key: 9f8e7d6c5b4a3210",
		"url creds":      "postgres://svc_user:s3cr3tpw@db.internal:5432/app is refusing connections",
	}

	for name, input := range cases {
		got := Redact(input)
		if !strings.Contains(got, "[redacted]") {
			t.Errorf("%s: nothing redacted in %q", name, got)
			continue
		}
		// The secret itself must be gone, not merely annotated.
		for _, leak := range []string{"eyJhbGciOiJIUzI1NiJ9abc123", "sk-abcd1234efgh5678",
			"ghp_A1b2C3d4E5f6G7h8", "xoxb-1234-5678-abcdefgh", "AKIAIOSFODNN7EXAMPLE",
			"hunter2correct", "9f8e7d6c5b4a3210", "s3cr3tpw"} {
			if strings.Contains(got, leak) {
				t.Errorf("%s: secret survived redaction: %q", name, got)
			}
		}
	}
}

// TestRedactionKeepsSurroundingContext — a redacted line still has to be worth
// reading. Blanking the whole message would protect the secret and destroy the
// finding.
func TestRedactionKeepsSurroundingContext(t *testing.T) {
	got := Redact("postgres://svc_user:s3cr3tpw@db.internal:5432/app is refusing connections")

	for _, keep := range []string{"postgres://", "svc_user", "db.internal", "refusing connections"} {
		if !strings.Contains(got, keep) {
			t.Errorf("redaction ate useful context %q: %q", keep, got)
		}
	}
}

func TestRedactsPemBodyButKeepsMarkers(t *testing.T) {
	pem := "here it is\n-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA\nabc\n-----END RSA PRIVATE KEY-----\nend"
	got := Redact(pem)

	if strings.Contains(got, "MIIEowIBAAKCAQEA") {
		t.Errorf("key body survived: %q", got)
	}
	// Keeping the markers tells the reader a key WAS here and was removed,
	// which is more useful than a silent gap.
	if !strings.Contains(got, "BEGIN RSA PRIVATE KEY") || !strings.Contains(got, "END RSA PRIVATE KEY") {
		t.Errorf("markers removed, so the reader cannot tell a key was elided: %q", got)
	}
}

func TestRedactLeavesOrdinaryTextAlone(t *testing.T) {
	ordinary := []string{
		"origin returned 502 for /api/v2/checkout starting at 14:22Z",
		"p99 latency spiked to 4.1s on the payments service",
		"deploy abc123def rolled out at 14:05 and the errors began at 14:07",
		"",
	}
	for _, input := range ordinary {
		if got := Redact(input); got != input {
			t.Errorf("Redact altered ordinary text:\n  in:  %q\n  out: %q", input, got)
		}
	}
}

// TestRedactionHappensBeforeDisk is FR-010's actual requirement.
//
// Redacting at publish time would satisfy "the room never sees it" while
// leaving the raw credential in a file that survives crashes, restarts and
// reboots. The disk is the thing being protected here, not just the wire.
func TestRedactionHappensBeforeDisk(t *testing.T) {
	s := open(t)
	const secret = "hunter2correct"

	if _, err := s.Accept("inc-1", "agent-1", "db is down, password="+secret, nil); err != nil {
		t.Fatalf("Accept: %v", err)
	}

	raw, err := os.ReadFile(s.path("inc-1"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatal("the raw secret was written to disk; redaction must happen before the entry is persisted, not on the way out")
	}
	if !strings.Contains(string(raw), "[redacted]") {
		t.Fatalf("nothing redacted on disk: %s", raw)
	}
}

// TestResponderIsToldTheirTextChanged — silently rewriting what someone wrote
// means they discover it later from the timeline, which is worse than being
// told now.
func TestResponderIsToldTheirTextChanged(t *testing.T) {
	s := open(t)

	e, err := s.Accept("inc-1", "agent-1", "token=abcd1234efgh", nil)
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if !e.Redacted {
		t.Error("entry was redacted but not flagged; the responder has no way to know")
	}

	clean, err := s.Accept("inc-1", "agent-1", "origin returned 502 at 14:22Z", nil)
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if clean.Redacted {
		t.Error("untouched text flagged as redacted; a false alarm trains people to ignore the real one")
	}
}

func TestRefsAreRedactedToo(t *testing.T) {
	s := open(t)
	e, err := s.Accept("inc-1", "agent-1", "see the config", []string{
		"src/app.go:42",
		"https://user:s3cr3tpw@internal.example/config",
	})
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if strings.Contains(strings.Join(e.Refs, " "), "s3cr3tpw") {
		t.Fatalf("a secret survived in refs: %v", e.Refs)
	}
	if e.Refs[0] != "src/app.go:42" {
		t.Errorf("an ordinary ref was altered: %q", e.Refs[0])
	}
}
