// Package commandstub implements the help and version surface used by the P001
// executable skeletons. Runtime operations are added in later phases.
package commandstub

import (
	"fmt"
	"io"
)

const Version = "0.0.0-dev"

// Run handles the only supported P001 command-line arguments and returns an
// exit status for the small executable entrypoints.
func Run(name, summary string, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help")) {
		fmt.Fprintf(stdout, "Usage: %s [--help|--version]\n%s\n", name, summary)
		return 0
	}

	if len(args) == 1 && (args[0] == "--version" || args[0] == "version") {
		fmt.Fprintf(stdout, "%s %s\n", name, Version)
		return 0
	}

	fmt.Fprintf(stderr, "%s: P001 stub; runtime operations are not implemented\n", name)
	return 2
}
