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

// openRunnerdRecoveryAuthority is deliberately the existing-authority-only
// migration path. recover-stalled already holds the runnerd lifecycle lock and
// has proven the service stopped; it must never create a missing authority
// while attempting offline repair.
var openRunnerdRecoveryAuthority = store.OpenExistingOfflineMaintenanceMigrating

// runRecoverStalled is an offline repair for an explicit complete set of
// already-terminal records. It has no ingress listener or dispatcher and it
// never reads or replays stored scripts. The caller must stop runnerd first;
// the lifecycle lock then prevents a service start during recovery.
func runRecoverStalled(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("runnerd recover-stalled", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "owner-only Linux runner configuration")
	apply := flags.Bool("apply", false, "perform the explicit offline repair")
	var jobValues, pairValues, sessionValues repeatedRecoveryValue
	flags.Var(&jobValues, "job-id", "terminal cancelled one-off job ID; repeat for every pending job")
	flags.Var(&pairValues, "lost-pair", "lost SESSION_ID:COMMAND_ID pair; repeat for every retained lost runtime")
	flags.Var(&sessionValues, "lost-session", "lost SESSION_ID without a persisted command; repeat for every retained commandless lost runtime")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *configPath == "" || !*apply || flags.NArg() != 0 || (len(jobValues) == 0 && len(pairValues) == 0 && len(sessionValues) == 0) {
		fmt.Fprintln(stderr, "runnerd recover-stalled: --config, --apply, and at least one --job-id, --lost-pair, or --lost-session are required")
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
	lostSessionRequests, err := parseLostRecoverySessions(sessionValues)
	if err != nil {
		fmt.Fprintf(stderr, "runnerd recover-stalled: %v\n", err)
		return 2
	}
	if err := validateDistinctLostRecoverySessions(lostRequests, lostSessionRequests); err != nil {
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
	database, err := openRunnerdRecoveryAuthority(ctx, settings.Database)
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
	if err := requireStalledRecoveryInventory(ctx, authority, service, runtimeAdapter, jobIDs, lostRequests, lostSessionRequests); err != nil {
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
	var recoveredSessions []execution.CommandlessLostRuntimeRecoveryResult
	if len(lostRequests) != 0 || len(lostSessionRequests) != 0 {
		recovered, recoveredSessions, err = service.RecoverLostRuntimeRecoverySet(ctx, lostRequests, lostSessionRequests)
		if err != nil {
			fmt.Fprintf(stderr, "runnerd recover-stalled: recover retained mixed lost capacity: %v\n", err)
			return 1
		}
	}
	if err := requireStalledRecoveryPostflight(ctx, authority); err != nil {
		fmt.Fprintf(stderr, "runnerd recover-stalled: postflight: %v\n", err)
		return 1
	}
	settledCount, alreadySettledCount := recoverySettlementCounts(settled)
	recoveredCount, alreadyRecoveredCount := recoveryResultCounts(recovered)
	recoveredSessionCount, alreadyRecoveredSessionCount := commandlessRecoveryResultCounts(recoveredSessions)
	fmt.Fprintf(stdout, "runnerd recover-stalled: settled_jobs=%d already_settled_jobs=%d recovered_lost_pairs=%d already_recovered_lost_pairs=%d recovered_lost_sessions=%d already_recovered_lost_sessions=%d\n", settledCount, alreadySettledCount, recoveredCount, alreadyRecoveredCount, recoveredSessionCount, alreadyRecoveredSessionCount)
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

func parseLostRecoverySessions(values []string) ([]execution.CommandlessLostRuntimeRecoveryRequest, error) {
	requests := make([]execution.CommandlessLostRuntimeRecoveryRequest, 0, len(values))
	seen := make(map[domain.SessionID]struct{}, len(values))
	for _, raw := range values {
		sessionID, err := domain.NewSessionID(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid lost session ID: %v", err)
		}
		if _, duplicate := seen[sessionID]; duplicate {
			return nil, fmt.Errorf("duplicate lost session ID %s", sessionID)
		}
		seen[sessionID] = struct{}{}
		requests = append(requests, execution.CommandlessLostRuntimeRecoveryRequest{SessionID: sessionID})
	}
	return requests, nil
}

func validateDistinctLostRecoverySessions(pairs []execution.LostRuntimeRecoveryRequest, sessions []execution.CommandlessLostRuntimeRecoveryRequest) error {
	seen := make(map[domain.SessionID]struct{}, len(pairs)+len(sessions))
	for _, pair := range pairs {
		seen[pair.SessionID] = struct{}{}
	}
	for _, session := range sessions {
		if _, duplicate := seen[session.SessionID]; duplicate {
			return fmt.Errorf("lost session %s was selected as both pair and commandless session", session.SessionID)
		}
		seen[session.SessionID] = struct{}{}
	}
	return nil
}

func requireStalledRecoveryInventory(ctx context.Context, authority *store.AuthorityStore, service *execution.Service, runtimeAdapter *LinuxSessionRuntime, jobIDs []domain.JobID, lostRequests []execution.LostRuntimeRecoveryRequest, lostSessionRequests []execution.CommandlessLostRuntimeRecoveryRequest) error {
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
	if len(lostRequests) != 0 || len(lostSessionRequests) != 0 {
		if err := service.CheckLostRuntimeRecoverySet(ctx, lostRequests, lostSessionRequests); err != nil {
			return err
		}
	}
	attributable := make(map[string]struct{}, len(lostRequests)+len(lostSessionRequests))
	for _, request := range lostRequests {
		attributable[string(request.SessionID)] = struct{}{}
	}
	for _, request := range lostSessionRequests {
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
	pendingPairs, err := authority.ListPendingLostRuntimeRecoveryFinalizations(ctx)
	if err != nil {
		return err
	}
	pendingSessions, err := authority.ListPendingCommandlessLostRuntimeRecoveryFinalizations(ctx)
	if err != nil {
		return err
	}
	if len(pendingPairs) != 0 || len(pendingSessions) != 0 {
		return fmt.Errorf("pending lost-runtime finalizations remain: pairs=%d sessions=%d", len(pendingPairs), len(pendingSessions))
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

func commandlessRecoveryResultCounts(results []execution.CommandlessLostRuntimeRecoveryResult) (recovered, alreadyRecovered int) {
	for _, result := range results {
		if result.Recovery.AlreadyRecovered {
			alreadyRecovered++
		} else {
			recovered++
		}
	}
	return recovered, alreadyRecovered
}
