package main

import (
	"os"
	"remote-session-runner/src/internal/commandstub"
	"remote-session-runner/src/internal/releaseflow"
)

func main() {
	args := os.Args[1:]
	if len(args) == 1 && args[0] == "--version" {
		os.Exit(commandstub.Run("runner-release", "Inspect durable release workflow records and perform supported rollback.", args, os.Stdout, os.Stderr))
		return
	}
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		os.Exit(releaseflow.RunAdminCLI([]string{"help"}, os.Stdout, os.Stderr))
		return
	}
	if len(args) > 0 && len(args[0]) >= 5 && args[0][:5] == "host-" {
		os.Exit(releaseflow.RunHostCLI(args, os.Stdout, os.Stderr))
		return
	}
	os.Exit(releaseflow.RunAdminCLI(args, os.Stdout, os.Stderr))
}
