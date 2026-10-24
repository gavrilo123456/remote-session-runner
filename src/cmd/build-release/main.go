package main

import (
	"os"
	"remote-session-runner/src/internal/commandstub"
	"remote-session-runner/src/internal/releaseflow"
)

func main() {
	args := os.Args[1:]
	if len(args) == 1 && args[0] == "--version" {
		os.Exit(commandstub.Run("build-release", "Freeze and build four native Runner release bundles without activation.", args, os.Stdout, os.Stderr))
	}
	os.Exit(releaseflow.RunBuildCLI(args, os.Stdout, os.Stderr))
}
