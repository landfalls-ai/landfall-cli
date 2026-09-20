package daemon

import (
	"testing"

	"github.com/landfalls-ai/landfall-cli/internal/client"
)

func seq(n int64) *int64 { return &n }

func TestUntoldSkipsMachineryAndPutsAddressedFirst(t *testing.T) {
	events := []client.Event{
		{Seq: seq(1), Type: "chat.message", Payload: map[string]any{"text": "old, already shown"}},
		{Seq: seq(2), Type: "agent.query"},
		{Seq: seq(3), Type: "edge.finding", Payload: map[string]any{"text": "p99 spiked"}},
		{Seq: seq(4), Type: "agent.widget.executed"},
		{Seq: seq(5), Type: "chat.message", Payload: map[string]any{"text": "@alice can you confirm?"}},
		{Seq: seq(6), Type: "claim.admitted"},
	}
	got := Untold(events, 1)
	want := []int64{5, 3, 6}
	if len(got) != len(want) {
		t.Fatalf("untold = %d events, want %d", len(got), len(want))
	}
	for i, w := range want {
		if *got[i].Seq != w {
			t.Fatalf("untold[%d].seq = %d, want %d (addressed first, then by seq, no machinery)", i, *got[i].Seq, w)
		}
	}
}

func TestOnlyACallerOfTheSameKindMovesACursor(t *testing.T) {
	terminal := &Reader{Name: TerminalReaderName("ws"), Kind: KindTerminal, Cursor: 10}
	if _, err := terminal.Advance(KindAgent, 20); err != ErrWrongKind || terminal.Cursor != 10 {
		t.Fatalf("an agent moved the person's cursor: cursor=%d err=%v", terminal.Cursor, err)
	}
	if c, err := terminal.Advance(KindTerminal, 20); err != nil || c != 20 {
		t.Fatalf("a hook (terminal kind) must move it: cursor=%d err=%v", c, err)
	}
	if c, _ := terminal.Advance(KindTerminal, 15); c != 20 {
		t.Fatalf("cursors never move backwards, got %d", c)
	}
}

func TestParseKindRefusesTheUnknown(t *testing.T) {
	for _, ok := range []string{"agent", "terminal", "panel", "push"} {
		if _, err := ParseKind(ok); err != nil {
			t.Fatalf("%q should parse: %v", ok, err)
		}
	}
	if _, err := ParseKind("subagent"); err == nil {
		t.Fatal("an unknown kind must be refused, not guessed")
	}
}
