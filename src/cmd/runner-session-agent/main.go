package main

import (
	"os"

	"remote-session-runner/src/internal/commandstub"
)

func main() {
	os.Exit(commandstub.Run("runner-session-agent", "Persistent Bash session supervisor.", os.Args[1:], os.Stdout, os.Stderr))
}
