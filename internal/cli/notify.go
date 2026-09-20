package cli

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/narrate"
)

// notifier taps the person on the shoulder for the ONE kind of room event that
// deserves it: a message addressed to someone by name. Everything else stays
// in the status line, which the person reads when they look up; a directed
// question is the case where they would want to be looked at.
//
// It is passive by construction: an OS notification, never a line in the
// agent's transcript, never a wake of the model. It is also best-effort and
// off the hot path — a missing `osascript`, a headless box, or LANDFALL_NOTIFY=0
// all mean silence, never an error anyone sees.
//
// Rate-limited to one per `minGap`: a room where several people are @-ing
// each other should not turn into a notification storm.
type notifier struct {
	mu      sync.Mutex
	last    time.Time
	minGap  time.Duration
	run     func(title, body string)
	enabled bool
}

func newNotifier() *notifier {
	n := &notifier{minGap: 10 * time.Second, enabled: os.Getenv("LANDFALL_NOTIFY") != "0"}
	switch runtime.GOOS {
	case "darwin":
		n.run = func(title, body string) {
			script := "display notification " + appleQuote(body) + " with title " + appleQuote(title)
			_ = exec.Command("osascript", "-e", script).Run()
		}
	default:
		if _, err := exec.LookPath("notify-send"); err == nil {
			n.run = func(title, body string) { _ = exec.Command("notify-send", title, body).Run() }
		}
	}
	return n
}

// isAddressed: a human's chat message that names someone. The session does not
// know the person's own display name, so any @-mention counts — a message
// addressed to a teammate is still the room talking to a person, and the cost
// of a notification the person did not need is one glance.
func isAddressed(evt client.Event) bool {
	if evt.Type != "chat.message" {
		return false
	}
	return strings.Contains(narrate.EventText(evt.Payload), "@")
}

// Maybe notifies for `evt` when it is addressed and the gap since the last
// notification allows. Returns true when a notification was sent.
func (n *notifier) Maybe(evt client.Event) bool {
	if n == nil || !n.enabled || n.run == nil || !isAddressed(evt) {
		return false
	}
	n.mu.Lock()
	if time.Since(n.last) < n.minGap {
		n.mu.Unlock()
		return false
	}
	n.last = time.Now()
	n.mu.Unlock()
	who := narrate.EventActor(evt.Payload)
	title := "Landfall war room"
	if who != "" {
		title = who + " in the war room"
	}
	body := narrate.EventText(evt.Payload)
	if len([]rune(body)) > 140 {
		body = string([]rune(body)[:139]) + "…"
	}
	go n.run(title, body)
	return true
}

func appleQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
