// Command sendaway runs matched build and test commands on a remote Linux host.
package main

import (
	"os"

	"github.com/libraz/sendaway/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args))
}
