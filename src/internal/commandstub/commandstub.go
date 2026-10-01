// Package commandstub implements the help and version surface used by the P001
// executable skeletons. Runtime operations are added in later phases.
package commandstub

import (
	"fmt"
	"io"

	"remote-session-runner/src/internal/buildinfo"
)

const Version = "0.0.0-dev"

// VersionString is shared by every executable version surface so an operator
// can distinguish an ordinary development build from one embedded by an
// installer at a specific source revision.
func VersionString(name string) string {
	return fmt.Sprintf("%s %s build_revision=%s", name, Version, buildinfo.Revision())
}

// Run handles the only supported P001 command-line arguments and returns an
// exit status for the small executable entrypoints.
func Run(name, summary string, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help")) {
		fmt.Fprintf(stdout, "Usage: %s [--help|--version]\n%s\n", name, summary)
		return 0
	}

	if len(args) == 1 && (args[0] == "--version" || args[0] == "version") {
		fmt.Fprintln(stdout, VersionString(name))
		return 0
	}

	fmt.Fprintf(stderr, "%s: P001 stub; runtime operations are not implemented\n", name)
	return 2
}
