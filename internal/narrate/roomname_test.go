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
