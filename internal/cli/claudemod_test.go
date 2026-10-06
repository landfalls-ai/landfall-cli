package cli

import (
	"errors"
	"strings"
	"testing"
)

type fakeClaudeMod struct {
	version    string
	versionErr error
	installed  bool
	installErr error
	installs   int
	updates    int
	uninstalls int
}

func (f *fakeClaudeMod) Version() (string, error) { return f.version, f.versionErr }
func (f *fakeClaudeMod) Installed() (bool, error) { return f.installed, nil }
func (f *fakeClaudeMod) Install() error {
	f.installs++
	if f.installErr == nil {
		f.installed = true
	}
	return f.installErr
}
func (f *fakeClaudeMod) Update() error    { f.updates++; return nil }
func (f *fakeClaudeMod) Uninstall() error { f.uninstalls++; f.installed = false; return nil }

func TestVersionAtLeast(t *testing.T) {
	cases := map[string]bool{
		"2.1.287": true, "2.1.291": true, "2.2.0": true, "3.0.0": true,
		"2.1.286": false, "2.1.285": false, "1.9.999": false, "garbage": false,
	}
	for v, want := range cases {
		if got := versionAtLeast(v, MinClaudeModVersion); got != want {
			t.Errorf("versionAtLeast(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestInstallClaudeModSkipsAnOldClaudeCode(t *testing.T) {
	mod := &fakeClaudeMod{version: "2.1.285"}
	o := installClaudeMod(mod, false)
	if o.Status != "skipped" || !strings.Contains(o.Detail, "2.1.287") {
		t.Fatalf("got %s %q, want skipped naming the minimum version", o.Status, o.Detail)
	}
	if mod.installs != 0 {
		t.Fatal("an old Claude Code must not get the mod")
	}
}

func TestInstallClaudeModInstallsOnceAndIsIdempotent(t *testing.T) {
	mod := &fakeClaudeMod{version: "2.1.291"}
	if o := installClaudeMod(mod, false); o.Status != "configured" {
		t.Fatalf("first run = %s, want configured", o.Status)
	}
	if o := installClaudeMod(mod, false); o.Status != "already-installed" {
		t.Fatalf("second run = %s, want already-installed", o.Status)
	}
	if mod.installs != 1 {
		t.Fatalf("installed %d times, want once", mod.installs)
	}
	// An installed mod is moved to its latest version, so a CLI upgrade carries its fixes.
	if mod.updates != 1 {
		t.Fatalf("updated %d times, want once on the second run", mod.updates)
	}
}

func TestInstallClaudeModDryRunWritesNothing(t *testing.T) {
	mod := &fakeClaudeMod{version: "2.1.291"}
	if o := installClaudeMod(mod, true); o.Status != "would-configure" || mod.installs != 0 {
		t.Fatalf("dry run = %s with %d installs", o.Status, mod.installs)
	}
}

func TestInstallClaudeModReportsAFailure(t *testing.T) {
	mod := &fakeClaudeMod{version: "2.1.291", installErr: errors.New("marketplace unreachable")}
	if o := installClaudeMod(mod, false); o.Status != "failed" || o.Detail != "marketplace unreachable" {
		t.Fatalf("got %s %q", o.Status, o.Detail)
	}
}

func TestUninstallClaudeModOnlyWhenInstalled(t *testing.T) {
	mod := &fakeClaudeMod{version: "2.1.291"}
	if _, acted := uninstallClaudeMod(mod); acted {
		t.Fatal("nothing installed, nothing to report")
	}
	mod.installed = true
	o, acted := uninstallClaudeMod(mod)
	if !acted || o.Status != "removed" || mod.uninstalls != 1 {
		t.Fatalf("got %v %s, %d uninstalls", acted, o.Status, mod.uninstalls)
	}
}

func TestJSONPartSkipsAWarningLine(t *testing.T) {
	if got := jsonPart("warning: something\n[{\"id\":\"x\"}]"); got != `[{"id":"x"}]` {
		t.Fatalf("got %q", got)
	}
}
