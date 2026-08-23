// Command landfall is the entrypoint for the Landfall CLI.
//
// This file is deliberately thin: argv pre-processing (the bare/URL-as-serve
// grammar, --link splicing, and the strict --help pre-parse — OD-2) and all
// real command logic live in internal/cli. main only owns process exit,
// because Go's os.Exit has the same stdout-truncation-on-unflushed-writer
// hazard the Node CLI's own comments document (bin/landfall.mjs:432-437) —
// os.Exit must be the only exit path, called exactly once, after everything
// else has returned normally and flushed.
package main

import (
	"os"

	"github.com/landfalls-ai/landfall-cli/internal/cli"
)

func main() {
	os.Exit(cli.Execute(os.Args[1:]))
}
