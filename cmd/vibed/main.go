package main

import (
	"os"

	"github.com/vibed-project/vibeD/pkg/server"
)

func main() {
	// Subcommands (must be handled before flag.Parse, which can't see them).
	if len(os.Args) > 1 && os.Args[1] == "validate-images" {
		os.Exit(runValidateImages(os.Args[2:]))
	}

	// Standard bootstrap (flags, config, logger, run) lives in pkg/server so
	// out-of-tree editions reuse it verbatim.
	server.Main()
}
