package runnerd

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"remote-session-runner/src/internal/audit"
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
	var jobValues, pairValues, sessionValues, teardownValues repeatedRecoveryValue
	flags.Var(&jobValues, "job-id", "terminal cancelled one-off job ID; repeat for every pending job")
	flags.Var(&pairValues, "lost-pair", "lost SESSION_ID:COMMAND_ID pair; repeat for every retained lost runtime")
	flags.Var(&sessionValues, "lost-session", "lost SESSION_ID without a persisted command; repeat for every retained commandless lost runtime")
	flags.Var(&teardownValues, "teardown-pair", "complete succeeded or ordinary failed command with lost teardown as SESSION_ID:COMMAND_ID; repeat for the complete retained set")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *configPath == "" || !*apply || flags.NArg() != 0 || (len(jobValues) == 0 && len(pairValues) == 0 && len(sessionValues) == 0 && len(teardownValues) == 0) {
		fmt.Fprintln(stderr, "runnerd recover-stalled: --config, --apply, and at least one --job-id, --lost-pair, --lost-session, or --teardown-pair are required")
		return 2
	}
	if len(teardownValues) != 0 && (len(jobValues) != 0 || len(sessionValues) != 0) {
		fmt.Fprintln(stderr, "runnerd recover-stalled: --teardown-pair can combine only with --lost-pair")
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
	teardownRequests, err := parseCompletedTeardownRecoveryPairs(teardownValues)
	if err != nil {
		fmt.Fprintf(stderr, "runnerd recover-stalled: %v\n", err)
		return 2
	}
	if err := validateDistinctLostRecoverySessions(lostRequests, lostSessionRequests); err != nil {
		fmt.Fprintf(stderr, "runnerd recover-stalled: %v\n", err)
		return 2
	}
	configBefore, err := os.ReadFile(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "runnerd recover-stalled: read config identity: %v\n", err)
		return 1
	}
	loaded, err := config.LoadFile(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "runnerd recover-stalled: load config: %v\n", err)
		return 1
	}
	configAfter, err := os.ReadFile(*configPath)
	if err != nil || !bytesEqual(configBefore, configAfter) {
		fmt.Fprintf(stderr, "runnerd recover-stalled: configuration changed while loading: %v\n", err)
		return 1
	}
	configFingerprint := sha256.Sum256(configAfter)
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
	jobIDValues := make([]string, len(jobIDs))
	for index, jobID := range jobIDs {
		jobIDValues[index] = string(jobID)
	}
	sessionIDValues := make([]string, len(lostSessionRequests))
	for index, request := range lostSessionRequests {
		sessionIDValues[index] = string(request.SessionID)
	}
	if err := requireRunnerdServiceStoppedForRecovery(settings, *configPath, configFingerprint, jobIDValues, len(lostRequests), len(teardownRequests), sessionIDValues); err != nil {
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
	if len(teardownRequests) != 0 {
		if err := requireMixedRuntimeRecoveryInventory(ctx, authority, service, runtimeAdapter, lostRequests, teardownRequests); err != nil {
			fmt.Fprintf(stderr, "runnerd recover-stalled: mixed preflight: %v\n", err)
			return 1
		}
		lostRecovered, teardownRecovered, recoverErr := service.RecoverMixedRuntimeRecoverySet(ctx, lostRequests, teardownRequests)
		if recoverErr != nil {
			fmt.Fprintf(stderr, "runnerd recover-stalled: mixed recovery: %v\n", recoverErr)
			return 1
		}
		if err := requireStalledRecoveryPostflight(ctx, authority); err != nil {
			fmt.Fprintf(stderr, "runnerd recover-stalled: mixed postflight: %v\n", err)
			return 1
		}
		lostCount, alreadyLost := recoveryResultCounts(lostRecovered)
		var teardownCount, alreadyTeardown int
		for _, result := range teardownRecovered {
			if result.Recovery.AlreadyRecovered {
				alreadyTeardown++
			} else {
				teardownCount++
			}
		}
		fmt.Fprintf(stdout, "runnerd recover-stalled: recovered_lost_pairs=%d already_recovered_lost_pairs=%d recovered_teardown_pairs=%d already_recovered_teardown_pairs=%d\n", lostCount, alreadyLost, teardownCount, alreadyTeardown)
		return 0
	}
	if err := requireStalledRecoveryInventory(ctx, authority, service, runtimeAdapter, jobIDs, lostRequests, lostSessionRequests); err != nil {
		fmt.Fprintf(stderr, "runnerd recover-stalled: preflight: %v\n", err)
		return 1
	}
	if len(lostSessionRequests) == 1 && lostSessionRequests[0].SessionID == domain.SessionID(linuxPRDProbeSession) {
		if err := requireExactLinuxPRDProbeRecoveryBinding(ctx, authority, jobIDs, lostRequests, teardownRequests, lostSessionRequests); err != nil {
			fmt.Fprintf(stderr, "runnerd recover-stalled: exact probe binding: %v\n", err)
			return 1
		}
		// Re-scan process ownership and systemd immediately before the recovery
		// service call. The lifecycle lock remains held through its transaction,
		// runtime finalization, and postflight below.
		if err := requireRunnerdServiceStoppedForRecovery(settings, *configPath, configFingerprint, jobIDValues, len(lostRequests), len(teardownRequests), sessionIDValues); err != nil {
			fmt.Fprintf(stderr, "runnerd recover-stalled: final stopped-state guard: %v\n", err)
			return 1
		}
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

const linuxPRDProbeScript = "printf 'catalog release maintenance probe\\n'"

func requireExactLinuxPRDProbeRecoveryBinding(ctx context.Context, authority *store.AuthorityStore,
	jobIDs []domain.JobID, pairs []execution.LostRuntimeRecoveryRequest,
	teardown []execution.CompletedTeardownRecoveryRequest,
	sessions []execution.CommandlessLostRuntimeRecoveryRequest) error {
	if authority == nil || len(jobIDs) != 0 || len(pairs) != 0 || len(teardown) != 0 || len(sessions) != 1 ||
		sessions[0].SessionID != domain.SessionID(linuxPRDProbeSession) {
		return errors.New("recovery input is not the single explicitly selected Linux PRD probe session")
	}
	recoveries, err := authority.CheckCommandlessLostRuntimeRecoveryBatch(ctx, []domain.SessionID{sessions[0].SessionID})
	if err != nil {
		return fmt.Errorf("validate commandless probe reservation: %w", err)
	}
	if len(recoveries) != 1 {
		return errors.New("commandless probe recovery did not return exactly one bound record")
	}
	recovery := recoveries[0]
	if string(recovery.JobID) != linuxPRDProbeJob || string(recovery.SessionID) != linuxPRDProbeSession ||
		string(recovery.CommandID) != linuxPRDProbeCommand {
		return errors.New("commandless probe job/session/command/reservation identity does not match the authorized retained probe")
	}
	job, err := authority.GetJob(ctx, recovery.JobID)
	if err != nil {
		return fmt.Errorf("read exact probe job metadata: %w", err)
	}
	session, err := authority.GetSession(ctx, recovery.SessionID)
	if err != nil {
		return fmt.Errorf("read exact probe session metadata: %w", err)
	}
	reservation, err := authority.GetSessionReservation(ctx, recovery.SessionID)
	if err != nil {
		return fmt.Errorf("read exact probe reservation metadata: %w", err)
	}
	if !exactLinuxPRDProbeRecoveryRecordsMatch(recovery, job, session, reservation) {
		return errors.New("probe job/session/command/reservation is not the exact commandless, lost acceptance request")
	}
	if err := requireNoUnrelatedProbeRecoveryFinalizations(ctx, authority, recovery.SessionID, recovery.FinalizationPending); err != nil {
		return err
	}
	commands, err := authority.ListSessionCommands(ctx, recovery.SessionID)
	if err != nil || len(commands) != 0 {
		return errors.Join(errors.New("probe session has command records or command inventory is unreadable"), err)
	}
	if _, err := authority.GetCommand(ctx, recovery.CommandID); !errors.Is(err, store.ErrCommandNotFound) {
		return errors.Join(errors.New("planned probe command unexpectedly has a durable command row"), err)
	}
	lifecycle, err := authority.ListSessionLifecycle(ctx, recovery.SessionID)
	if err != nil {
		return fmt.Errorf("read probe lifecycle evidence: %w", err)
	}
	if len(lifecycle) != 3 || lifecycle[0].Sequence != 1 || lifecycle[0].PreviousState != nil ||
		lifecycle[0].NewState != domain.SessionStateCreating || lifecycle[0].Reason != "session_created" ||
		lifecycle[1].Sequence != 2 || lifecycle[1].PreviousState == nil || *lifecycle[1].PreviousState != domain.SessionStateCreating ||
		lifecycle[1].NewState != domain.SessionStateClosing || lifecycle[1].Reason != "close_requested" ||
		lifecycle[2].Sequence != 3 || lifecycle[2].PreviousState == nil || *lifecycle[2].PreviousState != domain.SessionStateClosing ||
		lifecycle[2].NewState != domain.SessionStateLost || lifecycle[2].Reason != audit.ReasonRuntimeCleanupUnconfirmed {
		return errors.New("probe lifecycle history differs from the recorded stopped-loss sequence")
	}
	return nil
}

func exactLinuxPRDProbeRecoveryRecordsMatch(recovery store.CommandlessLostRuntimeRecovery, job store.JobRecord,
	session store.SessionRecord, reservation store.SessionReservation) bool {
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeDirectMTLS, "tomasz.walczuk")
	if err != nil {
		return false
	}
	target, err := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	if err != nil {
		return false
	}
	reservationReleased := reservation.CleanupConfirmedAt != nil
	if recovery.JobID != domain.JobID(linuxPRDProbeJob) || recovery.SessionID != domain.SessionID(linuxPRDProbeSession) ||
		recovery.CommandID != domain.CommandID(linuxPRDProbeCommand) || job.JobID != recovery.JobID ||
		job.SessionID != recovery.SessionID || job.CommandID != recovery.CommandID ||
		job.IdempotencyKey != "catalog-probe-63471d41af07b7afaf577d6a0989cc03-linux-host" ||
		job.Environment != "linux-dev" || job.Controller != controller || job.Ingress != audit.IngressDirectMTLS ||
		job.Target != target || job.Source.Mode() != domain.SourceModeEmpty || string(job.ScriptBytes) != linuxPRDProbeScript ||
		job.Phase != store.JobPhaseLost || job.CommandState != nil || job.ExitCode != nil || job.FinalEventSequence != nil ||
		job.OutputComplete || job.OutputTruncated || job.OutputUnavailableReason != "" ||
		job.TeardownState != store.JobTeardownPending || job.TeardownReason != "" ||
		session.SessionID != recovery.SessionID || session.State != domain.SessionStateLost || session.RuntimeGeneration != "" ||
		session.Environment != "linux-dev" || session.Controller != controller || session.Target != target || session.Source.Mode() != domain.SourceModeEmpty ||
		recovery.Session.SessionID != session.SessionID || recovery.Session.State != session.State ||
		recovery.Session.RuntimeGeneration != session.RuntimeGeneration || recovery.Session.Environment != session.Environment ||
		recovery.Session.Controller != session.Controller || recovery.Session.Target != session.Target ||
		recovery.Session.Source.Mode() != session.Source.Mode() ||
		reservation.SessionID != recovery.SessionID || reservation.HostKey != "authority" || reservation.ReservedAt.IsZero() ||
		(reservation.CleanupConfirmedAt == nil) != (reservation.ReleasedAt == nil) || recovery.AlreadyRecovered != reservationReleased ||
		recovery.FinalizationPending && !recovery.AlreadyRecovered ||
		recovery.SessionReservation.SessionID != reservation.SessionID || recovery.SessionReservation.HostKey != reservation.HostKey ||
		!recovery.SessionReservation.ReservedAt.Equal(reservation.ReservedAt) ||
		!optionalTimeEqual(recovery.SessionReservation.CleanupConfirmedAt, reservation.CleanupConfirmedAt) ||
		!optionalTimeEqual(recovery.SessionReservation.ReleasedAt, reservation.ReleasedAt) {
		return false
	}
	return true
}

func optionalTimeEqual(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func requireNoUnrelatedProbeRecoveryFinalizations(ctx context.Context, authority *store.AuthorityStore, sessionID domain.SessionID, sessionPending bool) error {
	pairs, err := authority.ListPendingLostRuntimeRecoveryFinalizations(ctx)
	if err != nil {
		return fmt.Errorf("read pending lost-pair finalizations: %w", err)
	}
	if len(pairs) != 0 {
		return errors.New("unrelated lost-pair recovery finalization is pending")
	}
	sessions, err := authority.ListPendingCommandlessLostRuntimeRecoveryFinalizations(ctx)
	if err != nil {
		return fmt.Errorf("read pending commandless finalizations: %w", err)
	}
	if sessionPending {
		if len(sessions) != 1 || sessions[0].SessionID != sessionID {
			return errors.New("pending commandless finalization does not match the exact probe reservation")
		}
	} else if len(sessions) != 0 {
		return errors.New("unrelated commandless recovery finalization is pending")
	}
	teardowns, err := authority.ListPendingCompletedTeardownRecoveryFinalizations(ctx)
	if err != nil {
		return fmt.Errorf("read pending teardown finalizations: %w", err)
	}
	if len(teardowns) != 0 {
		return errors.New("unrelated completed-teardown recovery finalization is pending")
	}
	return nil
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

func parseCompletedTeardownRecoveryPairs(values []string) ([]execution.CompletedTeardownRecoveryRequest, error) {
	requests := make([]execution.CompletedTeardownRecoveryRequest, 0, len(values))
	seenSessions := make(map[domain.SessionID]struct{}, len(values))
	seenCommands := make(map[domain.CommandID]struct{}, len(values))
	for _, raw := range values {
		sessionValue, commandValue, found := strings.Cut(raw, ":")
		if !found || sessionValue == "" || commandValue == "" || strings.Contains(commandValue, ":") {
			return nil, fmt.Errorf("invalid teardown pair; want SESSION_ID:COMMAND_ID")
		}
		sessionID, err := domain.NewSessionID(sessionValue)
		if err != nil {
			return nil, fmt.Errorf("invalid teardown session ID: %v", err)
		}
		commandID, err := domain.NewCommandID(commandValue)
		if err != nil {
			return nil, fmt.Errorf("invalid teardown command ID: %v", err)
		}
		if _, duplicate := seenSessions[sessionID]; duplicate {
			return nil, fmt.Errorf("duplicate teardown session ID %s", sessionID)
		}
		if _, duplicate := seenCommands[commandID]; duplicate {
			return nil, fmt.Errorf("duplicate teardown command ID %s", commandID)
		}
		seenSessions[sessionID] = struct{}{}
		seenCommands[commandID] = struct{}{}
		requests = append(requests, execution.CompletedTeardownRecoveryRequest{SessionID: sessionID, CommandID: commandID})
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

func requireMixedRuntimeRecoveryInventory(ctx context.Context, authority *store.AuthorityStore, service *execution.Service, runtimeAdapter *LinuxSessionRuntime, lost []execution.LostRuntimeRecoveryRequest, teardown []execution.CompletedTeardownRecoveryRequest) error {
	if authority == nil || service == nil || runtimeAdapter == nil || (len(lost) == 0 && len(teardown) == 0) {
		return fmt.Errorf("recovery dependencies are incomplete")
	}
	activeSessions, err := authority.CountActiveSessions(ctx)
	if err != nil || activeSessions != 0 {
		return fmt.Errorf("active session inventory is not empty")
	}
	runningCommands, err := authority.CountRunningCommands(ctx)
	if err != nil || runningCommands != 0 {
		return fmt.Errorf("running command inventory is not empty")
	}
	jobs, err := authority.ListNonterminalJobIDs(ctx)
	if err != nil || len(jobs) != 0 {
		return fmt.Errorf("nonterminal job inventory is not empty")
	}
	if _, _, err := service.CheckMixedRuntimeRecoverySet(ctx, lost, teardown); err != nil {
		return err
	}
	attributable := make(map[string]struct{}, len(lost)+len(teardown))
	for _, request := range lost {
		attributable[string(request.SessionID)] = struct{}{}
	}
	for _, request := range teardown {
		attributable[string(request.SessionID)] = struct{}{}
	}
	if err := runtimeAdapter.AuditOwnership(ctx, attributable); err != nil {
		return fmt.Errorf("audit mixed recovery ownership: %w", err)
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
	pendingTeardowns, err := authority.ListPendingCompletedTeardownRecoveryFinalizations(ctx)
	if err != nil {
		return err
	}
	if len(pendingTeardowns) != 0 {
		return fmt.Errorf("pending completed-teardown finalizations remain: pairs=%d", len(pendingTeardowns))
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
