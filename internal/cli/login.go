package cli

// login.go — `landfall login [--url <address>] [--save] [--json]`, a port of
// bin/landfall.mjs:224-239.
//
// Both instance flags are opt-in, and that is the whole point: a customer who
// passes neither reaches the hosted service. `--url` points THIS sign-in at a
// specific Landfall; `--save` also makes it the default for later commands.
//
// `--json` is for the Claude Code mod's sign-in state (console spec §2.6,
// §9): stdout carries one JSON line as soon as the handoff URL is known,
// {"event":"url","url":"…"}, then one final line, {"ok":true,"org","web"} or
// {"ok":false,"error":"<sentence>"}. Everything else is exactly what a plain
// `landfall login` does: the same stderr lines, the CLI opens the browser
// itself, the same exit code. `--host` is accepted and ignored, as every mod
// command takes it.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/landfalls-ai/landfall-cli/internal/auth"
	"github.com/landfalls-ai/landfall-cli/internal/instance"
	"github.com/spf13/cobra"
)

func newLoginCommand(ui *UI) *cobra.Command {
	var (
		url     string
		save    bool
		jsonOut bool
		host    string
	)
	c := newCommand(ui, "login", func(cmd *cobra.Command, _ []string) error {
		return runLogin(cmd.Context(), ui, loginArgs{URL: url, Save: save, JSON: jsonOut}, auth.LoginOptions{})
	})
	c.Flags().StringVar(&url, "url", "", "sign in to a specific Landfall instance")
	c.Flags().BoolVar(&save, "save", false, "also make that instance the default")
	c.Flags().BoolVar(&jsonOut, "json", false, "print the sign-in URL and the outcome as JSON lines on stdout")
	c.Flags().StringVar(&host, "host", "", "the agent host this runs for (accepted, not used)")
	// bin/landfall.mjs scans `rest` for these two and ignores everything else,
	// so an unrecognized flag is not an error today. Tolerating them keeps the
	// exit code identical (0, not 2) for a script that passes one.
	c.FParseErrWhitelist = cobra.FParseErrWhitelist{UnknownFlags: true}
	return c
}

// loginArgs are the command's flags.
type loginArgs struct {
	URL  string
	Save bool
	JSON bool
}

// runLogin signs in. base carries the test seams (HTTP client, browser,
// poll pacing); production passes the zero value.
func runLogin(ctx context.Context, ui *UI, args loginArgs, base auth.LoginOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	emit := func(v map[string]any) {
		if !args.JSON {
			return
		}
		body, err := json.Marshal(v)
		if err != nil {
			body = []byte(`{"ok":false,"error":"The answer could not be written."}`)
		}
		_, _ = fmt.Fprintln(ui.Out, string(body))
	}
	fail := func(err error) error {
		emit(map[string]any{"ok": false, "error": loginErrorSentence(err)})
		return err
	}

	// Resolved ONCE and reused for the sign-in and the nomination: resolving
	// twice is how a login could probe one origin and save another.
	inst, err := instance.Resolve(instance.Options{URL: args.URL})
	if err != nil {
		return fail(err)
	}

	opts := base
	opts.URL = args.URL
	opts.Instance = &inst
	if args.JSON {
		opts.OnURL = func(u string) { emit(map[string]any{"event": "url", "url": u}) }
	}
	if _, err := auth.Login(ctx, func(msg string) { ui.Log("%s", msg) }, opts); err != nil {
		return fail(err)
	}

	if args.Save {
		if _, err := instance.SaveNomination(instance.Instance{
			Name: inst.Name, Web: inst.Web, API: inst.API, Docs: inst.Docs,
		}); err != nil {
			return fail(err)
		}
		ui.Log("saved %s as your Landfall. Undo with `landfall instance reset`.", inst.Web)
	}

	ui.Log("signed in — session cached. You can now join a war room with no share link.")
	emit(map[string]any{"ok": true, "org": auth.GetCachedOrgSlug(), "web": strings.TrimRight(inst.Web, "/")})
	return nil
}

// loginErrorSentence is a failure as one line. The handoff timeout keeps its
// exact words (the mod recognizes them and says "Your browser did not finish
// within 5 minutes."); every other reason is the CLI's own message, an
// unreachable instance's included, with its fix lines folded onto one line.
func loginErrorSentence(err error) string {
	if err == nil {
		return "Sign-in did not finish."
	}
	s := strings.Join(strings.Fields(err.Error()), " ")
	if s == "" {
		return "Sign-in did not finish."
	}
	return s
}
