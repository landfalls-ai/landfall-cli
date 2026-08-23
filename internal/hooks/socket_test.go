package hooks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/client"
	"github.com/landfalls-ai/landfall-cli/internal/session"
)

// tempWorkspace builds an isolated workspace whose runtime directory is SHORT.
//
// Deliberately under /tmp rather than t.TempDir(): on macOS the default $TMPDIR
// is /var/folders/<…>/T/, and `<base>/landfall/<16-char key>/<pid>.sock` costs
// ~35 bytes beyond the base, which puts a real socket path over the 104-byte
// sockaddr_un.sun_path limit. That is exactly the failure 14 of the Node suite's
// 358 tests hit; there is no reason to reproduce it here.
func tempWorkspace(t *testing.T) Workspace {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "lf-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return Workspace{Cwd: root, Env: map[string]string{"XDG_RUNTIME_DIR": root}}
}

// --- the fake session -------------------------------------------------------

type fakeSession struct {
	pending    []client.Event
	dropped    int
	cursor     int64
	attention  *client.Attention
	divergence *client.Divergence
	cfg        *client.Config
	panics     bool
}

func (f *fakeSession) Pending() []client.Event {
	if f.panics {
		panic("pending exploded")
	}
	return f.pending
}
func (f *fakeSession) PendingDropped() int            { return f.dropped }
func (f *fakeSession) Cursor() int64                  { return f.cursor }
func (f *fakeSession) Attention() *client.Attention   { return f.attention }
func (f *fakeSession) Divergence() *client.Divergence { return f.divergence }
func (f *fakeSession) Client() session.EdgeClient {
	if f.cfg == nil {
		return nil
	}
	return client.New(*f.cfg, nil)
}

// settlingSession is a fakeSession that also offers the OPTIONAL
// AttentionSettler half of the #252 fix.
type settlingSession struct {
	*fakeSession
	settled int
	next    *client.Attention
}

func (s *settlingSession) SettleAttention(_ context.Context, _ time.Duration) *client.Attention {
	s.settled++
	s.attention = s.next
	return s.next
}

func seq(n int64) *int64 { return &n }

func joined(incidentID, slug string) *client.Config {
	return &client.Config{IncidentID: incidentID, Slug: slug}
}

// --- WorkspaceKey (FR-007: byte-identical to Node) --------------------------

func TestWorkspaceKeyMatchesTheNodeFormulaForKnownInputs(t *testing.T) {
	// Computed independently of this implementation: `shasum -a 256` over the
	// literal bytes, and Node's own
	// `createHash('sha256').update(x).digest('hex').slice(0,16)`. Both agree.
	//
	// A Go `landfall hooks stop` must find a Node `landfall serve`'s socket
	// during the transition, so this pair is the contract, not a smoke test.
	cases := map[string]string{
		// Deliberately unresolvable paths, so both implementations take the
		// fallback and hash the raw string — the same bytes on both sides, with
		// nothing about the local filesystem able to change the answer.
		// Cross-checked two independent ways: `shasum -a 256 | cut -c1-16`, and
		// Node's own `workspaceKey` body run against the same input.
		"/landfall-fixture/does-not-exist": "dad3c14f0967442b",
		"/nonexistent/landfall/workspace":  "f6849cf2a6fb92b5",
	}
	for in, want := range cases {
		if got := WorkspaceKey(in); got != want {
			t.Fatalf("WorkspaceKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWorkspaceKeyIs16LowercaseHexCharsOfTheFullDigest(t *testing.T) {
	in := "/some/workspace/that/does/not/exist"
	sum := sha256.Sum256([]byte(in))
	full := hex.EncodeToString(sum[:])

	got := WorkspaceKey(in)
	if len(got) != 16 {
		t.Fatalf("key is %d chars, want 16", len(got))
	}
	// The trap the contract calls out: hex.EncodeToString(h[:8]) happens to give
	// the same 16 chars, but hex.EncodeToString(h[:])[:16] is the definition —
	// assert against the full digest so a future "simplification" is caught.
	if got != full[:16] {
		t.Fatalf("key = %q, want the first 16 chars of %q", got, full)
	}
	if strings.ToLower(got) != got {
		t.Fatalf("key %q is not lowercase hex", got)
	}
}

func TestWorkspaceKeyResolvesSymlinksSoTwoNamesForOneDirectoryPair(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "lf-key-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	real := filepath.Join(root, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if WorkspaceKey(link) != WorkspaceKey(real) {
		t.Fatal("a symlink and its target must resolve to the same workspace key")
	}
}

func TestWorkspaceKeyFallbackHashesTheRawInputUnmodified(t *testing.T) {
	// The fallback exists for paths that cannot be resolved, and Node hashes
	// `String(cwd)` AS GIVEN. A port that calls filepath.Clean here would make
	// these two inputs collide — diverging on exactly the inputs the fallback
	// exists for, and silently breaking cross-implementation socket discovery.
	a := WorkspaceKey("/no/such/dir/")
	b := WorkspaceKey("/no/such/dir")
	if a == b {
		t.Fatal("the fallback path must hash the raw input; a trailing separator must not be normalized away")
	}
	sum := sha256.Sum256([]byte("/no/such/dir/"))
	if a != hex.EncodeToString(sum[:])[:16] {
		t.Fatal("the fallback must hash exactly the bytes it was given")
	}
}

// --- the three verbs --------------------------------------------------------

func TestPeekCarriesTheAttentionSnapshotAlongsideTheQueue(t *testing.T) {
	s := &fakeSession{
		cfg:       joined("inc-1", "acme"),
		attention: &client.Attention{FlaggedOwnContext: []client.FlaggedContext{{State: "quarantined"}}},
	}
	res := HandleSocketRequest(PeekRequest(), s, HandleOptions{PID: 1}).(PeekResponse)
	if len(res.Attention.FlaggedOwnContext) != 1 {
		t.Fatal("peek must carry the attention snapshot")
	}
	if res.Count != 0 {
		t.Fatal("attention is a SEPARATE thing from what is queued")
	}
}

func TestPeekOnASessionWithNoAttentionAnswersNullNotAPanic(t *testing.T) {
	res := HandleSocketRequest(PeekRequest(), &fakeSession{}, HandleOptions{PID: 1}).(PeekResponse)
	if res.Attention != nil {
		t.Fatal("a serve process with no attention must answer null")
	}
	body, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	// Present-but-null, never omitted: an older serve process's absent field
	// already reads as "no blockers", and the two must agree.
	if !strings.Contains(string(body), `"attention":null`) {
		t.Fatalf("attention must be present as null, got %s", body)
	}
}

func TestPeekNamesTheIncidentSoAHookCanTellTwoRoomsInOneCheckoutApart(t *testing.T) {
	s := &fakeSession{cfg: joined("inc-1", "acme")}
	res := HandleSocketRequest(PeekRequest(), s, HandleOptions{PID: 1}).(PeekResponse)
	if res.IncidentID != "inc-1" {
		t.Fatalf("incidentId = %q", res.IncidentID)
	}
}

func TestPeekReportsTheQueueCursorAndMaxSeq(t *testing.T) {
	s := &fakeSession{
		cursor:  4,
		dropped: 3,
		pending: []client.Event{
			{Seq: seq(5), Type: "edge.finding"},
			{Seq: seq(9), Type: "edge.finding"},
		},
	}
	res := HandleSocketRequest(PeekRequest(), s, HandleOptions{PID: 1}).(PeekResponse)
	if res.Count != 2 || res.Cursor != 4 || res.MaxSeq != 9 || res.Dropped != 3 {
		t.Fatalf("unexpected peek: %+v", res)
	}
	if len(res.Digest) != 2 {
		t.Fatalf("digest has %d lines, want 2", len(res.Digest))
	}
}

func TestPeekMaxSeqFallsBackToTheCursorWhenNothingIsQueued(t *testing.T) {
	res := HandleSocketRequest(PeekRequest(), &fakeSession{cursor: 7}, HandleOptions{PID: 1}).(PeekResponse)
	if res.MaxSeq != 7 {
		t.Fatalf("maxSeq = %d, want the cursor (7)", res.MaxSeq)
	}
}

func TestStatusCarriesAVotesAwaitedCount(t *testing.T) {
	s := &fakeSession{
		cfg:       joined("inc-1", "acme"),
		attention: &client.Attention{VotesAwaited: []client.VoteAwaited{{}, {}}},
	}
	res := HandleSocketRequest(StatusRequest(), s, HandleOptions{PID: 1}).(StatusResponse)
	// `landfall status` formats a number, not a raw list.
	if res.VotesAwaited != 2 {
		t.Fatalf("votesAwaited = %d, want 2", res.VotesAwaited)
	}
}

func TestStatusAnswersVotesAwaitedZeroWhenNoAttentionSnapshotExistsYet(t *testing.T) {
	res := HandleSocketRequest(StatusRequest(), &fakeSession{}, HandleOptions{PID: 1}).(StatusResponse)
	if res.VotesAwaited != 0 {
		t.Fatalf("votesAwaited = %d, want 0", res.VotesAwaited)
	}
}

func TestStatusIsAdditiveEveryPreT047FieldIsStillPresent(t *testing.T) {
	s := &fakeSession{cfg: joined("inc-1", "acme"), cursor: 3, dropped: 1}
	res := HandleSocketRequest(StatusRequest(), s, HandleOptions{PID: 1}).(StatusResponse)
	if res.IncidentID != "inc-1" || res.Slug != "acme" || !res.Connected {
		t.Fatalf("unexpected status: %+v", res)
	}
	if res.Cursor != 3 || res.Pending != 0 || res.Dropped != 1 {
		t.Fatalf("unexpected status: %+v", res)
	}
}

func TestStatusOnAnUnjoinedSessionReportsNotConnected(t *testing.T) {
	res := HandleSocketRequest(StatusRequest(), &fakeSession{}, HandleOptions{PID: 1}).(StatusResponse)
	if res.Connected || res.IncidentID != "" || res.Slug != "" {
		t.Fatalf("unexpected status: %+v", res)
	}
}

func TestStatusOmitsDivergenceEntirelyUntilARefreshHasLanded(t *testing.T) {
	s := &fakeSession{cfg: joined("inc-1", "acme")}
	body, err := json.Marshal(HandleSocketRequest(StatusRequest(), s, HandleOptions{PID: 1}))
	if err != nil {
		t.Fatal(err)
	}
	// Never a misleading default: absent, not null, not {diverging:false}.
	if strings.Contains(string(body), "divergence") {
		t.Fatalf("divergence must be omitted until one has landed, got %s", body)
	}
}

func TestStatusCarriesARealButFalseyLookingDivergenceAnswer(t *testing.T) {
	s := &fakeSession{cfg: joined("inc-1", "acme"), divergence: &client.Divergence{Diverging: false}}
	res := HandleSocketRequest(StatusRequest(), s, HandleOptions{PID: 1}).(StatusResponse)
	if res.Divergence == nil || res.Divergence.Diverging {
		t.Fatalf("a landed {diverging:false} must be carried verbatim, got %+v", res.Divergence)
	}
}

func TestStatusCarriesARealDivergingAnswerVerbatim(t *testing.T) {
	s := &fakeSession{
		cfg: joined("inc-1", "acme"),
		divergence: &client.Divergence{
			Diverging:          true,
			EstablishedSubject: "cli-handoff/redeem",
			ObservedSubject:    "cloudfront/5xxerrorrate",
		},
	}
	res := HandleSocketRequest(StatusRequest(), s, HandleOptions{PID: 1}).(StatusResponse)
	if !res.Divergence.Diverging || res.Divergence.EstablishedSubject != "cli-handoff/redeem" {
		t.Fatalf("unexpected divergence: %+v", res.Divergence)
	}
}

func TestConsumeRequiresANumericUpTo(t *testing.T) {
	for _, req := range []SocketRequest{
		{Op: "consume"},
		{Op: "consume", UpTo: "5"},
		{Op: "consume", UpTo: nil},
	} {
		res, ok := HandleSocketRequest(req, &fakeSession{}, HandleOptions{PID: 1}).(ErrorResponse)
		if !ok {
			t.Fatalf("%+v should have been refused", req)
		}
		if res.OK || res.Error != "consume requires a numeric upTo" || res.V != SocketProtocolVersion {
			t.Fatalf("unexpected refusal: %+v", res)
		}
	}
}

func TestConsumeMovesTheCursorThroughTheInjectedConsumeFunc(t *testing.T) {
	s := &fakeSession{cursor: 2}
	var sawUpTo int64
	opts := HandleOptions{PID: 1, Consume: func(_ SocketSession, upTo int64) int64 {
		sawUpTo = upTo
		return upTo
	}}
	res := HandleSocketRequest(ConsumeRequest(9), s, opts).(ConsumeResponse)
	if sawUpTo != 9 || res.Cursor != 9 || !res.OK {
		t.Fatalf("unexpected consume: upTo=%d res=%+v", sawUpTo, res)
	}
}

func TestConsumeWithoutAConsumeFuncReportsTheCursorUnchanged(t *testing.T) {
	// `socket.mjs`: `const cursor = consume ? consume(session, upTo) : session?.cursor`.
	res := HandleSocketRequest(ConsumeRequest(9), &fakeSession{cursor: 2}, HandleOptions{PID: 1}).(ConsumeResponse)
	if res.Cursor != 2 {
		t.Fatalf("cursor = %d, want 2 (unmoved)", res.Cursor)
	}
}

func TestUnknownOpIsRefusedWithoutAPIDField(t *testing.T) {
	res := HandleSocketRequest(SocketRequest{Op: "frobnicate"}, &fakeSession{}, HandleOptions{PID: 1})
	err, ok := res.(ErrorResponse)
	if !ok || err.OK || err.Error != "unknown op: frobnicate" {
		t.Fatalf("unexpected answer: %+v", res)
	}
	body, _ := json.Marshal(res)
	// The default branch deliberately carries no pid.
	if strings.Contains(string(body), `"pid"`) {
		t.Fatalf("the default branch must carry no pid field, got %s", body)
	}
}

func TestAMissingOpIsRefusedAsNone(t *testing.T) {
	res := HandleSocketRequest(SocketRequest{}, &fakeSession{}, HandleOptions{PID: 1}).(ErrorResponse)
	if res.Error != "unknown op: (none)" {
		t.Fatalf("error = %q", res.Error)
	}
}

func TestANonStringOpIsSpelledOutInTheRefusal(t *testing.T) {
	res := HandleSocketRequest(SocketRequest{Op: float64(123)}, &fakeSession{}, HandleOptions{PID: 1}).(ErrorResponse)
	if res.Error != "unknown op: 123" {
		t.Fatalf("error = %q, want the JS String(123) spelling", res.Error)
	}
}

func TestThereAreExactlyThreeVerbsAndNoFourth(t *testing.T) {
	// A guard, not a formality: this socket must never become a second way to
	// ACT in a war room. Anything not on the list is refused by construction.
	for _, op := range []string{"post", "join", "leave", "note", "token", "write"} {
		if _, ok := HandleSocketRequest(SocketRequest{Op: op}, &fakeSession{}, HandleOptions{}).(ErrorResponse); !ok {
			t.Fatalf("%q must not be a verb", op)
		}
	}
}

// --- peek's required settle (#252) -----------------------------------------

func TestAnswerSettlesAttentionBeforeAnsweringAPeek(t *testing.T) {
	// Without this, a Stop hook concludes against whatever snapshot the session
	// last happened to hold, and an agent whose last tool call predated a
	// quarantine concludes blocker-free — the exact failure the Stop tier exists
	// to prevent.
	stale := &client.Attention{}
	fresh := &client.Attention{FlaggedOwnContext: []client.FlaggedContext{{State: "quarantined"}}}
	s := &settlingSession{fakeSession: &fakeSession{attention: stale}, next: fresh}

	res := AnswerSocketRequest(context.Background(), PeekRequest(), s, HandleOptions{PID: 1}).(PeekResponse)
	if s.settled != 1 {
		t.Fatalf("settleAttention called %d times, want 1", s.settled)
	}
	if len(res.Attention.FlaggedOwnContext) != 1 {
		t.Fatal("peek must answer against the SETTLED snapshot, not the stale one")
	}
}

func TestAnswerSettlesForPeekOnly(t *testing.T) {
	// A statusline render is advisory and happens on every prompt; forcing a
	// network round trip on that cadence would be the wrong cost.
	s := &settlingSession{fakeSession: &fakeSession{}, next: &client.Attention{}}
	AnswerSocketRequest(context.Background(), StatusRequest(), s, HandleOptions{PID: 1})
	AnswerSocketRequest(context.Background(), ConsumeRequest(1), s, HandleOptions{PID: 1})
	if s.settled != 0 {
		t.Fatalf("settleAttention called %d times for non-peek verbs, want 0", s.settled)
	}
}

func TestASessionThatOffersNoSettlerIsAnsweredSynchronously(t *testing.T) {
	held := &client.Attention{VotesAwaited: []client.VoteAwaited{{}}}
	s := &fakeSession{attention: held}
	res := AnswerSocketRequest(context.Background(), PeekRequest(), s, HandleOptions{PID: 1}).(PeekResponse)
	if len(res.Attention.VotesAwaited) != 1 {
		t.Fatal("a bare/legacy session must be answered exactly as before")
	}
}

// --- the wire ---------------------------------------------------------------

func liveSession(t *testing.T, incidentID string, events ...client.Event) *session.Session {
	t.Helper()
	s := session.New(session.Options{Client: client.New(client.Config{IncidentID: incidentID, Slug: "acme"}, nil)})
	// Clean + not dirty, so SettleAttention answers from memory and never
	// reaches for a network this test does not have.
	s.SetAttention(&client.Attention{VotesAwaited: []client.VoteAwaited{{}}})
	s.SetAttentionDirty(false)
	for _, e := range events {
		s.EnqueueEvent(context.Background(), e)
	}
	return s
}

func TestARealRoundTripAnswersEveryVerb(t *testing.T) {
	ws := tempWorkspace(t)
	s := liveSession(t, "inc-real", client.Event{Seq: seq(1), Type: "edge.finding"}, client.Event{Seq: seq(2), Type: "edge.finding"})

	bound := StartHookSocket(context.Background(), s, StartOptions{
		Workspace: ws,
		PID:       1,
		Consume:   func(sess SocketSession, upTo int64) int64 { return sess.(*session.Session).ConsumeUpTo(upTo) },
		Log:       func(msg string) { t.Logf("bind: %s", msg) },
	})
	if bound == nil {
		t.Fatal("bind is expected to succeed in this environment")
	}
	defer func() { _ = bound.Close() }()

	status, err := SendToSocket(bound.SocketPath, StatusRequest(), 0)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !status.OK || status.IncidentID != "inc-real" || status.Pending != 2 || status.VotesAwaited != 1 {
		t.Fatalf("unexpected status: %+v", status)
	}

	peek, err := SendToSocket(bound.SocketPath, PeekRequest(), 0)
	if err != nil {
		t.Fatalf("peek: %v", err)
	}
	if peek.CountOr(0) != 2 || peek.MaxSeqOr(-1) != 2 || len(peek.Digest) != 2 {
		t.Fatalf("unexpected peek: %+v", peek)
	}

	consumed, err := SendToSocket(bound.SocketPath, ConsumeRequest(2), 0)
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if consumed.CursorOr(-1) != 2 {
		t.Fatalf("cursor = %d, want 2", consumed.CursorOr(-1))
	}
	if s.Cursor() != 2 || len(s.Pending()) != 0 {
		t.Fatal("consume must actually move the live session's cursor and drain the queue")
	}
}

func TestAnUnknownOpOverTheWireIsAnsweredNotDropped(t *testing.T) {
	ws := tempWorkspace(t)
	bound := StartHookSocket(context.Background(), &fakeSession{}, StartOptions{Workspace: ws, PID: 1})
	if bound == nil {
		t.Fatal("bind failed")
	}
	defer func() { _ = bound.Close() }()

	res, err := SendToSocket(bound.SocketPath, SocketRequest{Op: "frobnicate"}, 0)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if res.OK || res.Error != "unknown op: frobnicate" {
		t.Fatalf("unexpected answer: %+v", res)
	}
}

func TestAHandlerPanicBecomesAResponseNotADroppedConnection(t *testing.T) {
	ws := tempWorkspace(t)
	bound := StartHookSocket(context.Background(), &fakeSession{panics: true}, StartOptions{Workspace: ws, PID: 1})
	if bound == nil {
		t.Fatal("bind failed")
	}
	defer func() { _ = bound.Close() }()

	res, err := SendToSocket(bound.SocketPath, StatusRequest(), 0)
	if err != nil {
		t.Fatalf("the connection must carry an answer, not die: %v", err)
	}
	if res.OK || !strings.Contains(res.Error, "pending exploded") {
		t.Fatalf("unexpected answer: %+v", res)
	}
}

func TestAMalformedLineIsAnsweredByTheDefaultBranch(t *testing.T) {
	ws := tempWorkspace(t)
	bound := StartHookSocket(context.Background(), &fakeSession{}, StartOptions{Workspace: ws, PID: 1})
	if bound == nil {
		t.Fatal("bind failed")
	}
	defer func() { _ = bound.Close() }()

	conn, err := net.Dial("unix", bound.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte("{not json at all\n")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var res SocketResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(buf[:n]))), &res); err != nil {
		t.Fatal(err)
	}
	if res.OK || res.Error != "unknown op: (none)" {
		t.Fatalf("unexpected answer: %+v", res)
	}
}

func TestSendToSocketGivesUpRatherThanHangingOnASilentNode(t *testing.T) {
	ws := tempWorkspace(t)
	loc := SocketLocationFor(ws)
	if err := os.MkdirAll(loc.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A listener that accepts and then says nothing at all.
	silent := loc.PathFor(loc.NameFor("9"))
	ln, err := net.Listen("unix", silent)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn // held open, never answered
		}
	}()

	started := time.Now()
	if _, err := SendToSocket(silent, StatusRequest(), SocketTimeout); err == nil {
		t.Fatal("a silent node must fail, not hang")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("gave up after %v — the 250ms budget is not being honoured", elapsed)
	}
}

// --- fan-out ----------------------------------------------------------------

func TestListHookSocketsIsEmptyWhenNoServeHasEverRunHere(t *testing.T) {
	if got := ListHookSockets(tempWorkspace(t)); len(got) != 0 {
		t.Fatalf("want no sockets, got %v", got)
	}
}

func TestListHookSocketsIgnoresNonSocketEntries(t *testing.T) {
	ws := tempWorkspace(t)
	loc := SocketLocationFor(ws)
	if err := os.MkdirAll(loc.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"1.sock", "pending-digest.json", "status-cache.json", "2.sock"} {
		if err := os.WriteFile(loc.PathFor(name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got := ListHookSockets(ws)
	if len(got) != 2 || filepath.Base(got[0]) != "1.sock" || filepath.Base(got[1]) != "2.sock" {
		t.Fatalf("unexpected sockets: %v", got)
	}
}

func TestQueryHookSocketsUnionsLiveAnswersAndDropsUnreachableOnesSilently(t *testing.T) {
	ws := tempWorkspace(t)
	a := StartHookSocket(context.Background(), liveSession(t, "inc-a", client.Event{Seq: seq(1), Type: "edge.finding"}),
		StartOptions{Workspace: ws, PID: 1})
	b := StartHookSocket(context.Background(), liveSession(t, "inc-b"), StartOptions{Workspace: ws, PID: 2})
	if a == nil || b == nil {
		t.Fatal("both binds are expected to succeed")
	}
	defer func() { _ = a.Close() }()
	defer func() { _ = b.Close() }()

	// A crashed session's leftover node: present in the directory, answering
	// nothing. It must be skipped, not reported as a failure.
	loc := SocketLocationFor(ws)
	if err := os.WriteFile(loc.PathFor(loc.NameFor("999999")), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	answers := QueryHookSockets(PeekRequest(), ws, 0)
	if len(answers) != 2 {
		t.Fatalf("want 2 live answers, got %d: %+v", len(answers), answers)
	}
	seen := map[string]string{}
	for _, ans := range answers {
		if !ans.Response.OK {
			t.Fatal("only ok:true answers may survive the filter")
		}
		if ans.SocketPath == "" {
			t.Fatal("every answer must be tagged with the socket it came from")
		}
		seen[ans.Response.IncidentID] = ans.SocketPath
	}
	if seen["inc-a"] == "" || seen["inc-b"] == "" {
		t.Fatalf("both sessions must be represented: %v", seen)
	}
	if seen["inc-a"] == seen["inc-b"] {
		t.Fatal("the socketPath tag must distinguish the two sessions")
	}
}

func TestQueryHookSocketsDropsNotOkAnswers(t *testing.T) {
	ws := tempWorkspace(t)
	bound := StartHookSocket(context.Background(), &fakeSession{}, StartOptions{Workspace: ws, PID: 1})
	if bound == nil {
		t.Fatal("bind failed")
	}
	defer func() { _ = bound.Close() }()

	if got := QueryHookSockets(SocketRequest{Op: "frobnicate"}, ws, 0); len(got) != 0 {
		t.Fatalf("only ok:true answers are kept, got %+v", got)
	}
}

// --- bind resilience, permissions, sun_path ---------------------------------

func TestBindTakesTheNextNameRatherThanEvictingALiveSibling(t *testing.T) {
	ws := tempWorkspace(t)
	first := StartHookSocket(context.Background(), &fakeSession{}, StartOptions{Workspace: ws, PID: 1})
	if first == nil {
		t.Fatal("first bind failed")
	}
	defer func() { _ = first.Close() }()

	second := StartHookSocket(context.Background(), &fakeSession{}, StartOptions{Workspace: ws, PID: 1})
	if second == nil {
		t.Fatal("second bind failed")
	}
	defer func() { _ = second.Close() }()

	if filepath.Base(second.SocketPath) != "1-1.sock" {
		t.Fatalf("second socket is %q, want 1-1.sock", filepath.Base(second.SocketPath))
	}
	// And the live one is still answering.
	if _, err := SendToSocket(first.SocketPath, StatusRequest(), 0); err != nil {
		t.Fatalf("the live sibling must not have been evicted: %v", err)
	}
}

func TestBindUnlinksAndReusesADeadNodesName(t *testing.T) {
	ws := tempWorkspace(t)
	loc := SocketLocationFor(ws)
	if err := os.MkdirAll(loc.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := loc.PathFor(loc.NameFor("2"))
	if err := os.WriteFile(stale, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	bound := StartHookSocket(context.Background(), &fakeSession{}, StartOptions{Workspace: ws, PID: 2})
	if bound == nil {
		t.Fatal("bind over a dead node failed")
	}
	defer func() { _ = bound.Close() }()
	if bound.SocketPath != stale {
		t.Fatalf("a dead node's name must be reused, got %q", bound.SocketPath)
	}
	if _, err := SendToSocket(bound.SocketPath, StatusRequest(), 0); err != nil {
		t.Fatalf("the rebound socket must answer: %v", err)
	}
}

func TestBindSetsThe0700DirectoryAnd0600SocketModes(t *testing.T) {
	ws := tempWorkspace(t)
	loc := SocketLocationFor(ws)
	// A pre-existing, wide-open directory: MkdirAll's mode would not fix it, so
	// the explicit chmod is the thing under test.
	if err := os.MkdirAll(loc.Dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(loc.Dir, 0o777); err != nil {
		t.Fatal(err)
	}

	bound := StartHookSocket(context.Background(), &fakeSession{}, StartOptions{Workspace: ws, PID: 1})
	if bound == nil {
		t.Fatal("bind failed")
	}
	defer func() { _ = bound.Close() }()

	dirInfo, err := os.Stat(loc.Dir)
	if err != nil {
		t.Fatal(err)
	}
	// The 0700 directory is the ENTIRE access-control story.
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Fatalf("directory mode %o, want 700", perm)
	}
	sockInfo, err := os.Stat(bound.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := sockInfo.Mode().Perm(); perm != 0o600 {
		t.Fatalf("socket mode %o, want 600", perm)
	}
}

func TestAnOverLongPathIsANonFatalBindFailureThatNamesTheRealCause(t *testing.T) {
	// The macOS/BSD sockaddr_un.sun_path cap is 104 bytes; over-length bind fails
	// with EINVAL, NOT EADDRINUSE, so the 3-attempt retry never covers it. It
	// must stay non-fatal (serve still serves MCP) AND diagnosable.
	root, err := os.MkdirTemp("/tmp", "lf-long-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	deep := filepath.Join(root, strings.Repeat("d", 90))
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatal(err)
	}
	ws := Workspace{Cwd: root, Env: map[string]string{"XDG_RUNTIME_DIR": deep}}

	var logged []string
	bound := StartHookSocket(context.Background(), &fakeSession{}, StartOptions{
		Workspace: ws, PID: 1, Log: func(msg string) { logged = append(logged, msg) },
	})
	if bound != nil {
		_ = bound.Close()
		t.Fatal("an over-long socket path cannot bind")
	}
	if len(logged) != 1 {
		t.Fatalf("exactly one diagnostic expected, got %v", logged)
	}
	if !strings.Contains(logged[0], "sun_path") || !strings.Contains(logged[0], "lifecycle hooks will not see this session") {
		t.Fatalf("the log line must name the real cause, got %q", logged[0])
	}
}

func TestCloseRemovesTheSocketNodeAndIsIdempotent(t *testing.T) {
	ws := tempWorkspace(t)
	bound := StartHookSocket(context.Background(), &fakeSession{}, StartOptions{Workspace: ws, PID: 1})
	if bound == nil {
		t.Fatal("bind failed")
	}
	if err := bound.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := os.Stat(bound.SocketPath); !os.IsNotExist(err) {
		t.Fatal("close must unlink the socket node")
	}
	if err := bound.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if got := ListHookSockets(ws); len(got) != 0 {
		t.Fatalf("a closed socket must leave nothing behind, got %v", got)
	}
}
