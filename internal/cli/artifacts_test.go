package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/daemon"
)

func openPath(id string) string { return "/o/acme/incidents/inc-1/artifacts/" + id + "/open" }

// shared is one artifact.shared row as core-api appends it.
func shared(seq int, id, name, ct string, size int, sha, who, display, kind, label string) map[string]any {
	p := map[string]any{
		"artifactId": id, "filename": name, "contentType": ct, "size": size, "sha256": sha,
		"safeRenderMode": "text", "displayName": display, "initials": "XX", "kind": kind,
		"humanActorId": who, "at": "2026-10-08T15:00:00Z",
	}
	if label != "" {
		p["edgeAgentLabel"] = label
	}
	return map[string]any{"seq": seq, "type": "artifact.shared", "payload": p, "occurredAt": "2026-10-08T15:00:01Z"}
}

func artifactEvents() []map[string]any {
	return []map[string]any{
		{"seq": 1, "type": "chat.message", "payload": map[string]any{"text": "hi"}},
		shared(3, "a1", "runbook.md", "text/markdown", 4096, "sha-x", "h-bob", "bob", "agent", "Claude Code"),
		shared(5, "a2", "screenshot.png", "image/png", 1200000, "sha-y", "h-alice", "alice", "member", ""),
		// The same bytes again, by somebody else: one entry, the first sharing.
		shared(7, "a3", "runbook-copy.md", "text/markdown", 4096, "sha-x", "h-carol", "carol", "member", ""),
		shared(9, "a4", "notes.txt", "text/plain", 30, "", "h-alice", "alice", "member", ""),
	}
}

// artifactDeps is f.deps plus the daemon's read-raw op over a real client.
func (f *fakeLandfall) artifactDeps() ArtifactDeps {
	return ArtifactDeps{
		ReadDeps: f.deps(true),
		RawRead: func(ctx context.Context, roomKey, path string) ([]byte, string, error) {
			if _, ok := daemon.RawReadPathAllowed(path); !ok {
				f.t.Fatalf("the daemon's read-raw op would refuse %q", path)
			}
			if roomKey != "rk1" {
				return nil, "", &client.HTTPError{Path: path, Status: 404}
			}
			cl := client.New(client.Config{BaseURL: f.srv.URL, Slug: "acme", IncidentID: "inc-1", Token: roomToken}, nil)
			return cl.OpenArtifact(ctx, strings.TrimSuffix(strings.TrimPrefix(path, "/artifacts/"), "/open"))
		},
	}
}

func (f *fakeLandfall) serveFile(id, ct string, body []byte) {
	f.handle("GET "+openPath(id), func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("authorization") != "Bearer "+roomToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.RawQuery != "" {
			f.t.Errorf("a person's read of a file carries no query: %q", r.URL.RawQuery)
		}
		w.Header().Set("content-type", ct)
		_, _ = w.Write(body)
	})
}

func TestArtifactsNewestFirstWithIdenticalBytesCollapsed(t *testing.T) {
	f := newFakeLandfall(t)
	f.serveEvents(eventsPath, artifactEvents())
	ans := roundTrip(t, RunArtifacts(context.Background(), "rk1", f.deps(false)))
	if ans["ok"] != true {
		t.Fatalf("answer: %v", ans)
	}
	arts := ans["artifacts"].([]any)
	if len(arts) != 3 {
		t.Fatalf("artifacts: %v", arts)
	}
	var ids []string
	for _, a := range arts {
		ids = append(ids, a.(map[string]any)["artifactId"].(string))
	}
	if strings.Join(ids, ",") != "a4,a2,a1" {
		t.Fatalf("order = %v, want newest first with the duplicate bytes collapsed to the first sharing", ids)
	}
	first := arts[2].(map[string]any)
	if first["filename"] != "runbook.md" || first["contentType"] != "text/markdown" || first["size"] != float64(4096) || first["sharedAt"] != "2026-10-08T15:00:00Z" {
		t.Fatalf("row: %v", first)
	}
	sharer := first["sharer"].(map[string]any)
	if sharer["displayName"] != "bob" || sharer["kind"] != "agent" || sharer["edgeAgentLabel"] != "Claude Code" || sharer["humanActorId"] != "h-bob" {
		t.Fatalf("sharer: %v", sharer)
	}
	if s := arts[1].(map[string]any)["sharer"].(map[string]any); s["edgeAgentLabel"] != "" {
		t.Fatalf("a person with no agent label: %v", s)
	}
	// Read with the room's session, no sign-in needed.
	for _, c := range f.allCalls() {
		if c.Token != roomToken {
			t.Fatalf("a call used %q: %+v", c.Token, c)
		}
	}
}

func TestArtifactsEmptyAndErrors(t *testing.T) {
	f := newFakeLandfall(t)
	f.serveEvents(eventsPath, []map[string]any{{"seq": 0, "type": "incident.opened", "payload": map[string]any{}}})
	ans := roundTrip(t, RunArtifacts(context.Background(), "", f.deps(false)))
	if ans["ok"] != true || len(ans["artifacts"].([]any)) != 0 {
		t.Fatalf("empty: %v", ans)
	}
	wantFailure(t, RunArtifacts(context.Background(), "", noRoomDeps()), msgNoRoom)
	g := newFakeLandfall(t)
	g.status("GET "+eventsPath, http.StatusUnauthorized)
	wantFailure(t, RunArtifacts(context.Background(), "rk1", g.deps(false)), msgRoomExpired)
}

func TestArtifactTextIsCutAtMaxChars(t *testing.T) {
	f := newFakeLandfall(t)
	f.serveEvents(eventsPath, artifactEvents())
	// 25,000 characters, multi-byte, so characters and bytes differ.
	text := strings.Repeat("é", 25000)
	f.serveFile("a1", "text/markdown; charset=utf-8", []byte(text))
	d := f.artifactDeps()

	ans := roundTrip(t, RunArtifact(context.Background(), ArtifactOptions{Room: "rk1", ArtifactID: "a1"}, d))
	if ans["ok"] != true || ans["artifactId"] != "a1" || ans["filename"] != "runbook.md" || ans["contentType"] != "text/markdown" {
		t.Fatalf("answer: %v", ans)
	}
	if got := []rune(ans["text"].(string)); len(got) != 20000 {
		t.Fatalf("text = %d characters, want the default 20000", len(got))
	}
	if ans["totalChars"] != float64(25000) || ans["truncated"] != true || ans["size"] != float64(len(text)) {
		t.Fatalf("totals: %v %v %v", ans["totalChars"], ans["truncated"], ans["size"])
	}
	if _, has := ans["binary"]; has {
		t.Fatal("a text file is not binary")
	}
	if _, has := ans["base64"]; has {
		t.Fatal("no bytes without --bytes")
	}

	ans = roundTrip(t, RunArtifact(context.Background(), ArtifactOptions{Room: "rk1", ArtifactID: "a1", MaxChars: 4000}, d))
	if len([]rune(ans["text"].(string))) != 4000 || ans["truncated"] != true {
		t.Fatalf("--max-chars 4000: %d %v", len([]rune(ans["text"].(string))), ans["truncated"])
	}

	// A short file is whole.
	f.serveFile("a4", "text/plain", []byte("origin pool notes"))
	ans = roundTrip(t, RunArtifact(context.Background(), ArtifactOptions{Room: "rk1", ArtifactID: "a4"}, d))
	if ans["text"] != "origin pool notes" || ans["totalChars"] != float64(17) || ans["truncated"] != false {
		t.Fatalf("short: %v", ans)
	}
	for _, c := range f.callsTo(openPath("a1")) {
		if c.Token != roomToken || c.Query != "" {
			t.Fatalf("the file must be read with the room's session and no agent instance: %+v", c)
		}
	}
}

func TestArtifactBinaryAndTheBytesCap(t *testing.T) {
	f := newFakeLandfall(t)
	f.serveEvents(eventsPath, artifactEvents())
	png := []byte{0x89, 'P', 'N', 'G', 0, 1, 2, 3}
	f.serveFile("a2", "image/png", png)
	d := f.artifactDeps()

	ans := roundTrip(t, RunArtifact(context.Background(), ArtifactOptions{Room: "rk1", ArtifactID: "a2"}, d))
	if ans["ok"] != true || ans["binary"] != true || ans["contentType"] != "image/png" || ans["size"] != float64(len(png)) {
		t.Fatalf("binary: %v", ans)
	}
	for _, k := range []string{"text", "totalChars", "truncated", "base64"} {
		if _, has := ans[k]; has {
			t.Fatalf("a binary answer has %s: %v", k, ans)
		}
	}

	ans = roundTrip(t, RunArtifact(context.Background(), ArtifactOptions{Room: "rk1", ArtifactID: "a2", Bytes: true}, d))
	got, err := base64.StdEncoding.DecodeString(ans["base64"].(string))
	if err != nil || !bytes.Equal(got, png) {
		t.Fatalf("--bytes: %v %v", got, err)
	}

	// Over 256 KB: described, never handed over.
	big := bytes.Repeat([]byte{7}, artifactBytesMax+1)
	f.serveFile("a2", "image/png", big)
	ans = roundTrip(t, RunArtifact(context.Background(), ArtifactOptions{Room: "rk1", ArtifactID: "a2", Bytes: true}, d))
	if _, has := ans["base64"]; has || ans["binary"] != true || ans["size"] != float64(len(big)) {
		t.Fatalf("over the cap: size %v base64 present %v", ans["size"], ans["base64"] != nil)
	}
	// Exactly the cap still goes.
	f.serveFile("a2", "image/png", big[:artifactBytesMax])
	ans = roundTrip(t, RunArtifact(context.Background(), ArtifactOptions{Room: "rk1", ArtifactID: "a2", Bytes: true}, d))
	if _, has := ans["base64"]; !has {
		t.Fatal("a file of exactly 256 KB is handed over")
	}
}

func TestArtifactErrors(t *testing.T) {
	ctx := context.Background()
	f := newFakeLandfall(t)
	f.serveEvents(eventsPath, artifactEvents())
	d := f.artifactDeps()

	wantFailure(t, RunArtifact(ctx, ArtifactOptions{Room: "rk1"}, d), "Name the artifact to read: landfall artifact <artifactId>.")
	wantFailure(t, RunArtifact(ctx, ArtifactOptions{Room: "rk1", ArtifactID: "../events"}, d), "That is not an artifact id.")
	wantFailure(t, RunArtifact(ctx, ArtifactOptions{Room: "rk1", ArtifactID: "nope"}, d), "This room has no artifact nope.")
	if len(f.callsTo(openPath("nope"))) != 0 {
		t.Fatal("an artifact the room never shared is not fetched")
	}

	// The collapsed duplicate is still readable by its own id.
	f.serveFile("a3", "text/markdown", []byte("same bytes"))
	if ans := RunArtifact(ctx, ArtifactOptions{Room: "rk1", ArtifactID: "a3"}, d); ans["ok"] != true || ans["filename"] != "runbook-copy.md" {
		t.Fatalf("duplicate: %v", ans)
	}

	f.status("GET "+openPath("a1"), http.StatusUnauthorized)
	wantFailure(t, RunArtifact(ctx, ArtifactOptions{Room: "rk1", ArtifactID: "a1"}, d), msgRoomExpired)
	f.status("GET "+openPath("a1"), http.StatusNotFound)
	wantFailure(t, RunArtifact(ctx, ArtifactOptions{Room: "rk1", ArtifactID: "a1"}, d), "Landfall no longer has the file for runbook.md.")

	old := d
	old.RawRead = func(context.Context, string, string) ([]byte, string, error) { return nil, "", errDaemonTooOld }
	wantFailure(t, RunArtifact(ctx, ArtifactOptions{Room: "rk1", ArtifactID: "a1"}, old), "The room daemon is older than this command. Run landfall daemon stop, then try again.")
	gone := d
	gone.RawRead = func(context.Context, string, string) ([]byte, string, error) {
		return nil, "", errors.New("the room daemon did not answer: dial")
	}
	wantFailure(t, RunArtifact(ctx, ArtifactOptions{Room: "rk1", ArtifactID: "a1"}, gone), msgNoRoom)

	wantFailure(t, RunArtifact(ctx, ArtifactOptions{ArtifactID: "a1"}, ArtifactDeps{ReadDeps: noRoomDeps()}), msgNoRoom)
}

// TestArtifactCommandsAnswerOneJSONLine runs both commands through Cobra with
// no room daemon: one JSON line, ok:false with a sentence, exit 0.
func TestArtifactCommandsAnswerOneJSONLine(t *testing.T) {
	rt, err := os.MkdirTemp("/tmp", "lfa")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(rt) })
	t.Setenv("XDG_RUNTIME_DIR", rt)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LANDFALL_SLUG", "")
	t.Setenv("LANDFALL_DAEMON", "0")
	for _, args := range [][]string{
		{"artifacts", "--room", "rk1", "--host", "claude-code"},
		{"artifact", "a1", "--room", "rk1", "--max-chars", "4000", "--bytes", "--surprise"},
		{"wall", "--room", "rk1", "--person", "me"},
	} {
		t.Run(args[0], func(t *testing.T) {
			var out bytes.Buffer
			prev := stdout
			stdout = &out
			defer func() { stdout = prev }()
			code := run(&UI{Out: &out, Err: io.Discard}, args, func(string) string { return "" })
			lines := strings.Split(strings.TrimSpace(out.String()), "\n")
			if code != 0 || len(lines) != 1 {
				t.Fatalf("exit %d, out %q", code, out.String())
			}
			var ans map[string]any
			if err := json.Unmarshal([]byte(lines[0]), &ans); err != nil {
				t.Fatalf("not JSON: %q", lines[0])
			}
			if ans["ok"] != false {
				t.Fatalf("answer: %v", ans)
			}
			assertPlainSentence(t, ans["error"].(string))
		})
	}
}
