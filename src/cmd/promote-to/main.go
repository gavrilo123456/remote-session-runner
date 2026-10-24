package main

import (
	"os"
	"remote-session-runner/src/internal/commandstub"
	"remote-session-runner/src/internal/releaseflow"
)

func main() {
	args := os.Args[1:]
	if len(args) == 1 && args[0] == "--version" {
		os.Exit(commandstub.Run("promote-to", "Plan or promote one sealed Runner release through guarded Dev, QAS, and PRD cohorts.", args, os.Stdout, os.Stderr))
	}
	os.Exit(releaseflow.RunPromoteCLI(args, os.Stdout, os.Stderr))
}
