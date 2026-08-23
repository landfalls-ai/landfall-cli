// hooksmerge.go — the non-destructive merge core for hook config, and the one
// place the hook install/uninstall decision is made. A Go port of
// `src/hooks/merge.mjs`.
//
// Why this lives next to jsonmerge.go rather than inside it: MCP registration
// is ONE key holding ONE object, so "is it ours?" is a whole-value comparison.
// A hook config is a LIST per event that the user writes into too — a plain
// set/delete at a key path would take their hooks with it. So the unit here is
// one ELEMENT of an array, and every operation preserves every element it did
// not put there. The two are genuinely different problems; unifying them would
// mean one of the two behaviours becoming wrong.
//
// Four outcomes per registration, and no fifth:
//
//	present  — an element equal to what we would write is already there
//	outdated — an element equal to what an OLDER version of this installer
//	           wrote (Registration.Superseded): still ours, still unmodified,
//	           just stale — upgraded in place, position kept
//	conflict — an element that IS ours by command prefix but matches neither
//	           the current nor any past form (a human edited it): never
//	           overwritten, never deleted, never duplicated
//	absent   — nothing of ours: append, leaving every other element untouched
//
// `outdated` is what makes changing a registered command survivable (#228).
// Without it the only honest classification for a stale entry is `conflict`,
// and a conflict is by design immovable: install refuses to write and
// uninstall refuses to delete, so an upgrade would leave a dead entry that our
// own tooling cannot clear. Enumerating past forms explicitly — rather than
// treating "starts with landfall hooks but differs" as replaceable — is what
// keeps a hand-edited entry safe, since it matches no enumerated form.
package install

// Registration is one hook entry to register at one key path.
type Registration struct {
	// KeyPath is path SEGMENTS (e.g. {"hooks", "Stop"}), never a dotted
	// string — see GetPath's comment for why.
	KeyPath []string
	// Entry is the element to ensure is present in the list at KeyPath.
	Entry any
	// Superseded are exact elements a PREVIOUS version of this installer
	// wrote for the same (event, host).
	Superseded []any
}

// Hook-registration states. These are per-REGISTRATION; the file-level action
// is the worst case across them (see PlanHookInstall).
const (
	StatePresent  = "present"
	StateOutdated = "outdated"
	StateConflict = "conflict"
	StateAbsent   = "absent"
)

// IsOurs recognizes a landfall-authored element regardless of whether it still
// matches byte-for-byte. It is what separates "our entry, edited by a human"
// (untouchable) from "somebody else's hook" (invisible to us).
type IsOurs func(element any) bool

// PlannedRegistration is a Registration plus the state it was found in.
type PlannedRegistration struct {
	Registration
	State string
}

// HookPlan is the result of inspecting a hook config file.
type HookPlan struct {
	// Action is one of the Action* constants in jsonmerge.go.
	Action string
	// Data is the parsed config as found (plan) or as written (applied).
	Data map[string]any
	// Registrations carries each registration's own state, which survives
	// into the applied result as the PLAN-TIME state — deliberately, so a
	// caller can report "hooks would have been written, but the statusLine
	// conflicted so nothing was".
	Registrations []PlannedRegistration
	// Detail is an optional human-readable note (Codex's TOML flag uses it).
	Detail string
}

func listAt(data map[string]any, keyPath []string) []any {
	v, found := GetPath(data, keyPath)
	if !found {
		return nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	return list
}

func isSuperseded(r Registration, element any) bool {
	for _, old := range r.Superseded {
		if EqualJSON(element, old) {
			return true
		}
	}
	return false
}

// Classify reports one registration's state against the config already on
// disk.
//
// `outdated` is tested BEFORE `present`, so a file holding both the current
// entry and a stale one is still reported as work to do — otherwise the stale
// entry would survive every future install, firing a command written for a
// contract we no longer answer.
func Classify(data map[string]any, r Registration, isOurs IsOurs) string {
	list := listAt(data, r.KeyPath)
	for _, el := range list {
		if isSuperseded(r, el) {
			return StateOutdated
		}
	}
	for _, el := range list {
		if EqualJSON(el, r.Entry) {
			return StatePresent
		}
	}
	if isOurs != nil {
		for _, el := range list {
			if isOurs(el) {
				return StateConflict
			}
		}
	}
	return StateAbsent
}

// upgradeInPlace returns the list with every stale form of `r` replaced by the
// current entry, IN PLACE — position preserved (a user reading their own
// config should not find our hook has jumped to the end), duplicates collapsed
// to one.
func upgradeInPlace(list []any, r Registration) []any {
	kept := make([]any, 0, len(list)+1)
	placed := false
	for _, el := range list {
		stale := isSuperseded(r, el)
		current := EqualJSON(el, r.Entry)
		if !stale && !current {
			kept = append(kept, el)
			continue
		}
		if placed {
			continue // a second copy of ours, stale or not, is not kept
		}
		kept = append(kept, r.Entry)
		placed = true
	}
	if !placed {
		kept = append(kept, r.Entry)
	}
	return kept
}

func classifyAll(data map[string]any, regs []Registration, isOurs IsOurs) []PlannedRegistration {
	per := make([]PlannedRegistration, 0, len(regs))
	for _, r := range regs {
		per = append(per, PlannedRegistration{Registration: r, State: Classify(data, r, isOurs)})
	}
	return per
}

func anyState(per []PlannedRegistration, states ...string) bool {
	for _, r := range per {
		for _, s := range states {
			if r.State == s {
				return true
			}
		}
	}
	return false
}

// PlanHookInstall reports what an install would do to `path`, without writing
// anything. The file-level action is the worst case across its registrations:
// any conflict wins, then any write, else already-installed.
func PlanHookInstall(path string, regs []Registration, isOurs IsOurs) (HookPlan, error) {
	data, err := ReadJSONOrEmpty(path)
	if err != nil {
		return HookPlan{}, err
	}
	per := classifyAll(data, regs, isOurs)
	action := ActionAlreadyInstalled
	switch {
	case anyState(per, StateConflict):
		action = ActionConflict
	case anyState(per, StateAbsent, StateOutdated):
		action = ActionWrite
	}
	return HookPlan{Action: action, Data: data, Registrations: per}, nil
}

// ApplyHookInstall applies PlanHookInstall. It writes ONLY when at least one
// registration is absent or outdated and none conflicts, and touches only
// those — a partially installed file gains exactly what it was missing.
//
// A conflict writes NOTHING AT ALL, including for the registrations that would
// have been fine: a file whose Stop entry a human rewrote is a file to leave
// alone and report on, not one to half-update. This is the "not one byte
// written" guarantee the whole command is judged on.
func ApplyHookInstall(path string, regs []Registration, isOurs IsOurs) (HookPlan, error) {
	plan, err := PlanHookInstall(path, regs, isOurs)
	if err != nil {
		return HookPlan{}, err
	}
	if plan.Action != ActionWrite {
		return plan, nil
	}
	next := plan.Data
	for _, r := range plan.Registrations {
		switch r.State {
		case StateAbsent:
			appended := append(append([]any{}, listAt(next, r.KeyPath)...), r.Entry)
			next = SetPath(next, r.KeyPath, appended)
		case StateOutdated:
			next = SetPath(next, r.KeyPath, upgradeInPlace(listAt(next, r.KeyPath), r.Registration))
		}
	}
	if err := WriteJSONPretty(path, next); err != nil {
		return HookPlan{}, err
	}
	return HookPlan{Action: ActionConfigured, Data: next, Registrations: plan.Registrations}, nil
}

// PlanHookUninstall reports what an uninstall would do, without writing:
//
//	remove        — at least one entry of ours is present exactly, in its
//	                current form or in one an older version wrote
//	left-in-place — only edited-since entries of ours remain
//	not-installed — nothing of ours anywhere
func PlanHookUninstall(path string, regs []Registration, isOurs IsOurs) (HookPlan, error) {
	data, err := ReadJSONOrEmpty(path)
	if err != nil {
		return HookPlan{}, err
	}
	per := classifyAll(data, regs, isOurs)
	action := ActionNotInstalled
	switch {
	case anyState(per, StatePresent, StateOutdated):
		action = ActionRemove
	case anyState(per, StateConflict):
		action = ActionLeftInPlace
	}
	return HookPlan{Action: action, Data: data, Registrations: per}, nil
}

// ApplyHookUninstall applies PlanHookUninstall: it drops exactly the elements
// equal to what we wrote and nothing else.
//
// Uninstall is SELECTIVE where install is all-or-nothing — deliberately, and
// not an inconsistency: it removes what it can safely remove and leaves what a
// human edited, per registration. An event list, or the container holding it,
// that we have just emptied is pruned, so an install→uninstall round trip
// leaves the file as it was found rather than littered with `"Stop": []`. A
// list that still holds someone else's hook is of course kept.
func ApplyHookUninstall(path string, regs []Registration, isOurs IsOurs) (HookPlan, error) {
	plan, err := PlanHookUninstall(path, regs, isOurs)
	if err != nil {
		return HookPlan{}, err
	}
	if plan.Action != ActionRemove {
		return plan, nil
	}
	next := plan.Data
	for _, r := range plan.Registrations {
		if r.State != StatePresent && r.State != StateOutdated {
			continue
		}
		// An entry an older version wrote is still ours to remove — leaving it
		// behind would make "uninstall removed everything landfall added" false.
		kept := make([]any, 0)
		for _, el := range listAt(next, r.KeyPath) {
			if EqualJSON(el, r.Entry) || isSuperseded(r.Registration, el) {
				continue
			}
			kept = append(kept, el)
		}
		if len(kept) > 0 {
			next = SetPath(next, r.KeyPath, kept)
		} else {
			next = DeletePath(next, r.KeyPath)
		}
	}
	for _, parent := range pruneCandidates(plan.Registrations) {
		value, found := GetPath(next, parent)
		if !found {
			continue
		}
		if m, ok := value.(map[string]any); ok && len(m) == 0 {
			next = DeletePath(next, parent)
		}
	}
	if err := WriteJSONPretty(path, next); err != nil {
		return HookPlan{}, err
	}
	return HookPlan{Action: ActionRemoved, Data: next, Registrations: plan.Registrations}, nil
}

// pruneCandidates is the parent key paths ({"hooks"} for {"hooks","Stop"})
// worth pruning if left empty, deduped, first-seen order.
func pruneCandidates(regs []PlannedRegistration) [][]string {
	var out [][]string
	seen := map[string]bool{}
	for _, r := range regs {
		if len(r.KeyPath) < 2 {
			continue
		}
		parent := r.KeyPath[:len(r.KeyPath)-1]
		key := joinSegments(parent)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, parent)
	}
	return out
}

// joinSegments is a dedupe key only — never a path used for lookup, so the
// dotted-key ambiguity GetPath avoids does not apply. A separator no JSON key
// is likely to contain keeps even that theoretical collision away.
func joinSegments(segments []string) string {
	key := ""
	for _, s := range segments {
		key += s + "\x00"
	}
	return key
}
