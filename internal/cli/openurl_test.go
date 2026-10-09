package cli

import (
	"errors"
	"testing"
)

func openEnv(k string) string {
	if k == "LANDFALL_URL" {
		return "https://app.landfalls.ai"
	}
	return ""
}

func TestRunOpenURLOpensTheInstancesOwnPages(t *testing.T) {
	var opened []string
	d := OpenURLDeps{Env: openEnv, Open: func(u string) error { opened = append(opened, u); return nil }}
	ans := RunOpenURL(d, "https://app.landfalls.ai/o/acme/incidents/i171")
	if ans["ok"] != true {
		t.Fatalf("ok = %v, %v", ans["ok"], ans["error"])
	}
	if len(opened) != 1 || opened[0] != "https://app.landfalls.ai/o/acme/incidents/i171" {
		t.Fatalf("opened %v", opened)
	}
}

func TestRunOpenURLRefusesOtherHostsAndSchemes(t *testing.T) {
	d := OpenURLDeps{Env: openEnv, Open: func(string) error { t.Fatal("opened"); return nil }}
	for _, target := range []string{"https://evil.example.com/x", "file:///etc/passwd", "javascript:alert(1)", ""} {
		if ans := RunOpenURL(d, target); ans["ok"] != false {
			t.Fatalf("%q opened: %v", target, ans)
		}
	}
}

func TestRunOpenURLSaysWhenNoBrowserAnswers(t *testing.T) {
	d := OpenURLDeps{Env: openEnv, Open: func(string) error { return errors.New("exec: \"xdg-open\": not found") }}
	ans := RunOpenURL(d, "https://app.landfalls.ai/o/acme/incidents/i171")
	if ans["ok"] != false || ans["error"] != `no browser answered (exec: "xdg-open": not found)` {
		t.Fatalf("answer %v", ans)
	}
}
