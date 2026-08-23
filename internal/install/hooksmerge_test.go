package install

// hooksmerge_test.go — the list-element merge core (T048). The four states,
// the all-or-nothing install, the selective uninstall, and the pruning that
// makes an install→uninstall round trip leave the file as it was found.

import (
	"os"
	"path/filepath"
	"testing"
)

// The Claude-Code-shaped fixtures every case here uses.
func stopEntry(command string) map[string]any {
	return map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command}}}
}

func stopRegistration() Registration {
	return Registration{KeyPath: []string{"hooks", "Stop"}, Entry: stopEntry("landfall hooks stop")}
}

func hookIsOurs(element any) bool {
	m, ok := element.(map[string]any)
	if !ok {
		return false
	}
	list, ok := m["hooks"].([]any)
	if !ok {
		return false
	}
	for _, h := range list {
		hm, ok := h.(map[string]any)
		if !ok {
			continue
		}
		if cmd, _ := hm["command"].(string); len(cmd) >= 15 && cmd[:15] == "landfall hooks " {
			return true
		}
	}
	return false
}

var userHook = map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "my-own-script.sh"}}}

func TestClassify_TheFourStatesAndNoFifth(t *testing.T) {
	r := stopRegistration()
	r.Superseded = []any{stopEntry("landfall hooks stop --old")}

	cases := []struct {
		name string
		list []any
		want string
	}{
		{"absent when the list holds only somebody else's hook", []any{userHook}, StateAbsent},
		{"absent when there is no list at all", nil, StateAbsent},
		{"present on an exact match", []any{r.Entry}, StatePresent},
		{"outdated on a past form", []any{stopEntry("landfall hooks stop --old")}, StateOutdated},
		{"conflict on ours-but-edited", []any{stopEntry("landfall hooks stop --verbose")}, StateConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := map[string]any{}
			if tc.list != nil {
				data = map[string]any{"hooks": map[string]any{"Stop": tc.list}}
			}
			if got := Classify(data, r, hookIsOurs); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestClassify_OutdatedBeatsPresentSoAStaleCopyIsNeverLeftBehind(t *testing.T) {
	r := stopRegistration()
	r.Superseded = []any{stopEntry("landfall hooks stop --old")}
	data := map[string]any{"hooks": map[string]any{"Stop": []any{
		r.Entry,
		stopEntry("landfall hooks stop --old"),
	}}}
	if got := Classify(data, r, hookIsOurs); got != StateOutdated {
		t.Fatalf("a file holding both the current AND a stale entry is still work to do, got %q", got)
	}
}

func TestApplyHookInstall_AppendsAndLeavesEveryOtherElementUntouched(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	writeJSONFile(t, p, map[string]any{"model": "opus", "hooks": map[string]any{"Stop": []any{userHook}}})

	if _, err := ApplyHookInstall(p, []Registration{stopRegistration()}, hookIsOurs); err != nil {
		t.Fatal(err)
	}
	after := readJSONFile(t, p)
	if after["model"] != "opus" {
		t.Fatal("unrelated settings must survive")
	}
	list := after["hooks"].(map[string]any)["Stop"].([]any)
	if len(list) != 2 {
		t.Fatalf("want 2 elements, got %d", len(list))
	}
	if !EqualJSON(list[0], userHook) {
		t.Fatal("the user's own hook must stay FIRST and untouched")
	}
	if !EqualJSON(list[1], stopEntry("landfall hooks stop")) {
		t.Fatalf("ours must be appended, got %v", list[1])
	}
}

func TestApplyHookInstall_IsIdempotentAndNeverDuplicates(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	if _, err := ApplyHookInstall(p, []Registration{stopRegistration()}, hookIsOurs); err != nil {
		t.Fatal(err)
	}
	first := readText(t, p)
	plan, err := ApplyHookInstall(p, []Registration{stopRegistration()}, hookIsOurs)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != ActionAlreadyInstalled {
		t.Fatalf("want already-installed, got %q", plan.Action)
	}
	if readText(t, p) != first {
		t.Fatal("the second run must not rewrite the file")
	}
	list := readJSONFile(t, p)["hooks"].(map[string]any)["Stop"].([]any)
	if len(list) != 1 {
		t.Fatalf("want exactly one entry, got %d", len(list))
	}
}

func TestApplyHookInstall_UpgradesAStaleEntryInPlaceKeepingItsPositionAndCollapsingDuplicates(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	r := stopRegistration()
	r.Superseded = []any{stopEntry("landfall hooks stop --old")}
	writeJSONFile(t, p, map[string]any{"hooks": map[string]any{"Stop": []any{
		stopEntry("landfall hooks stop --old"),
		userHook,
		stopEntry("landfall hooks stop --old"), // a second stale copy
	}}})

	result, err := ApplyHookInstall(p, []Registration{r}, hookIsOurs)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != ActionConfigured {
		t.Fatalf("want configured, got %q", result.Action)
	}
	list := readJSONFile(t, p)["hooks"].(map[string]any)["Stop"].([]any)
	if len(list) != 2 {
		t.Fatalf("duplicates must collapse to one: %v", list)
	}
	if !EqualJSON(list[0], r.Entry) {
		t.Fatalf("the upgraded entry must keep its original position, got %v", list[0])
	}
	if !EqualJSON(list[1], userHook) {
		t.Fatal("the user's hook must keep its position too")
	}
}

func TestApplyHookInstall_AConflictWritesNotOneByteAnywhereIncludingForCleanRegistrations(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	writeJSONFile(t, p, map[string]any{"hooks": map[string]any{
		"Stop": []any{stopEntry("landfall hooks stop --verbose")}, // ours, hand-edited
	}})
	before := readText(t, p)

	regs := []Registration{
		stopRegistration(),
		// A second, entirely clean registration that WOULD have been written.
		{KeyPath: []string{"hooks", "UserPromptSubmit"}, Entry: stopEntry("landfall hooks user-prompt-submit")},
	}
	plan, err := ApplyHookInstall(p, regs, hookIsOurs)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != ActionConflict {
		t.Fatalf("want conflict, got %q", plan.Action)
	}
	if readText(t, p) != before {
		t.Fatal("install is all-or-nothing: a conflict on one registration blocks every other")
	}
}

func TestApplyHookUninstall_IsSelectiveWhereInstallIsAllOrNothing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	writeJSONFile(t, p, map[string]any{"hooks": map[string]any{
		"Stop":             []any{stopEntry("landfall hooks stop --hand-edited")},
		"UserPromptSubmit": []any{stopEntry("landfall hooks user-prompt-submit")},
	}})
	regs := []Registration{
		stopRegistration(),
		{KeyPath: []string{"hooks", "UserPromptSubmit"}, Entry: stopEntry("landfall hooks user-prompt-submit")},
	}

	result, err := ApplyHookUninstall(p, regs, hookIsOurs)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != ActionRemove && result.Action != ActionRemoved {
		t.Fatalf("something of ours WAS removed, so the file-level action is removed: got %q", result.Action)
	}
	after := readJSONFile(t, p)["hooks"].(map[string]any)
	if _, still := after["UserPromptSubmit"]; still {
		t.Fatal("the clean registration must be removed and its emptied list pruned")
	}
	list := after["Stop"].([]any)
	if len(list) != 1 || !EqualJSON(list[0], stopEntry("landfall hooks stop --hand-edited")) {
		t.Fatalf("the hand-edited entry must survive untouched, got %v", list)
	}
}

func TestApplyHookUninstall_LeftInPlaceWhenEveryRegistrationIsAConflict(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	writeJSONFile(t, p, map[string]any{"hooks": map[string]any{"Stop": []any{stopEntry("landfall hooks stop --verbose")}}})
	before := readText(t, p)

	plan, err := ApplyHookUninstall(p, []Registration{stopRegistration()}, hookIsOurs)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != ActionLeftInPlace {
		t.Fatalf("want left-in-place, got %q", plan.Action)
	}
	if readText(t, p) != before {
		t.Fatal("nothing may be written when there is nothing safe to remove")
	}
}

func TestApplyHookUninstall_RemovesAStaleEntryAnOlderVersionWrote(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	r := stopRegistration()
	r.Superseded = []any{stopEntry("landfall hooks stop --old")}
	writeJSONFile(t, p, map[string]any{"hooks": map[string]any{"Stop": []any{stopEntry("landfall hooks stop --old")}}})

	if _, err := ApplyHookUninstall(p, []Registration{r}, hookIsOurs); err != nil {
		t.Fatal(err)
	}
	if got := readJSONFile(t, p); len(got) != 0 {
		t.Fatalf(`"uninstall removed everything landfall added" must include an older version's entry, got %v`, got)
	}
}

func TestApplyHookUninstall_PrunesAnEmptiedListAndItsNowEmptyParent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	if _, err := ApplyHookInstall(p, []Registration{stopRegistration()}, hookIsOurs); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyHookUninstall(p, []Registration{stopRegistration()}, hookIsOurs); err != nil {
		t.Fatal(err)
	}
	after := readJSONFile(t, p)
	if len(after) != 0 {
		t.Fatalf(`no "Stop": [] and no empty "hooks" container may be left behind, got %v`, after)
	}
}

func TestApplyHookUninstall_KeepsAListThatStillHoldsSomebodyElsesHook(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	writeJSONFile(t, p, map[string]any{"hooks": map[string]any{"Stop": []any{userHook, stopEntry("landfall hooks stop")}}})

	if _, err := ApplyHookUninstall(p, []Registration{stopRegistration()}, hookIsOurs); err != nil {
		t.Fatal(err)
	}
	list := readJSONFile(t, p)["hooks"].(map[string]any)["Stop"].([]any)
	if len(list) != 1 || !EqualJSON(list[0], userHook) {
		t.Fatalf("ours gone, theirs untouched: got %v", list)
	}
}

func TestPlanHookInstall_NeverWritesAndRefusesAnUnparseableFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	const original = "{ this is not json"
	if err := os.WriteFile(p, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanHookInstall(p, []Registration{stopRegistration()}, hookIsOurs); err == nil {
		t.Fatal("want an error")
	}
	if _, err := ApplyHookInstall(p, []Registration{stopRegistration()}, hookIsOurs); err == nil {
		t.Fatal("want an error")
	}
	if readText(t, p) != original {
		t.Fatal("an unparseable file is reported, never overwritten")
	}
}

func TestPlanHookUninstall_NotInstalledOnACleanMachine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	plan, err := PlanHookUninstall(p, []Registration{stopRegistration()}, hookIsOurs)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != ActionNotInstalled {
		t.Fatalf("want not-installed, got %q", plan.Action)
	}
	if fileExists(p) {
		t.Fatal("planning must not create the file")
	}
}
