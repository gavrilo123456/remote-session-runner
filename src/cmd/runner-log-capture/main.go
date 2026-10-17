package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"remote-session-runner/src/internal/commandstub"
	"remote-session-runner/src/internal/logcapture"
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 || (len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help" || args[0] == "--version" || args[0] == "version")) {
		os.Exit(commandstub.Run("runner-log-capture", "Bounded child stdout/stderr capture and durable drop counter.", args, os.Stdout, os.Stderr))
	}
	os.Exit(run(args))
}

func run(args []string) int {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	return logcapture.RunCLIContext(ctx, args, os.Stdout, os.Stderr)
}
