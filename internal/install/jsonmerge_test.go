package install

// jsonmerge_test.go — the single-key merge core (T047). Real files in a real
// temp directory throughout, never a mock filesystem: the whole point of this
// code is what a config file on disk looks like after it runs.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadJSONOrEmpty_MissingFileIsAnEmptyObjectNotAnError(t *testing.T) {
	data, err := ReadJSONOrEmpty(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("want nil error, got %v", err)
	}
	if len(data) != 0 {
		t.Fatalf("want empty object, got %v", data)
	}
}

func TestReadJSONOrEmpty_AnEmptyFileIsAnEmptyObject(t *testing.T) {
	p := filepath.Join(t.TempDir(), "empty.json")
	if err := os.WriteFile(p, []byte("   \n\t "), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := ReadJSONOrEmpty(p)
	if err != nil || len(data) != 0 {
		t.Fatalf("want empty object and nil error, got %v %v", data, err)
	}
}

func TestReadJSONOrEmpty_AnUnparseableFileIsADistinctErrorAndIsNeverTreatedAsEmpty(t *testing.T) {
	p := filepath.Join(t.TempDir(), "broken.json")
	if err := os.WriteFile(p, []byte("{ this is not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ReadJSONOrEmpty(p)
	var unparseable *UnparseableConfigError
	if err == nil || !asUnparseable(err, &unparseable) {
		t.Fatalf("want *UnparseableConfigError, got %#v", err)
	}
	if unparseable.Path != p {
		t.Fatalf("error must name the file, got %q", unparseable.Path)
	}
	// And the message says what happened, since it is shown to the user
	// verbatim on the report line.
	if want := "could not be parsed as JSON"; !contains(err.Error(), want) {
		t.Fatalf("message %q must contain %q", err.Error(), want)
	}
}

func TestPlanInstall_NeverWritesAndAnUnparseableFileIsNeverOverwritten(t *testing.T) {
	p := filepath.Join(t.TempDir(), "broken.json")
	const original = "{ this is not json"
	if err := os.WriteFile(p, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyInstall(p, []string{"mcpServers", "landfall"}, LandfallEntry()); err == nil {
		t.Fatal("want an error, got nil")
	}
	if got := readText(t, p); got != original {
		t.Fatalf("not one byte may be written: got %q", got)
	}
}

func TestWriteJSONPretty_TwoSpaceIndentTrailingNewlineAndCreatedParents(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a", "b", "c.json")
	if err := WriteJSONPretty(p, map[string]any{"k": map[string]any{"n": "v"}}); err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"k\": {\n    \"n\": \"v\"\n  }\n}\n"
	if got := readText(t, p); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestWriteJSONPretty_DoesNotHTMLEscapeSomebodyElsesValues(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	if err := WriteJSONPretty(p, map[string]any{"args": []any{"--filter=a<b&c"}}); err != nil {
		t.Fatal(err)
	}
	if got := readText(t, p); !contains(got, "a<b&c") {
		t.Fatalf("a value we only carry through must survive verbatim, got %q", got)
	}
}

func TestGetPath_DistinguishesAnAbsentKeyFromOneHoldingNull(t *testing.T) {
	data := map[string]any{"mcpServers": map[string]any{"landfall": nil}}
	v, found := GetPath(data, []string{"mcpServers", "landfall"})
	if !found || v != nil {
		t.Fatalf("an explicit null is PRESENT: got %v found=%v", v, found)
	}
	if _, found := GetPath(data, []string{"mcpServers", "other"}); found {
		t.Fatal("an absent key must report not-found")
	}
}

func TestGetPathAndSetPath_HandleAKeyContainingALiteralDot(t *testing.T) {
	// The reason this package takes path SEGMENTS rather than a dotted string:
	// the Node original splits on '.', so this key is unreachable there and
	// setPath on it silently creates a nested object instead.
	next := SetPath(map[string]any{}, []string{"my.key"}, "v")
	if got, found := GetPath(next, []string{"my.key"}); !found || got != "v" {
		t.Fatalf("got %v found=%v", got, found)
	}
	if _, nested := next["my"]; nested {
		t.Fatal("must be a flat key, not a nested object")
	}
}

func TestSetPathAndDeletePath_DoNotMutateTheInput(t *testing.T) {
	original := map[string]any{"mcpServers": map[string]any{"other": "keep"}}
	SetPath(original, []string{"mcpServers", "landfall"}, LandfallEntry())
	DeletePath(original, []string{"mcpServers", "other"})
	inner := original["mcpServers"].(map[string]any)
	if len(inner) != 1 || inner["other"] != "keep" {
		t.Fatalf("input was mutated: %v", original)
	}
}

func TestEqualJSON_IsOrderInsensitive_TheResolvedFR014BehaviorChange(t *testing.T) {
	// Node compares JSON.stringify output, so these two are NOT equal there and
	// the second would be reported as a permanent `conflict`. FR-014 (resolved)
	// makes the Go port structural. This test exists so nobody "fixes" the
	// comparison back into a marshal-and-compare.
	a := map[string]any{"command": "landfall", "args": []any{"serve"}}
	b := map[string]any{"args": []any{"serve"}, "command": "landfall"}
	if !EqualJSON(a, b) {
		t.Fatal("key order must not affect equality")
	}
}

func TestEqualJSON_ArrayOrderStillMatters(t *testing.T) {
	if EqualJSON([]any{"a", "b"}, []any{"b", "a"}) {
		t.Fatal("array order is meaningful and must still be compared in order")
	}
}

func TestEqualJSON_NumbersCompareByValueAcrossDecodedAndLiteralForms(t *testing.T) {
	var decoded any
	dec := json.NewDecoder(stringReader(`{"n":1}`))
	dec.UseNumber()
	if err := dec.Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	if !EqualJSON(decoded, map[string]any{"n": 1}) {
		t.Fatal("a json.Number from disk must equal an int literal denoting the same number")
	}
}

func TestEqualJSON_NotEqualOnExtraOrMissingKeys(t *testing.T) {
	a := map[string]any{"command": "landfall"}
	b := map[string]any{"command": "landfall", "args": []any{"serve"}}
	if EqualJSON(a, b) || EqualJSON(b, a) {
		t.Fatal("a superset is not equal in either direction")
	}
}

func TestPlanInstall_ClassifiesWriteAlreadyInstalledAndConflict(t *testing.T) {
	dir := t.TempDir()
	keyPath := []string{"mcpServers", "landfall"}

	fresh := filepath.Join(dir, "fresh.json")
	if p, _ := PlanInstall(fresh, keyPath, LandfallEntry()); p.Action != ActionWrite {
		t.Fatalf("absent key must be `write`, got %q", p.Action)
	}

	installed := filepath.Join(dir, "installed.json")
	writeJSONFile(t, installed, map[string]any{"mcpServers": map[string]any{"landfall": LandfallEntry()}})
	if p, _ := PlanInstall(installed, keyPath, LandfallEntry()); p.Action != ActionAlreadyInstalled {
		t.Fatalf("our own entry must be `already-installed`, got %q", p.Action)
	}

	conflicting := filepath.Join(dir, "conflict.json")
	writeJSONFile(t, conflicting, map[string]any{"mcpServers": map[string]any{"landfall": map[string]any{"command": "other"}}})
	if p, _ := PlanInstall(conflicting, keyPath, LandfallEntry()); p.Action != ActionConflict {
		t.Fatalf("a differing entry must be `conflict`, got %q", p.Action)
	}
}

func TestApplyInstall_WritesOnlyWhenAbsentAndPreservesEveryUnrelatedKey(t *testing.T) {
	p := filepath.Join(t.TempDir(), "mcp.json")
	writeJSONFile(t, p, map[string]any{
		"mcpServers":       map[string]any{"other": map[string]any{"command": "foo", "args": []any{"bar"}}},
		"someOtherSetting": true,
		"aNumber":          42,
	})

	if _, err := ApplyInstall(p, []string{"mcpServers", "landfall"}, LandfallEntry()); err != nil {
		t.Fatal(err)
	}
	after := readJSONFile(t, p)
	servers := after["mcpServers"].(map[string]any)
	if !EqualJSON(servers["landfall"], LandfallEntry()) {
		t.Fatalf("landfall entry missing/wrong: %v", servers["landfall"])
	}
	if !EqualJSON(servers["other"], map[string]any{"command": "foo", "args": []any{"bar"}}) {
		t.Fatalf("a neighbouring server was disturbed: %v", servers["other"])
	}
	if after["someOtherSetting"] != true {
		t.Fatal("an unrelated top-level setting must survive")
	}
	// An integer must not come back as 42 or 4.2e+01 — third-party values are
	// carried through, not reformatted.
	if !contains(readText(t, p), "\"aNumber\": 42") {
		t.Fatalf("number reformatted: %s", readText(t, p))
	}
}

func TestApplyInstall_ConflictWritesNothing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "mcp.json")
	writeJSONFile(t, p, map[string]any{"mcpServers": map[string]any{"landfall": map[string]any{"command": "some-other-thing"}}})
	before := readText(t, p)

	plan, err := ApplyInstall(p, []string{"mcpServers", "landfall"}, LandfallEntry())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != ActionConflict {
		t.Fatalf("want conflict, got %q", plan.Action)
	}
	if readText(t, p) != before {
		t.Fatal("a conflict must not write one byte")
	}
}

func TestApplyUninstall_RemovesOnlyOurEntryAndLeavesAHandEditedOne(t *testing.T) {
	dir := t.TempDir()
	keyPath := []string{"mcpServers", "landfall"}

	ours := filepath.Join(dir, "ours.json")
	writeJSONFile(t, ours, map[string]any{"mcpServers": map[string]any{
		"landfall": LandfallEntry(),
		"other":    map[string]any{"command": "other-tool"},
	}, "unrelatedTopLevelSetting": "keep-me"})
	r, err := ApplyUninstall(ours, keyPath, LandfallEntry())
	if err != nil || r.Action != ActionRemoved {
		t.Fatalf("want removed, got %q %v", r.Action, err)
	}
	after := readJSONFile(t, ours)
	servers := after["mcpServers"].(map[string]any)
	if _, still := servers["landfall"]; still {
		t.Fatal("our entry must be gone")
	}
	if !EqualJSON(servers["other"], map[string]any{"command": "other-tool"}) {
		t.Fatal("a neighbouring server must survive")
	}
	if after["unrelatedTopLevelSetting"] != "keep-me" {
		t.Fatal("unrelated settings must survive")
	}

	edited := filepath.Join(dir, "edited.json")
	handEdited := map[string]any{"command": "landfall", "args": []any{"serve", "--verbose"}}
	writeJSONFile(t, edited, map[string]any{"mcpServers": map[string]any{"landfall": handEdited}})
	before := readText(t, edited)
	r, err = ApplyUninstall(edited, keyPath, LandfallEntry())
	if err != nil || r.Action != ActionLeftInPlace {
		t.Fatalf("want left-in-place, got %q %v", r.Action, err)
	}
	if readText(t, edited) != before {
		t.Fatal("a hand-edited entry must be left byte-identical, not deleted")
	}
}

func TestApplyUninstall_NotInstalledWritesNothingAndDoesNotCreateTheFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "never-existed.json")
	r, err := ApplyUninstall(p, []string{"mcpServers", "landfall"}, LandfallEntry())
	if err != nil || r.Action != ActionNotInstalled {
		t.Fatalf("want not-installed, got %q %v", r.Action, err)
	}
	if fileExists(p) {
		t.Fatal("uninstall must not create a config file")
	}
}

func TestInstallThenUninstall_RoundTripsToTheFileAsFound(t *testing.T) {
	p := filepath.Join(t.TempDir(), "mcp.json")
	writeJSONFile(t, p, map[string]any{"mcpServers": map[string]any{}})
	before := readText(t, p)

	if _, err := ApplyInstall(p, []string{"mcpServers", "landfall"}, LandfallEntry()); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyUninstall(p, []string{"mcpServers", "landfall"}, LandfallEntry()); err != nil {
		t.Fatal(err)
	}
	if got := readText(t, p); got != before {
		t.Fatalf("round trip changed the file:\n got %q\nwant %q", got, before)
	}
}

func TestApplyInstall_IsIdempotent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "mcp.json")
	if _, err := ApplyInstall(p, []string{"mcpServers", "landfall"}, LandfallEntry()); err != nil {
		t.Fatal(err)
	}
	first := readText(t, p)
	plan, err := ApplyInstall(p, []string{"mcpServers", "landfall"}, LandfallEntry())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != ActionAlreadyInstalled {
		t.Fatalf("second run must be already-installed, got %q", plan.Action)
	}
	if readText(t, p) != first {
		t.Fatal("second run must not rewrite the file")
	}
}

func TestApplyInstall_ReorderedKeysAreRecognizedAsOursNotAsAConflict(t *testing.T) {
	// The observable consequence of FR-014: an entry whose keys someone (or
	// some formatter) reordered is still ours — installable-idempotent AND
	// removable, where the Node CLI would call it a conflict forever.
	p := filepath.Join(t.TempDir(), "mcp.json")
	if err := os.WriteFile(p, []byte(`{"mcpServers":{"landfall":{"args":["serve"],"command":"landfall"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanInstall(p, []string{"mcpServers", "landfall"}, LandfallEntry())
	if err != nil || plan.Action != ActionAlreadyInstalled {
		t.Fatalf("want already-installed, got %q %v", plan.Action, err)
	}
	r, err := ApplyUninstall(p, []string{"mcpServers", "landfall"}, LandfallEntry())
	if err != nil || r.Action != ActionRemoved {
		t.Fatalf("want removed, got %q %v", r.Action, err)
	}
}

// --- small helpers -------------------------------------------------------

func asUnparseable(err error, target **UnparseableConfigError) bool {
	return errors.As(err, target)
}

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }

func stringReader(s string) *strings.Reader { return strings.NewReader(s) }
