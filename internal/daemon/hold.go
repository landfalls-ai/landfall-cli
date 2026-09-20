package daemon

import "strings"

// The working-directory rule (spec FR-008, research R4). The daemon decides;
// the front end acts. A front end asks `match` with a share's text; the daemon
// answers what the text names from that reader's workspace fingerprints, and
// whether the person has allowed working-directory content for the room. The
// spool, and therefore the hold itself, stays with the front end's worker,
// which is per workspace; the daemon never opens a spool.

// Match reports which fingerprints a share's text names. Deliberately simple
// and explainable (data-model "WorkspaceFingerprints"): a commit hash anywhere,
// a tracked path longer than eight characters anywhere, a name as a whole word.
func (fp *Fingerprints) Match(text string) []string {
	if fp == nil || text == "" {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, c := range fp.Commits {
		if len(c) >= 7 && strings.Contains(text, c) {
			add(c)
		}
	}
	for _, p := range fp.Paths {
		if len(p) > 8 && strings.Contains(text, p) {
			add(p)
		}
	}
	lower := strings.ToLower(text)
	for _, n := range fp.Names {
		if n == "" {
			continue
		}
		if containsWord(lower, strings.ToLower(n)) {
			add(n)
		}
	}
	return out
}

func containsWord(haystack, word string) bool {
	idx := 0
	for {
		i := strings.Index(haystack[idx:], word)
		if i < 0 {
			return false
		}
		start := idx + i
		end := start + len(word)
		before := start == 0 || !isWordByte(haystack[start-1])
		after := end == len(haystack) || !isWordByte(haystack[end])
		if before && after {
			return true
		}
		idx = start + 1
	}
}

func isWordByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// match answers the `match` verb: what the text names, and whether the room
// allows working-directory content. Fingerprints are kept per reader
// workspace, so two checkouts in one room each match their own files.
func (d *Daemon) match(req Request) Response {
	room := d.room(req.RoomKey)
	if room == nil {
		return fail("no such room")
	}
	rd, found := room.Reader(req.ReaderName)
	if !found {
		return fail("no reader " + req.ReaderName)
	}
	d.mu.Lock()
	fp := d.fingerprints[room.Key+"|"+rd.WorkspaceKey]
	d.mu.Unlock()
	room.mu.Lock()
	allowed := room.CwdAllowed
	room.mu.Unlock()
	res := ok()
	res.Matched = fp.Match(req.Text)
	res.Allowed = allowed
	res.Held = !allowed && len(res.Matched) > 0
	return res
}

// allowCwd answers `allow-cwd`: the person's decision, recorded on the room.
func (d *Daemon) allowCwd(req Request) Response {
	room := d.room(req.RoomKey)
	if room == nil {
		return fail("no such room")
	}
	room.mu.Lock()
	room.CwdAllowed = true
	room.mu.Unlock()
	d.save()
	return ok()
}
