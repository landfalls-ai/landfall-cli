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

// version is the build version, stamped in at link time:
//
//	go build -ldflags "-X main.version=$(git describe --tags --always)" ./cmd/landfall
//
// It is the ONLY place a release version is recorded, and `landfall serve`
// reports it as the MCP server's own `serverInfo.version` — bin/landfall.mjs:487
// hardcoded `'0.2.0'` there, which had already drifted three minor releases
// behind package.json. An un-stamped build (`go run`, `go test`, a plain
// `go build`) keeps "dev", which is a true statement about it.
var version = "dev"

func main() {
	cli.SetVersion(version)
	os.Exit(cli.Execute(os.Args[1:]))
}
