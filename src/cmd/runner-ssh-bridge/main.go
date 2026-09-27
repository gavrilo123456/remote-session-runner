package main

import (
	"os"

	"remote-session-runner/src/internal/commandstub"
	"remote-session-runner/src/internal/sshbridge"
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 || (len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help" || args[0] == "--version" || args[0] == "version")) {
		os.Exit(commandstub.Run("runner-ssh-bridge", "Restricted SSH transport adapter to the private runnerd socket.", args, os.Stdout, os.Stderr))
	}
	os.Exit(sshbridge.Run(args, os.Stdin, os.Stdout, os.Stderr))
}
