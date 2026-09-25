package main

import (
	"os"

	"remote-session-runner/src/internal/commandstub"
)

func main() {
	os.Exit(commandstub.Run("runner-ssh-bridge", "Restricted SSH transport adapter.", os.Args[1:], os.Stdout, os.Stderr))
}
