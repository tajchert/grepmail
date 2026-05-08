// grepmail is a fast CLI for searching, reading, and exploring mbox files.
//
// The binary itself is a thin shim: all command dispatch and option
// handling lives in internal/cli, which makes the surface easy to test
// without invoking os.Exit.
package main

import (
	"os"

	"github.com/tajchert/grepmail/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
