// jsonmerge.go — the one non-destructive JSON merge/idempotency/conflict
// implementation shared by every direct-JSON-editing harness adapter (Cursor,
// VS Code's fallback, Claude Desktop, Windsurf) AND, through
// internal/hooks/hosts, by Claude Code's `statusLine` surface. A Go port of
// `src/install/json-merge.mjs`.
//
// There is exactly one canonical entry value landfall ever writes; every
// adapter just picks the file path and the key path within it (e.g.
// mcpServers→landfall or, for VS Code, servers→landfall).
//
// THE ONE RULE THAT GOVERNS THIS ENTIRE FILE: these are configuration files
// this CLI does not own the schema of. Every value read from disk stays a
// `map[string]any` / `[]any` from the moment it is parsed to the moment it is
// written back — NEVER unmarshaled into a typed struct anywhere in this path,
// because an unknown key in a struct-shaped round trip is a key silently
// deleted from somebody else's config.
package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Install-plan actions.
const (
	ActionWrite            = "write"
	ActionAlreadyInstalled = "already-installed"
	ActionConflict         = "conflict"
	ActionConfigured       = "configured"
)

// Uninstall-plan actions.
const (
	ActionNotInstalled = "not-installed"
	ActionRemove       = "remove"
	ActionLeftInPlace  = "left-in-place"
	ActionRemoved      = "removed"
)

// LandfallEntry is the one value landfall ever writes into a harness's own
// config. Returned as a fresh map per call rather than exposed as a package
// var: a shared mutable map handed to four adapters is one accidental write
// away from corrupting every one of them.
func LandfallEntry() map[string]any {
	return map[string]any{"command": "landfall", "args": []any{"serve"}}
}

// UnparseableConfigError is returned when an existing config file cannot be
// parsed. Such a file is NEVER overwritten (FR-012): the only safe response to
// "there is something here and I do not understand it" is to report it.
type UnparseableConfigError struct {
	Path string
	Err  error
}

func (e *UnparseableConfigError) Error() string {
	return fmt.Sprintf("%s exists but could not be parsed as JSON: %s", e.Path, e.Err)
}

func (e *UnparseableConfigError) Unwrap() error { return e.Err }

// ReadJSONOrEmpty parses `path`, treating BOTH a missing file and a file whose
// contents are empty/whitespace as an empty object — the two states that mean
// "nothing configured yet" rather than "something I must not touch".
//
// A file that exists and holds bytes that are not JSON returns
// *UnparseableConfigError and never an empty map, so no caller can mistake it
// for a fresh file and write over it.
func ReadJSONOrEmpty(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	if strings.TrimSpace(string(raw)) == "" {
		return map[string]any{}, nil
	}
	var data map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&data); err != nil {
		return nil, &UnparseableConfigError{Path: path, Err: err}
	}
	if data == nil {
		// Literal `null` parses without error into a nil map. It is not an
		// object, but it is also not something worth refusing over — treat it
		// as the empty object the file is trying to be.
		data = map[string]any{}
	}
	return data, nil
}

// WriteJSONPretty writes `data` as 2-space-indented JSON with a trailing
// newline, creating parent directories as needed — the exact output shape
// `JSON.stringify(data, null, 2) + '\n'` produces, so a config file this CLI
// rewrites looks the way the Node CLI left it.
//
// HTML escaping is disabled: encoding/json would otherwise turn `&`, `<` and
// `>` inside somebody else's config string into & &c. That is valid JSON
// but a gratuitous, visible edit to a value we were only supposed to carry
// through untouched.
func WriteJSONPretty(path string, data map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(data); err != nil {
		return err
	}
	// Encode already appends exactly one "\n".
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// GetPath walks `obj` down `segments` and returns the value there.
//
// PATH SEGMENTS, NOT A DOTTED STRING — deliberately different from
// `src/install/json-merge.mjs`, whose getPath/setPath/deletePath split a
// dotted string on every '.'. That has a real latent bug: a config key that
// itself contains a literal dot is unreachable, and worse, `setPath` on such a
// path silently creates a NESTED object instead of the flat key the caller
// meant. Taking segments removes the hazard from the port entirely rather than
// carrying it forward (inventory.md's "reconsider, don't transliterate").
//
// The bool distinguishes "key absent" from "key present holding JSON null",
// which is exactly the `existing === undefined` check the plan functions make:
// a key explicitly set to null is somebody's deliberate configuration and must
// classify as a conflict, not as an empty slot to fill.
func GetPath(obj map[string]any, segments []string) (any, bool) {
	var cur any = obj
	for _, key := range segments {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		v, found := m[key]
		if !found {
			return nil, false
		}
		cur = v
	}
	return cur, true
}

// SetPath returns a NEW object with `value` set at `segments`; `obj` is not
// mutated, and neither is any map reachable from it.
//
// Only the maps ON the path are copied — siblings are carried by reference,
// which is safe precisely because nothing in this package ever mutates a map
// in place. That is the same copy-on-write shape as the Node original's
// spread-per-level, and it is what lets a caller hold a plan's `Data` and
// compare it against what was actually written.
//
// An intermediate segment holding a non-object (a string, a number, an array)
// is REPLACED by a fresh object. The alternative — refusing — would mean an
// install could be permanently blocked by a scalar sitting where a container
// belongs; the alternative Node actually has (spreading a string into an
// index-keyed object) is worse than either.
func SetPath(obj map[string]any, segments []string, value any) map[string]any {
	if len(segments) == 0 {
		return obj
	}
	root := copyMap(obj)
	cur := root
	for i, key := range segments {
		if i == len(segments)-1 {
			cur[key] = value
			break
		}
		child, _ := cur[key].(map[string]any)
		next := copyMap(child)
		cur[key] = next
		cur = next
	}
	return root
}

// DeletePath returns a NEW object with the key at `segments` removed; `obj` is
// not mutated. Same copy-on-write discipline as SetPath.
func DeletePath(obj map[string]any, segments []string) map[string]any {
	if len(segments) == 0 {
		return obj
	}
	root := copyMap(obj)
	cur := root
	for i, key := range segments {
		if i == len(segments)-1 {
			delete(cur, key)
			break
		}
		child, _ := cur[key].(map[string]any)
		next := copyMap(child)
		cur[key] = next
		cur = next
	}
	return root
}

func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	return out
}

// EqualJSON is order-insensitive structural comparison of two decoded-JSON
// values.
//
// DO NOT "FIX" THIS BACK INTO A MARSHAL-AND-COMPARE. `src/install/json-
// merge.mjs:89-91` implements deepEqual as
// `JSON.stringify(a) === JSON.stringify(b)`, which is sensitive to KEY ORDER:
// under Node, `{command, args}` and `{args, command}` are two different
// entries, so a user (or a future version of the host app) who reordered the
// keys in a landfall entry it otherwise left alone would see it classified as
// a `conflict` — never upgraded, never removable by `landfall uninstall`.
//
// Spec FR-014 (resolved; data-model.md "Hook Registration" → Parity risk;
// tasks.md T047/OD-4) decides this is a deliberate, documented BEHAVIOR CHANGE
// in the Go port: equality is structural, so key order is irrelevant. Go has
// no stable-key-order JSON object type to replicate the old quirk with anyway,
// and replicating it would mean re-serializing a map through a sorted encoder
// — which is not the Node behavior either, just a third one.
//
// Numbers are compared by their decoded value, not their Go type, so a
// json.Number from disk and an int/float64 literal in a Go-constructed entry
// compare equal when they denote the same number.
func EqualJSON(a, b any) bool {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, v := range av {
			other, found := bv[k]
			if !found || !EqualJSON(v, other) {
				return false
			}
		}
		return true
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !EqualJSON(av[i], bv[i]) {
				return false
			}
		}
		return true
	case nil:
		return b == nil
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	default:
		an, aok := numberOf(a)
		bn, bok := numberOf(b)
		if aok && bok {
			return an == bn
		}
		return false
	}
}

func numberOf(v any) (float64, bool) {
	switch n := v.(type) {
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}

// Plan is the result of inspecting a config file without writing to it.
type Plan struct {
	// Action is one of the Action* constants above.
	Action string
	// Data is the parsed config as it was found (for a plan) or as it was
	// written (for an applied result).
	Data map[string]any
	// Existing is the value already at the key path, when there was one.
	Existing any
}

// PlanInstall inspects `path` (a JSON config file, which may not exist)
// WITHOUT writing anything, and reports what an install would do:
//
//	write             — the key is absent; ApplyInstall would create it
//	already-installed — the key holds exactly landfall's own entry
//	conflict          — the key holds something else; NEVER overwritten
func PlanInstall(path string, keyPath []string, entry any) (Plan, error) {
	data, err := ReadJSONOrEmpty(path)
	if err != nil {
		return Plan{}, err
	}
	existing, found := GetPath(data, keyPath)
	if !found {
		return Plan{Action: ActionWrite, Data: data}, nil
	}
	if EqualJSON(existing, entry) {
		return Plan{Action: ActionAlreadyInstalled, Data: data, Existing: existing}, nil
	}
	return Plan{Action: ActionConflict, Data: data, Existing: existing}, nil
}

// ApplyInstall applies the plan from PlanInstall: writes the file ONLY when
// the key was absent. Passes `already-installed` / `conflict` through
// unchanged, having written nothing in either case.
func ApplyInstall(path string, keyPath []string, entry any) (Plan, error) {
	plan, err := PlanInstall(path, keyPath, entry)
	if err != nil {
		return Plan{}, err
	}
	if plan.Action != ActionWrite {
		return plan, nil
	}
	next := SetPath(plan.Data, keyPath, entry)
	if err := WriteJSONPretty(path, next); err != nil {
		return Plan{}, err
	}
	return Plan{Action: ActionConfigured, Data: next}, nil
}

// PlanUninstall inspects `path` without writing anything, and reports what an
// uninstall would do:
//
//	not-installed — the key is absent (nothing to remove)
//	remove        — the key holds exactly landfall's own entry
//	left-in-place — the key holds something else (hand-edited since install);
//	                NEVER deleted
func PlanUninstall(path string, keyPath []string, entry any) (Plan, error) {
	data, err := ReadJSONOrEmpty(path)
	if err != nil {
		return Plan{}, err
	}
	existing, found := GetPath(data, keyPath)
	if !found {
		return Plan{Action: ActionNotInstalled, Data: data}, nil
	}
	if EqualJSON(existing, entry) {
		return Plan{Action: ActionRemove, Data: data, Existing: existing}, nil
	}
	return Plan{Action: ActionLeftInPlace, Data: data, Existing: existing}, nil
}

// ApplyUninstall applies the plan from PlanUninstall: writes the file ONLY
// when the key matched landfall's own entry exactly.
func ApplyUninstall(path string, keyPath []string, entry any) (Plan, error) {
	plan, err := PlanUninstall(path, keyPath, entry)
	if err != nil {
		return Plan{}, err
	}
	if plan.Action != ActionRemove {
		return plan, nil
	}
	next := DeletePath(plan.Data, keyPath)
	if err := WriteJSONPretty(path, next); err != nil {
		return Plan{}, err
	}
	return Plan{Action: ActionRemoved, Data: next}, nil
}
