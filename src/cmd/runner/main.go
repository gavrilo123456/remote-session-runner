package main

import (
	"os"

	"remote-session-runner/src/internal/commandstub"
)

func main() {
	os.Exit(commandstub.Run("runner", "Remote Session Runner command-line client.", os.Args[1:], os.Stdout, os.Stderr))
}
