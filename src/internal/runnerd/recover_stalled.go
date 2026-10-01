package runnerd

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	hostruntime "remote-session-runner/src/internal/runtime"
	"remote-session-runner/src/internal/store"
)

// runRecoverStalled is an offline repair for an explicit complete set of
// already-terminal records. It has no ingress listener or dispatcher and it
// never reads or replays stored scripts. The caller must stop runnerd first;
// the lifecycle lock then prevents a service start during recovery.
func runRecoverStalled(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("runnerd recover-stalled", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "owner-only Linux runner configuration")
	apply := flags.Bool("apply", false, "perform the explicit offline repair")
	var jobValues, pairValues repeatedRecoveryValue
	flags.Var(&jobValues, "job-id", "terminal cancelled one-off job ID; repeat for every pending job")
	flags.Var(&pairValues, "lost-pair", "lost SESSION_ID:COMMAND_ID pair; repeat for every retained lost runtime")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *configPath == "" || !*apply || flags.NArg() != 0 || (len(jobValues) == 0 && len(pairValues) == 0) {
		fmt.Fprintln(stderr, "runnerd recover-stalled: --config, --apply, and at least one --job-id or --lost-pair are required")
		return 2
	}
	jobIDs, err := parseRecoveryJobIDs(jobValues)
	if err != nil {
		fmt.Fprintf(stderr, "runnerd recover-stalled: %v\n", err)
		return 2
	}
	lostRequests, err := parseLostRecoveryPairs(pairValues)
	if err != nil {
		fmt.Fprintf(stderr, "runnerd recover-stalled: %v\n", err)
		return 2
	}
	loaded, err := config.LoadFile(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "runnerd recover-stalled: load config: %v\n", err)
		return 1
	}
	settings, ok := loaded.LinuxSettings()
	if !ok || loaded.Kind() != config.HostKindLinux {
		fmt.Fprintln(stderr, "runnerd recover-stalled: Linux host configuration is required")
		return 1
	}
	releaseLifecycleLock, err := acquireRunnerdLifecycleLock(settings.ServiceRoot)
	if err != nil {
		fmt.Fprintf(stderr, "runnerd recover-stalled: %v\n", err)
		return 1
	}
	defer releaseLifecycleLock()
	if err := requireRunnerdServiceStopped(); err != nil {
		fmt.Fprintf(stderr, "runnerd recover-stalled: %v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	profile, err := hostruntime.NewLinuxProcessProfile(hostruntime.LinuxRuntimeOptions{
		Account: settings.Account, ServiceRoot: settings.ServiceRoot, WorkspaceRoot: settings.Workspaces, ShellPath: "/usr/bin/bash",
	})
	if err != nil {
		fmt.Fprintf(stderr, "runnerd recover-stalled: host process profile: %v\n", err)
		return 1
	}
	profileReport, err := profile.Doctor(ctx)
	if err != nil || !profileReport.Ready {
		fmt.Fprintln(stderr, "runnerd recover-stalled: host process profile is not ready")
		return 1
	}
	database, err := store.Open(ctx, settings.Database)
	if err != nil {
		fmt.Fprintf(stderr, "runnerd recover-stalled: open authority database: %v\n", err)
		return 1
	}
	defer database.Close()
	authority, err := store.NewAuthorityStore(database)
	if err != nil {
		fmt.Fprintf(stderr, "runnerd recover-stalled: construct authority store: %v\n", err)
		return 1
	}
	environments := make([]domain.Environment, 0, len(loaded.EnvironmentNames()))
	for _, name := range loaded.EnvironmentNames() {
		registered, present := loaded.Environment(name)
		if !present {
			fmt.Fprintf(stderr, "runnerd recover-stalled: configured environment %q disappeared\n", name)
			return 1
		}
		environments = append(environments, registered.Policy())
	}
	service, runtimeAdapter, err := NewLinuxExecutionService(authority, hostruntime.LinuxRuntimeOptions{
		Account: settings.Account, ServiceRoot: settings.ServiceRoot, WorkspaceRoot: settings.Workspaces, ShellPath: "/usr/bin/bash",
	}, environments...)
	if err != nil {
		fmt.Fprintf(stderr, "runnerd recover-stalled: construct execution service: %v\n", err)
		return 1
	}
	if err := requireStalledRecoveryInventory(ctx, authority, service, runtimeAdapter, jobIDs, lostRequests); err != nil {
		fmt.Fprintf(stderr, "runnerd recover-stalled: preflight: %v\n", err)
		return 1
	}
	var settled []store.ClosedCancelledJobSettlement
	if len(jobIDs) != 0 {
		settled, err = authority.SettleClosedCancelledOneOffJobs(ctx, jobIDs)
		if err != nil {
			fmt.Fprintf(stderr, "runnerd recover-stalled: settle terminal jobs: %v\n", err)
			return 1
		}
	}
	var recovered []execution.LostRuntimeRecoveryResult
	if len(lostRequests) != 0 {
		recovered, err = service.RecoverLostRuntimeBatch(ctx, lostRequests)
		if err != nil {
			fmt.Fprintf(stderr, "runnerd recover-stalled: recover retained lost capacity: %v\n", err)
			return 1
		}
	}
	if err := requireStalledRecoveryPostflight(ctx, authority); err != nil {
		fmt.Fprintf(stderr, "runnerd recover-stalled: postflight: %v\n", err)
		return 1
	}
	settledCount, alreadySettledCount := recoverySettlementCounts(settled)
	recoveredCount, alreadyRecoveredCount := recoveryResultCounts(recovered)
	fmt.Fprintf(stdout, "runnerd recover-stalled: settled_jobs=%d already_settled_jobs=%d recovered_lost_pairs=%d already_recovered_lost_pairs=%d\n", settledCount, alreadySettledCount, recoveredCount, alreadyRecoveredCount)
	return 0
}

type repeatedRecoveryValue []string

func (v *repeatedRecoveryValue) String() string { return strings.Join(*v, ",") }

func (v *repeatedRecoveryValue) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("value is empty")
	}
	*v = append(*v, value)
	return nil
}

func parseRecoveryJobIDs(values []string) ([]domain.JobID, error) {
	jobIDs := make([]domain.JobID, 0, len(values))
	seen := make(map[domain.JobID]struct{}, len(values))
	for _, raw := range values {
		jobID, err := domain.NewJobID(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid job ID: %v", err)
		}
		if _, duplicate := seen[jobID]; duplicate {
			return nil, fmt.Errorf("duplicate job ID %s", jobID)
		}
		seen[jobID] = struct{}{}
		jobIDs = append(jobIDs, jobID)
	}
	return jobIDs, nil
}

func parseLostRecoveryPairs(values []string) ([]execution.LostRuntimeRecoveryRequest, error) {
	requests := make([]execution.LostRuntimeRecoveryRequest, 0, len(values))
	seenSessions := make(map[domain.SessionID]struct{}, len(values))
	seenCommands := make(map[domain.CommandID]struct{}, len(values))
	for _, raw := range values {
		sessionValue, commandValue, found := strings.Cut(raw, ":")
		if !found || sessionValue == "" || commandValue == "" || strings.Contains(commandValue, ":") {
			return nil, fmt.Errorf("invalid lost pair %q; want SESSION_ID:COMMAND_ID", raw)
		}
		sessionID, err := domain.NewSessionID(sessionValue)
		if err != nil {
			return nil, fmt.Errorf("invalid lost pair session ID: %v", err)
		}
		commandID, err := domain.NewCommandID(commandValue)
		if err != nil {
			return nil, fmt.Errorf("invalid lost pair command ID: %v", err)
		}
		if _, duplicate := seenSessions[sessionID]; duplicate {
			return nil, fmt.Errorf("duplicate lost pair session ID %s", sessionID)
		}
		if _, duplicate := seenCommands[commandID]; duplicate {
			return nil, fmt.Errorf("duplicate lost pair command ID %s", commandID)
		}
		seenSessions[sessionID] = struct{}{}
		seenCommands[commandID] = struct{}{}
		requests = append(requests, execution.LostRuntimeRecoveryRequest{SessionID: sessionID, CommandID: commandID})
	}
	return requests, nil
}

func requireStalledRecoveryInventory(ctx context.Context, authority *store.AuthorityStore, service *execution.Service, runtimeAdapter *LinuxSessionRuntime, jobIDs []domain.JobID, lostRequests []execution.LostRuntimeRecoveryRequest) error {
	if authority == nil || service == nil || runtimeAdapter == nil {
		return fmt.Errorf("recovery dependencies are incomplete")
	}
	activeSessions, err := authority.CountActiveSessions(ctx)
	if err != nil {
		return err
	}
	runningCommands, err := authority.CountRunningCommands(ctx)
	if err != nil {
		return err
	}
	if activeSessions != 0 || runningCommands != 0 {
		return fmt.Errorf("active_sessions=%d running_commands=%d, want 0/0", activeSessions, runningCommands)
	}
	if len(jobIDs) != 0 {
		if _, err := authority.CheckClosedCancelledOneOffJobs(ctx, jobIDs); err != nil {
			return fmt.Errorf("validate explicit terminal jobs: %w", err)
		}
	}
	actualJobs, err := authority.ListNonterminalJobIDs(ctx)
	if err != nil {
		return err
	}
	if !sameRecoveryJobIDs(actualJobs, jobIDs) {
		return fmt.Errorf("nonterminal jobs do not match the explicit recovery input")
	}
	expectedRetained := 0
	if len(lostRequests) != 0 {
		results, err := service.CheckLostRuntimeRecoveryBatch(ctx, lostRequests)
		if err != nil {
			return err
		}
		for _, result := range results {
			if !result.AlreadyRecovered {
				expectedRetained++
			}
		}
	}
	liveSlots, err := authority.CountLiveCommandSlots(ctx)
	if err != nil {
		return err
	}
	liveReservations, err := authority.CountLiveSessionReservations(ctx)
	if err != nil {
		return err
	}
	if liveSlots != expectedRetained || liveReservations != expectedRetained {
		return fmt.Errorf("live command slots=%d live session reservations=%d, want %d selected retained runtimes", liveSlots, liveReservations, expectedRetained)
	}
	attributable := make(map[string]struct{}, len(lostRequests))
	for _, request := range lostRequests {
		attributable[string(request.SessionID)] = struct{}{}
	}
	if err := runtimeAdapter.AuditOwnership(ctx, attributable); err != nil {
		return fmt.Errorf("audit lost runtime ownership: %w", err)
	}
	return nil
}

func requireStalledRecoveryPostflight(ctx context.Context, authority *store.AuthorityStore) error {
	activeSessions, err := authority.CountActiveSessions(ctx)
	if err != nil {
		return err
	}
	runningCommands, err := authority.CountRunningCommands(ctx)
	if err != nil {
		return err
	}
	liveSlots, err := authority.CountLiveCommandSlots(ctx)
	if err != nil {
		return err
	}
	liveReservations, err := authority.CountLiveSessionReservations(ctx)
	if err != nil {
		return err
	}
	jobs, err := authority.ListNonterminalJobIDs(ctx)
	if err != nil {
		return err
	}
	if activeSessions != 0 || runningCommands != 0 || liveSlots != 0 || liveReservations != 0 || len(jobs) != 0 {
		return fmt.Errorf("active_sessions=%d running_commands=%d live_slots=%d live_session_reservations=%d unfinished_jobs=%d, want 0/0/0/0/0", activeSessions, runningCommands, liveSlots, liveReservations, len(jobs))
	}
	return nil
}

// sameRecoveryJobIDs verifies that every current nonterminal job was named by
// the operator. Named jobs may already be terminal on a retry, but all of them
// were independently checked by CheckClosedCancelledOneOffJobs above.
func sameRecoveryJobIDs(actual, selected []domain.JobID) bool {
	selectedSet := make(map[domain.JobID]struct{}, len(selected))
	for _, jobID := range selected {
		selectedSet[jobID] = struct{}{}
	}
	for _, jobID := range actual {
		if _, present := selectedSet[jobID]; !present {
			return false
		}
	}
	return true
}

func recoverySettlementCounts(results []store.ClosedCancelledJobSettlement) (settled, alreadySettled int) {
	for _, result := range results {
		if result.AlreadySettled {
			alreadySettled++
		} else {
			settled++
		}
	}
	return settled, alreadySettled
}

func recoveryResultCounts(results []execution.LostRuntimeRecoveryResult) (recovered, alreadyRecovered int) {
	for _, result := range results {
		if result.AlreadyRecovered {
			alreadyRecovered++
		} else {
			recovered++
		}
	}
	return recovered, alreadyRecovered
}
