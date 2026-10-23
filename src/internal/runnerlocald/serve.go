package runnerlocald

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	"remote-session-runner/src/internal/laneinstall"
	"remote-session-runner/src/internal/lifecycle"
	"remote-session-runner/src/internal/opshealth"
	"remote-session-runner/src/internal/queueworker"
	hostruntime "remote-session-runner/src/internal/runtime"
	"remote-session-runner/src/internal/store"
	"syscall"
	"time"
)

const (
	macShutdownDrainTimeout   = 8 * time.Second
	macShutdownCleanupTimeout = 5 * time.Second
	// macPolicySweepInterval bounds how long an active Mac-local command can
	// remain beyond its persisted command deadline while runner-locald is
	// healthy. The command's own configured timeout remains authoritative.
	macPolicySweepInterval = time.Second
)

// NewMacExecutionService wires the shared execution service to the Mac
// process adapter and configured environment registry.
func NewMacExecutionService(authority *store.AuthorityStore, options hostruntime.MacRuntimeOptions, environments ...domain.Environment) (*execution.Service, *MacSessionRuntime, error) {
	adapter, err := hostruntime.NewMacProcessAdapter(options)
	if err != nil {
		return nil, nil, err
	}
	runtimeAdapter, err := NewMacSessionRuntime(adapter)
	if err != nil {
		return nil, nil, err
	}
	registry, err := execution.NewEnvironmentRegistry(environments...)
	if err != nil {
		return nil, nil, err
	}
	service, err := execution.NewExecutionService(authority, runtimeAdapter, registry, execution.RealClock{}, nil)
	if err != nil {
		return nil, nil, err
	}
	return service, runtimeAdapter, nil
}

// Run starts the owner-only Mac runner-locald private API.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "doctor" {
		return runDoctor(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "preflight-restart" {
		return runRestartPreflight(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "prepare-controlled-restart" {
		return runPrepareControlledRestart(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "controlled-restart-status" {
		return runControlledRestartStatus(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "controlled-restart-socket-boundary" {
		return runControlledRestartSocketBoundary(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "recover-stalled" {
		return runRecoverStalled(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "recover-failed-startup" {
		return runRecoverFailedStartup(args[1:], stdout, stderr)
	}
	flags := flag.NewFlagSet("runner-locald", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "owner-only Mac runner configuration")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *configPath == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "runner-locald: --config is required")
		return 2
	}
	signalContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	loaded, err := config.LoadFile(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "runner-locald: load config: %v\n", err)
		return 1
	}
	settings, ok := loaded.MacSettings()
	if !ok || loaded.Kind() != config.HostKindMac {
		fmt.Fprintln(stderr, "runner-locald: Mac host configuration is required")
		return 1
	}
	executable, executableErr := os.Executable()
	if executableErr != nil || laneinstall.ValidateConfiguredCatalogExecutable(loaded, "runner-locald", executable) != nil {
		fmt.Fprintln(stderr, "runner-locald: running binary does not match the validated lane catalog selector")
		return 78
	}
	maintenanceHeld := false
	var maintenanceProbe *laneinstall.CatalogMaintenanceProbeGuard
	if plan, catalogManaged, planErr := laneinstall.CatalogPlanFromConfiguration(loaded); planErr != nil {
		fmt.Fprintln(stderr, "runner-locald: catalog lane identity is invalid")
		return 78
	} else if catalogManaged {
		var operationID string
		maintenanceHeld, operationID, err = laneinstall.CatalogCohortAdmissionHeld(plan)
		if err != nil {
			fmt.Fprintln(stderr, "runner-locald: catalog cohort receipt is invalid; refusing work admission")
			return 78
		}
		if maintenanceHeld {
			maintenanceProbe, err = laneinstall.NewCatalogMaintenanceProbeGuardForTransportWithConfig(plan, operationID, laneinstall.CatalogProbeMacLocal, loaded)
			if err != nil {
				fmt.Fprintln(stderr, "runner-locald: Mac-local catalog maintenance probe guard is invalid")
				return 78
			}
		}
	}
	releaseLifecycleLock, err := acquireRunnerLocaldLifecycleLock(settings.ServiceRoot)
	if err != nil {
		fmt.Fprintf(stderr, "runner-locald: %v\n", err)
		return runnerLocaldLifecycleLockExit
	}
	defer releaseLifecycleLock()
	ctx := context.Background()
	openAuthority := store.Open
	if maintenanceHeld {
		// A catalog maintenance start must never migrate production state as a
		// side effect of starting the held candidate.
		openAuthority = store.OpenExistingCurrent
	}
	authorityDB, err := openAuthority(ctx, settings.Database)
	if err != nil {
		fmt.Fprintf(stderr, "runner-locald: open authority database: %v\n", err)
		return 1
	}
	defer authorityDB.Close()
	authority, err := store.NewAuthorityStore(authorityDB)
	if err != nil {
		fmt.Fprintf(stderr, "runner-locald: construct authority store: %v\n", err)
		return 1
	}
	environments := make([]domain.Environment, 0, len(loaded.EnvironmentNames()))
	for _, name := range loaded.EnvironmentNames() {
		registered, present := loaded.Environment(name)
		if !present {
			fmt.Fprintf(stderr, "runner-locald: configured environment %q disappeared\n", name)
			return 1
		}
		environments = append(environments, registered.Policy())
	}
	service, _, err := NewMacExecutionService(authority, hostruntime.MacRuntimeOptions{Account: settings.Account, WorkspaceRoot: settings.Workspaces, ShellPath: "/bin/bash"}, environments...)
	if err != nil {
		fmt.Fprintf(stderr, "runner-locald: construct execution service: %v\n", err)
		return 1
	}
	var reconciliation execution.StartupReconciliationReport
	var controlledRestart execution.ControlledRestartStartup
	if !maintenanceHeld {
		reconciliation, controlledRestart, err = service.ReconcileStartupWithControlledRestartPlan(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "runner-locald: startup runtime reconciliation: %v\n", err)
			return 1
		}
	}
	slog.Info("runner-locald startup reconciliation complete",
		"sessions_inspected", reconciliation.SessionsInspected,
		"sessions_lost", reconciliation.SessionsLost,
		"commands_lost", reconciliation.CommandsLost,
		"commands_rejected", reconciliation.CommandsRejected,
		"cleanup_unconfirmed", reconciliation.CleanupUnconfirmed,
	)
	if controlledRestart.Plan != nil {
		slog.Info("runner-locald controlled restart shell rehydrated",
			"job_id", controlledRestart.Plan.JobID,
			"session_id", controlledRestart.Plan.SessionID,
			"command_id", controlledRestart.Plan.CommandID,
			"lost_pair_count", len(controlledRestart.Plan.LostPairs),
		)
	}
	dispatchGate := lifecycle.NewGate()
	if maintenanceHeld {
		dispatchGate.Stop()
	}
	worker, err := queueworker.New(queueworker.Options{
		Service: service, Authority: authority, DispatchGate: dispatchGate,
	})
	if err != nil {
		fmt.Fprintf(stderr, "runner-locald: construct queued worker: %v\n", err)
		return 1
	}
	owner, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, domain.ControllerID(settings.Account))
	if err != nil {
		fmt.Fprintf(stderr, "runner-locald: owner controller: %v\n", err)
		return 1
	}
	thresholds := opshealth.NewThresholdMonitor()
	var queueWake func()
	if !maintenanceHeld {
		queueWake = worker.Wake
	}
	server, err := NewPrivateServer(PrivateServerOptions{
		Authority: authority, Service: service, Owner: owner, SocketPath: settings.LocalDSocket,
		Thresholds: thresholds, DispatchGate: dispatchGate, QueueWake: queueWake,
		MaintenanceAdmissionClosed: maintenanceHeld,
		MaintenanceProbeGuard:      maintenanceProbe,
	})
	if err != nil {
		fmt.Fprintf(stderr, "runner-locald: construct private API: %v\n", err)
		return 1
	}
	if err := server.Listen(); err != nil {
		fmt.Fprintf(stderr, "runner-locald: listen: %v\n", err)
		return 1
	}
	coordinator, err := lifecycle.NewCoordinator(&privateServerShutdown{server: server, worker: worker}, lifecycle.RealClock{}, lifecycle.Config{
		DrainTimeout: macShutdownDrainTimeout, CleanupTimeout: macShutdownCleanupTimeout,
	})
	if err != nil {
		stop()
		_ = server.Close(context.Background())
		fmt.Fprintf(stderr, "runner-locald: configure shutdown: %v\n", err)
		return 1
	}
	if !maintenanceHeld {
		if err := worker.RecoverNonterminalJobs(signalContext); err != nil {
			shutdownErr := coordinator.Shutdown(context.Background())
			stop()
			fmt.Fprintf(stderr, "runner-locald: recover queued jobs after startup reconciliation: %v\n", errors.Join(err, shutdownErr))
			return 1
		}
		worker.Start(signalContext)
	}
	policyEnforcerDone := make(chan struct{})
	if maintenanceHeld {
		close(policyEnforcerDone)
	} else {
		go func() {
			defer close(policyEnforcerDone)
			enforceMacLocalPolicies(signalContext, service, macPolicySweepInterval)
		}()
	}
	metricsObserverDone := make(chan struct{})
	go func() {
		defer close(metricsObserverDone)
		observeMacLocalOperationalMetrics(signalContext, authority, thresholds)
	}()
	fmt.Fprintf(stdout, "runner-locald listening on %s\n", settings.LocalDSocket)
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve() }()
	select {
	case serveErr := <-serveErrors:
		shutdownErr := coordinator.Shutdown(context.Background())
		stop()
		<-policyEnforcerDone
		<-metricsObserverDone
		if serveErr != nil || shutdownErr != nil {
			fmt.Fprintf(stderr, "runner-locald: serve: %v\n", errors.Join(serveErr, shutdownErr))
			return 1
		}
	case <-signalContext.Done():
		shutdownErr := coordinator.Shutdown(context.Background())
		serveErr := <-serveErrors
		<-policyEnforcerDone
		<-metricsObserverDone
		if shutdownErr != nil || serveErr != nil {
			fmt.Fprintf(stderr, "runner-locald: shutdown: %v\n", errors.Join(shutdownErr, serveErr))
			return 1
		}
	}
	return 0
}

// enforceMacLocalPolicies makes the stored timeout, idle, and lifetime limits
// live on the installed Mac executor. It performs no work-body dispatch: a
// sweep can only settle existing durable state through the shared execution
// service. A failed sweep is logged with a fixed reason and retried on the
// next bounded interval, without exposing scripts or runtime details.
func enforceMacLocalPolicies(ctx context.Context, service *execution.Service, interval time.Duration) {
	execution.RunPolicyEnforcer(ctx, service, interval, func(error) {
		slog.Warn("runner-locald policy sweep failed", "lifecycle_phase", "policy_enforcement", "reason", "policy_enforcement_failed")
	})
}

type privateServerShutdown struct {
	server  *PrivateServer
	worker  *queueworker.Worker
	stopErr error
}

func (h *privateServerShutdown) StopAccepting() {
	if h != nil && h.server != nil {
		h.stopErr = h.server.StopAccepting()
	}
}

func (h *privateServerShutdown) StopDispatch() {
	if h == nil {
		return
	}
	if h.server != nil {
		h.server.StopDispatch()
	}
	// Close the shared admission gate before stopping the wake loop. A worker
	// that is between wake delivery and its next claim cannot enter after
	// shutdown dispatch begins; already admitted work remains drainable.
	if h.worker != nil {
		h.worker.Stop()
	}
}

func (h *privateServerShutdown) Drain(ctx context.Context) error {
	if h == nil || h.server == nil {
		return errors.New("runner-locald shutdown is not configured")
	}
	var workerErr error
	if h.worker != nil {
		workerErr = h.worker.Wait(ctx)
	}
	return errors.Join(h.stopErr, workerErr, h.server.Drain(ctx))
}

func (h *privateServerShutdown) CancelRemaining(ctx context.Context) error {
	if h == nil || h.server == nil {
		return errors.New("runner-locald shutdown is not configured")
	}
	return h.server.CancelRemaining(ctx)
}

func (h *privateServerShutdown) Flush(ctx context.Context) error {
	if h == nil || h.server == nil {
		return errors.New("runner-locald shutdown is not configured")
	}
	return h.server.Flush(ctx)
}

func (h *privateServerShutdown) CloseStreams(context.Context) error {
	if h == nil || h.server == nil {
		return errors.New("runner-locald shutdown is not configured")
	}
	return h.server.CloseStreams()
}

func observeMacLocalOperationalMetrics(ctx context.Context, authority *store.AuthorityStore, thresholds *opshealth.ThresholdMonitor) {
	if ctx == nil {
		ctx = context.Background()
	}
	observe := func() {
		if authority == nil {
			return
		}
		durable, err := authority.ReadOperationalMetrics(ctx)
		if err != nil {
			return
		}
		metrics := opshealth.Metrics{
			ActiveSessionSlots: durable.ActiveSessionSlots, ActiveCommandSlots: durable.ActiveCommandSlots,
			QueuedCommands: durable.QueuedCommands, QueuedIntents: durable.QueuedIntents,
			DispatchAttemptsTotal: durable.DispatchAttemptsTotal, ReconciliationAgeSeconds: durable.ReconciliationAgeSeconds,
			EventLagEvents: durable.EventLagEvents, EventGapsTotal: durable.EventGapsTotal,
			OutputTruncationsTotal: durable.OutputTruncationsTotal,
			StorageErrorsTotal:     durable.StorageErrorsTotal, CleanupFailuresTotal: durable.CleanupFailuresTotal,
			MailboxBacklog: durable.MailboxBacklog,
		}
		thresholds.LogThresholds(slog.Default(), "mac_local_executor", metrics)
	}
	observe()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			observe()
		}
	}
}
