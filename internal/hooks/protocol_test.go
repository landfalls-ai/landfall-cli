package hooks

// protocol_test.go — layer 1 of `test/hooks/cursor-adapter.test.mjs` (#228,
// story #190): the rendering, per protocol, pure.
//
// Cursor is the one host whose hook contract is not the exit-2 convention the
// other two share, and the failure mode of getting it wrong is silent: an entry
// sits in the user's config, fires on every turn, and its verdict is discarded
// (or, worse, its empty stdout is a JSON parse error). So these assert the WIRE.

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAHostIsMappedToAProtocolAndAnythingUnknownFallsBackToExit2(t *testing.T) {
	if got := ProtocolForHost("cursor"); got != CURSOR_JSON {
		t.Fatalf("cursor → %q", got)
	}
	for _, host := range []string{"claude-code", "codex"} {
		if got := ProtocolForHost(host); got != EXIT2 {
			t.Fatalf("%s → %q", host, got)
		}
	}
	// A hook runs on every turn; an unrecognized --host must not be fatal.
	if got := ProtocolForHost("windsurf-someday"); got != EXIT2 {
		t.Fatalf("unknown host → %q", got)
	}
	if got := ProtocolForHost(""); got != EXIT2 {
		t.Fatalf("absent host → %q", got)
	}
}

func TestExit2BlockIsExit2PlusStderrAndAllowIsSilence(t *testing.T) {
	blocked := RenderStopVerdict(EXIT2, true, "read the room")
	if blocked.ExitCode != 2 || blocked.Channel != ChannelStderr || blocked.Text != "read the room\n" {
		t.Fatalf("got %+v", blocked)
	}
	allowed := RenderStopVerdict(EXIT2, false, "")
	if allowed.ExitCode != 0 || allowed.Text != "" {
		t.Fatalf("silence is a valid answer under exit2 and the one we already ship: %+v", allowed)
	}
}

func TestCursorJSONBlockIsAFollowupMessageAllowIsEmptyObjectAndBothExitZero(t *testing.T) {
	blocked := RenderStopVerdict(CURSOR_JSON, true, "read the room")
	if blocked.Channel != ChannelStdout {
		t.Fatalf("channel %q", blocked.Channel)
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(blocked.Text), &body); err != nil {
		t.Fatalf("stdout must be one parseable JSON object: %v", err)
	}
	if body["followup_message"] != "read the room" {
		t.Fatalf("got %v", body)
	}
	if blocked.ExitCode != 0 {
		t.Fatal(`non-zero means "the hook failed", not "the hook objected"`)
	}

	allowed := RenderStopVerdict(CURSOR_JSON, false, "")
	if allowed.ExitCode != 0 {
		t.Fatalf("exit %d", allowed.ExitCode)
	}
	if strings.TrimSpace(allowed.Text) != "{}" {
		t.Fatalf("empty stdout is a parse error, not an allow: %q", allowed.Text)
	}
	if RenderNoOp(CURSOR_JSON).Text != allowed.Text {
		t.Fatal("the no-op and the allow must be the same bytes")
	}
}

func TestCursorJSONOutputIsExactlyOneJSONObjectAndNothingElse(t *testing.T) {
	text := RenderStopVerdict(CURSOR_JSON, true, "line one\nline two").Text
	if got := len(strings.Split(strings.TrimRight(text, "\n"), "\n")); got != 1 {
		t.Fatalf("a multi-line reason must not become %d lines of stdout", got)
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(text), &body); err != nil {
		t.Fatal(err)
	}
	if body["followup_message"] != "line one\nline two" {
		t.Fatalf("got %q", body["followup_message"])
	}
}

func TestSpeaksOnSilenceIsTrueOnlyForTheHostThatParsesStdoutEveryTime(t *testing.T) {
	if !SpeaksOnSilence(CURSOR_JSON) {
		t.Fatal("cursor-json must speak even to say nothing")
	}
	if SpeaksOnSilence(EXIT2) {
		t.Fatal("exit2's allow path is silence")
	}
}

func TestSpecAndDispatchShareOneHostTable(t *testing.T) {
	// The collapse T045 exists for: HookCommand's `--host` decision and the
	// no-op's shape must have exactly ONE answer, or a registered command string
	// and the handler that answers it will drift.
	for _, host := range []string{"claude-code", "codex", "cursor", "not-a-host", ""} {
		wantFlag := ProtocolForHost(host) != EXIT2
		if got := hookCommandNeedsHostFlag(host); got != wantFlag {
			t.Fatalf("%q: hookCommandNeedsHostFlag = %v, ProtocolForHost = %q", host, got, ProtocolForHost(host))
		}
	}
}
