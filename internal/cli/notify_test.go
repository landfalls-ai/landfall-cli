package cli

import (
	"sync"
	"testing"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
)

func TestNotifierTapsOnlyForAddressedChatAndRateLimits(t *testing.T) {
	var mu sync.Mutex
	var sent []string
	n := &notifier{minGap: time.Hour, enabled: true, run: func(title, body string) {
		mu.Lock()
		sent = append(sent, title+": "+body)
		mu.Unlock()
	}}
	plumbing := client.Event{Type: "agent.query"}
	finding := client.Event{Type: "edge.finding", Payload: map[string]any{"text": "p99 spiked"}}
	addressed := client.Event{Type: "chat.message", Payload: map[string]any{"text": "@alice can you confirm?", "displayName": "bob"}}
	unaddressed := client.Event{Type: "chat.message", Payload: map[string]any{"text": "rolling back now"}}
	if n.Maybe(plumbing) || n.Maybe(finding) || n.Maybe(unaddressed) {
		t.Fatal("only a message addressed to someone deserves a notification")
	}
	if !n.Maybe(addressed) {
		t.Fatal("an @-mention must notify")
	}
	if n.Maybe(addressed) {
		t.Fatal("a second @-mention inside the gap must be rate-limited")
	}
	time.Sleep(20 * time.Millisecond) // let the goroutine run
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 1 || sent[0] != "bob in the war room: @alice can you confirm?" {
		t.Fatalf("sent = %v", sent)
	}
}

func TestNotifierIsSilentWhenDisabledOrUnsupported(t *testing.T) {
	addressed := client.Event{Type: "chat.message", Payload: map[string]any{"text": "@alice ping"}}
	off := &notifier{minGap: time.Second, enabled: false, run: func(string, string) { t.Fatal("must not run") }}
	if off.Maybe(addressed) {
		t.Fatal("LANDFALL_NOTIFY=0 must silence it")
	}
	noBackend := &notifier{minGap: time.Second, enabled: true}
	if noBackend.Maybe(addressed) {
		t.Fatal("no OS backend means silence, not an error")
	}
	var nilN *notifier
	if nilN.Maybe(addressed) {
		t.Fatal("a nil notifier is silent")
	}
}
