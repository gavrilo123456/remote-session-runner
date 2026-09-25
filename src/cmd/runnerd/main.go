package main

import (
	"os"

	"remote-session-runner/src/internal/commandstub"
)

func main() {
	os.Exit(commandstub.Run("runnerd", "Linux remote execution worker and private API.", os.Args[1:], os.Stdout, os.Stderr))
}
