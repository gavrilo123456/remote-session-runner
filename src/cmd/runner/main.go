package main

import (
	"os"

	"remote-session-runner/src/internal/runnercli"
)

func main() {
	os.Exit(runnercli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
