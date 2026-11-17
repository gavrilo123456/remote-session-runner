// Command mailbox-uncertain-closeout is the exact GPU F07 maintenance operator.
// Plan is read-only. Apply requires reviewed owner-only approval and stopped
// Mac PRD ingress; it never replays or ACKs any request or changes capacity.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"remote-session-runner/src/internal/laneinstall"
)

func main() {
	plan := flag.Bool("plan", false, "read exact GPU request metadata without effects")
	apply := flag.Bool("apply", false, "apply the separately approved exact unknown-outcome disposition")
	project := flag.Bool("project-only", false, "resume only an interrupted terminal-response publication")
	approval := flag.String("approval-file", "", "owner-only snapshot-bound approval JSON; required with --apply")
	backup := flag.String("backup-directory", "", "original verified closeout backup; required only with --project-only")
	flag.Parse()
	count := 0
	for _, selected := range []bool{*plan, *apply, *project} {
		if selected {
			count++
		}
	}
	if flag.NArg() != 0 || count != 1 || (*plan && *approval != "") || ((*apply || *project) && *approval == "") || (*project && *backup == "") || (!*project && *backup != "") {
		fmt.Fprintln(os.Stderr, "usage: mailbox-uncertain-closeout --plan | --apply --approval-file /absolute/private/approval.json | --project-only --approval-file /absolute/private/approval.json --backup-directory /absolute/original/backup")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var report laneinstall.GPUUncertainCloseoutReport
	var err error
	if *plan {
		report, err = laneinstall.PlanGPUUncertainCloseout(ctx)
	} else if *project {
		report, err = laneinstall.ProjectGPUUncertainCloseout(ctx, *approval, *backup)
	} else {
		report, err = laneinstall.ApplyGPUUncertainCloseout(ctx, *approval)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		if report.Applied {
			_ = json.NewEncoder(os.Stdout).Encode(report)
		}
		os.Exit(1)
	}
	if err = json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintln(os.Stderr, "encode closeout report failed")
		os.Exit(1)
	}
}
