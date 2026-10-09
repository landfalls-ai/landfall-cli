package cli

// openurl.go: `landfall open <url> [--host]` (console spec §4.6, round 5 review
// issue 1): the console's `o: open in browser` on an incident. The mod never
// opens a browser itself; this command does, the way `landfall login` already
// does (pkg/browser), and answers one JSON line so the console can say what
// happened: {"ok":true} once the browser was asked, {"ok":false,"error"} when
// it could not be (a headless box, no browser), which the console turns into
// `Could not open your browser: <reason>` and a link row the person can use.
//
// It opens only the Landfall this CLI points at: an https (or http, for a
// local instance) address on the instance's own web host. Anything else is
// refused, so a key press can never open an arbitrary address.

import (
	"net/url"
	"os"
	"strings"

	"github.com/landfalls-ai/landfall-cli/internal/instance"
	"github.com/pkg/browser"
	"github.com/spf13/cobra"
)

// OpenURLDeps are open's effects: the environment and the browser.
type OpenURLDeps struct {
	Env  func(string) string
	Open func(string) error
}

func newOpenCommand(ui *UI) *cobra.Command {
	var host string
	c := newCommand(ui, "open", func(_ *cobra.Command, args []string) error {
		target := ""
		if len(args) > 0 {
			target = args[0]
		}
		return printAnswer(ui.Out, RunOpenURL(OpenURLDeps{}, target))
	})
	c.Flags().StringVar(&host, "host", "", "the agent host this runs for (accepted, not used)")
	c.FParseErrWhitelist.UnknownFlags = true
	c.Hidden = true
	return c
}

// RunOpenURL opens target in the person's browser when it is an address on the
// web host of the Landfall this CLI points at.
func RunOpenURL(d OpenURLDeps, target string) map[string]any {
	getenv := d.Env
	if getenv == nil {
		getenv = os.Getenv
	}
	open := d.Open
	if open == nil {
		open = browser.OpenURL
	}
	inst, err := instance.Resolve(instance.Options{Env: instance.Env(getenv)})
	if err != nil {
		return failure("The Landfall address this CLI is set to is not valid.")
	}
	u, err := url.Parse(strings.TrimSpace(target))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return failure("That is not a web address.")
	}
	web, err := url.Parse(trimSlash(inst.Web))
	if err != nil || !strings.EqualFold(u.Host, web.Host) {
		return failure("Only addresses on " + web.Host + " open from here.")
	}
	if err := open(u.String()); err != nil {
		return failure("no browser answered (" + firstLine(err.Error()) + ")")
	}
	return map[string]any{"ok": true}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}
