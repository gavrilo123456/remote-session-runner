// runner-mailbox-handoff is a local, owner-scoped maintenance coordinator.
// It never submits Runner work and never installs or upgrades PRD binaries.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"remote-session-runner/src/internal/commandstub"
	"remote-session-runner/src/internal/laneinstall"
)

func main() { os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)) }

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help" || args[0] == "--version" || args[0] == "version") {
		return commandstub.Run("runner-mailbox-handoff", "Prepare, stop, activate, inspect or restore an owner-approved PRD to QAS mailbox handoff.", args, stdout, stderr)
	}
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: runner-mailbox-handoff prepare|status|stop|activate|restore [options]")
		return 2
	}
	if args[0] != "prepare" && args[0] != "status" && args[0] != "stop" && args[0] != "activate" && args[0] != "restore" {
		fmt.Fprintln(stderr, "unknown handoff action")
		return 2
	}
	flags := flag.NewFlagSet("runner-mailbox-handoff "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	owner := flags.String("owner", "", "owner confirming stopped mailbox clients")
	paused := flags.Bool("publishers-paused", false, "confirm all affected mailbox publishers are stopped")
	path := flags.String("operation-file", "", "owner-only operation.json from prepare")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		return 2
	}
	var op laneinstall.MailboxHandoffOperation
	var err error
	if args[0] == "prepare" {
		if *path != "" || *owner == "" || !*paused {
			fmt.Fprintln(stderr, "prepare requires --owner and --publishers-paused")
			return 2
		}
		op, err = laneinstall.PrepareMailboxHandoff(ctx, *owner, *paused)
	} else {
		if *path == "" || *owner != "" {
			fmt.Fprintln(stderr, "operation requires --operation-file")
			return 2
		}
		if args[0] != "status" && !*paused {
			fmt.Fprintln(stderr, "mutating operations require a fresh --publishers-paused confirmation")
			return 2
		}
		op, err = laneinstall.LoadMailboxHandoff(*path)
		if err == nil {
			switch args[0] {
			case "status":
			case "stop":
				err = laneinstall.StopMailboxHandoffConsumers(ctx, op)
			case "activate":
				op, err = laneinstall.ActivateMailboxHandoff(ctx, op)
			case "restore":
				op, err = laneinstall.RestoreMailboxHandoff(ctx, op)
			default:
				fmt.Fprintln(stderr, "unknown handoff action")
				return 2
			}
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "runner-mailbox-handoff: %v\n", err)
		return 1
	}
	result := struct {
		OperationID   string `json:"operation_id"`
		Phase         string `json:"phase"`
		OperationFile string `json:"operation_file"`
		ReceiptFile   string `json:"receipt_file"`
		Mailboxes     int    `json:"mailboxes"`
	}{op.OperationID, op.Phase, op.Directory + "/operation.json", op.ReceiptPath, len(op.Receipt.Mailboxes)}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if encoder.Encode(result) != nil {
		return 1
	}
	return 0
}
