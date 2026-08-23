// input.go — read the JSON payload a host writes to a hook's stdin. A Go port
// of `src/hooks/input.mjs`.
//
// Claude Code and Codex both hand a lifecycle hook its context this way, and for
// `stop` that payload carries the loop guard (`stop_hook_active`). But a hook
// must also survive being run by hand, by a host that sends nothing, or with
// stdin left open — so this NEVER blocks indefinitely: no stdin, a TTY, or
// silence past the deadline all resolve to "" and the caller treats it as a
// first attempt.
//
// READ ONCE. The hook entrypoint (`landfall hooks <event>`) calls this exactly
// once and passes the result to every handler through HookDeps.Input. A handler
// that reads stdin itself would race this read for the same bytes — which is
// why RunHookEvent takes the string rather than the stream.
package hooks

import (
	"io"
	"os"
	"sync"
	"time"

	"golang.org/x/term"
)

// StdinDeadline bounds the one stdin read.
const StdinDeadline = 250 * time.Millisecond

// stdinMax caps the payload. A hook payload is not a stream.
const stdinMax = 1_000_000

// ReadHookInput performs the one bounded read of the host's event payload.
//
// A nil reader, a terminal, or silence past the deadline all answer "" — never
// an error, and never a block. A zero timeout means StdinDeadline.
//
// The read runs on its own goroutine and whatever it has accumulated by the
// deadline is returned, so a host that writes a partial payload and then stalls
// still yields what it managed to send. The goroutine may outlive this call; it
// holds nothing open that would delay process exit, which is the Go equivalent
// of Node's `timer.unref()`.
func ReadHookInput(r io.Reader, timeout time.Duration) string {
	if r == nil {
		return ""
	}
	if f, ok := r.(*os.File); ok {
		if f == nil {
			return ""
		}
		if term.IsTerminal(int(f.Fd())) {
			return "" // run by hand at a prompt: there is no payload coming
		}
	}
	if timeout <= 0 {
		timeout = StdinDeadline
	}

	var (
		mu   sync.Mutex
		buf  []byte
		done = make(chan struct{})
	)
	go func() {
		defer close(done)
		chunk := make([]byte, 32*1024)
		for {
			n, err := r.Read(chunk)
			if n > 0 {
				mu.Lock()
				buf = append(buf, chunk[:n]...)
				over := len(buf) > stdinMax
				mu.Unlock()
				if over {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}

	mu.Lock()
	defer mu.Unlock()
	return string(buf)
}
