package main

import (
	"os"

	"remote-session-runner/src/internal/runnerlocal"
)

func main() {
	os.Exit(runnerlocal.Run(os.Args[1:], os.Stdout, os.Stderr))
}
