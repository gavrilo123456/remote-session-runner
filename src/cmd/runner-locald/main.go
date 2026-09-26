package main

import (
	"os"

	"remote-session-runner/src/internal/commandstub"
	"remote-session-runner/src/internal/runnerlocald"
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 || (len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help" || args[0] == "--version" || args[0] == "version")) {
		os.Exit(commandstub.Run("runner-locald", "Mac-local execution worker and private API.", args, os.Stdout, os.Stderr))
	}
	os.Exit(runnerlocald.Run(args, os.Stdout, os.Stderr))
}
