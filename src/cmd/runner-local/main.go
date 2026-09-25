package main

import (
	"os"

	"remote-session-runner/src/internal/commandstub"
)

func main() {
	os.Exit(commandstub.Run("runner-local", "Mac-local API, mailbox, router, and dispatcher.", os.Args[1:], os.Stdout, os.Stderr))
}
