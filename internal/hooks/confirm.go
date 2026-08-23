// confirm.go — the one-keystroke confirmation a matched command must get before
// anything leaves the machine (#233). A Go port of `src/hooks/confirm.mjs`.
//
// ── Why /dev/tty and not stdin ────────────────────────────────────────────
// A hook process is spawned by the agent harness with the event payload on
// stdin, so stdin is already spoken for and is not a terminal. `/dev/tty` is the
// controlling terminal of the process group — the engineer's actual keyboard —
// which is the only channel that can carry a prompt the human sees while their
// agent is mid-turn.
//
// ── Every failure mode declines ───────────────────────────────────────────
// No controlling terminal (CI, a headless daemon, a harness that detaches the
// process group), a non-terminal fd, a write error, or silence past the timeout
// all resolve to false. The acceptance criterion is "nothing leaves without the
// confirm", so the absence of a human is not a soft case to be lenient about —
// it is the clearest possible "no". The default answer to the prompt itself is
// likewise no: `y` confirms, and every other key, including Enter, declines.
//
// The timeout exists because this prompt sits in front of a command the engineer
// is trying to run. A hook that waits forever for a keystroke nobody is there to
// press would hang their shell, which would make the first experience of this
// feature a reason to uninstall it.
package hooks

import (
	"os"
	"strings"
	"time"
	"unicode"

	"golang.org/x/term"
)

// ConfirmTimeout is how long to wait for a keystroke before declining.
const ConfirmTimeout = 30 * time.Second

// ConfirmOptions configures ConfirmOnTTY. The zero value is the production one.
type ConfirmOptions struct {
	// Timeout is the silence budget. Zero means ConfirmTimeout.
	Timeout time.Duration
	// TTYPath is the terminal to ask on. Empty means "/dev/tty".
	TTYPath string
}

// ConfirmOnTTY asks on the controlling terminal and answers true only on an
// explicit `y`.
//
// `question` is rendered as-is; the caller owns the wording.
func ConfirmOnTTY(question string, opts ConfirmOptions) bool {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = ConfirmTimeout
	}
	ttyPath := opts.TTYPath
	if ttyPath == "" {
		ttyPath = "/dev/tty"
	}

	f, err := os.OpenFile(ttyPath, os.O_RDWR, 0)
	if err != nil {
		return false // no controlling terminal — nobody to ask, so: no
	}
	defer func() { _ = f.Close() }()

	fd := int(f.Fd())
	if !term.IsTerminal(fd) {
		return false // the fd is not a terminal (a redirected /dev/tty in CI)
	}

	if _, err := f.WriteString(question + " [y/N] "); err != nil {
		return false
	}

	// Raw mode is what makes this a SINGLE keystroke rather than a line the
	// human has to press Enter after. A terminal that refuses raw mode still
	// works line-buffered, exactly as the Node original's catch allows.
	if oldState, err := term.MakeRaw(fd); err == nil {
		defer func() { _ = term.Restore(fd, oldState) }()
	}

	answer := readKeystroke(f, timeout)

	// Raw mode swallows the echo, so the answer is written back explicitly. In
	// raw mode a bare "\n" does not return the cursor, hence "\r\n".
	if answer {
		_, _ = f.WriteString("yes\r\n")
	} else {
		_, _ = f.WriteString("no\r\n")
	}
	return answer
}

// readKeystroke waits up to `timeout` for one key and reports whether it was a
// `y`.
//
// The wait is bounded two independent ways, because neither alone is portable:
// a read deadline on the tty fd (which unblocks the read itself where the
// runtime can poll a character device) and a select on a timer (which always
// bounds the CALLER). The reading goroutine may outlive this function; it holds
// nothing that would delay process exit, which is the Go equivalent of the Node
// original's `timer.unref()` — a hook whose work is finished must never sit
// alive waiting for a key nobody is going to press.
func readKeystroke(f *os.File, timeout time.Duration) bool {
	type result struct {
		key string
		ok  bool
	}
	ch := make(chan result, 1)

	_ = f.SetReadDeadline(time.Now().Add(timeout)) // best-effort
	go func() {
		buf := make([]byte, 8)
		n, err := f.Read(buf)
		if err != nil || n == 0 {
			ch <- result{}
			return
		}
		ch <- result{key: string(buf[:n]), ok: true}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case r := <-ch:
		if !r.ok {
			return false // read error, or the terminal went away
		}
		return isYes(r.key)
	case <-timer.C:
		return false // silence: the clearest possible "no"
	}
}

// isYes is `chunk.toString('utf8').trim().slice(0, 1).toLowerCase() === 'y'` —
// so a bare Enter trims to "" and declines, and a stray leading space before a
// `y` still confirms.
func isYes(chunk string) bool {
	trimmed := strings.TrimSpace(chunk)
	if trimmed == "" {
		return false
	}
	return unicode.ToLower([]rune(trimmed)[0]) == 'y'
}
