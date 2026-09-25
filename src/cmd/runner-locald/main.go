package main

import (
	"os"

	"remote-session-runner/src/internal/commandstub"
)

func main() {
	os.Exit(commandstub.Run("runner-locald", "Mac-local execution worker.", os.Args[1:], os.Stdout, os.Stderr))
}
