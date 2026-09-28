package narrate

import (
	"strings"
	"testing"

	"github.com/landfalls-ai/landfall-cli/internal/client"
)

func TestRoomNameOfReadsTheFrameOnOneLine(t *testing.T) {
	n := RoomNameOf(&client.ContextFrame{Incident: client.Incident{DisplayID: " Acme\t42 ", Title: "Checkout\n5xx spike"}})
	if n.DisplayID != "Acme 42" || n.Title != "Checkout 5xx spike" {
		t.Fatalf("name = %+v", n)
	}
	if !RoomNameOf(nil).IsZero() || !RoomNameOf(&client.ContextFrame{}).IsZero() {
		t.Fatal("a frame with no name must name nothing")
	}
}

func TestRenderFrameLeadsWithTheDisplayIDWhenTheServerSendsOne(t *testing.T) {
	got := RenderFrame(&client.ContextFrame{Incident: client.Incident{DisplayID: "Acme 42", Title: "Checkout 5xx", Severity: "sev2"}})
	if !strings.HasPrefix(got, "Acme 42 · Checkout 5xx · sev2") {
		t.Fatalf("got %q", got)
	}
	plain := RenderFrame(&client.ContextFrame{Incident: client.Incident{Title: "Checkout 5xx"}})
	if !strings.HasPrefix(plain, "Checkout 5xx") {
		t.Fatalf("without a display id the head is unchanged, got %q", plain)
	}
}

// TestATitleCannotDriveThePersonsTerminal: an incident title is the server's
// (an alert payload's, any member's) and it is printed on the status line,
// which Claude Code renders with ANSI, and by `landfall rooms`. Escape
// sequences in it must reach neither.
func TestATitleCannotDriveThePersonsTerminal(t *testing.T) {
	const (
		esc = "\x1b"
		csi = "\u009b" // the 8-bit CSI, a C1 control
		bel = "\x07"
		del = "\x7f"
	)
	n := RoomNameOf(&client.ContextFrame{Incident: client.Incident{
		DisplayID: "Acme" + esc + "[2J 42",
		Title:     "Checkout " + esc + "[31m5xx" + esc + "[0m " + esc + "]0;owned" + bel + "spike" + csi + "1A" + del + "\x00\tnow",
	}})
	for _, s := range []string{n.DisplayID, n.Title, n.StatusHead(), RoomName{Title: n.Title}.StatusHead()} {
		for _, r := range s {
			if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
				t.Fatalf("control character %U survived in %q", r, s)
			}
		}
	}
	if n.DisplayID != "Acme[2J 42" {
		t.Errorf("display id = %q", n.DisplayID)
	}
	if n.Title != "Checkout [31m5xx[0m ]0;ownedspike1A now" {
		t.Errorf("title = %q: the words stay, only the controls go (a tab is a space)", n.Title)
	}

	// A name that never went through RoomNameOf (a state file, another
	// process's hook answer) is cleaned where it is printed.
	raw := RoomName{Title: "Checkout " + esc + "[2J5xx"}
	if head := raw.StatusHead(); strings.ContainsRune(head, 0x1b) {
		t.Fatalf("StatusHead printed ESC: %q", head)
	}
	if got := Printable("a\xffb"); got != "a�b" {
		t.Errorf("invalid UTF-8 = %q, want the replacement character", got)
	}
}
