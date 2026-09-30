// Package runnerlocal composes the Mac ingress, mailbox, and Router process.
// The local execution authority remains in the separate runner-locald process.
package runnerlocal

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"remote-session-runner/src/internal/commandstub"
	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/dispatcher"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/lifecycle"
	"remote-session-runner/src/internal/localapi"
	"remote-session-runner/src/internal/mailbox"
	"remote-session-runner/src/internal/opshealth"
	"remote-session-runner/src/internal/store"
)

const (
	defaultPollInterval                  = 250 * time.Millisecond
	defaultDrainLimit                    = 64
	intentLeaseDuration                  = 2 * time.Minute
	acceptedRemoteReconciliationInterval = time.Second
)

var errMacDatabaseNotReady = errors.New("Mac authority database is not ready")

// Run loads the selected Mac configuration and serves local ingress until
// launchd sends SIGTERM or the process receives an interrupt.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help" || args[0] == "--version" || args[0] == "version") {
		return commandstub.Run("runner-local", "Mac-local API, mailbox, router, and dispatcher.", args, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "doctor" {
		return runDoctor(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "validate-config" {
		return runValidateConfig(args[1:], stdout, stderr)
	}
	flags := flag.NewFlagSet("runner-local", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "owner-only Mac runner configuration")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *configPath == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "runner-local: --config is required")
		return 2
	}
	service, err := New(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "runner-local: configure service: %v\n", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := service.Serve(ctx, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "runner-local: service stopped: %v\n", err)
		return 1
	}
	return 0
}

// runValidateConfig is the installer-safe configuration check. It creates no
// service paths and makes no remote probe. With --check-retained-mailboxes it
// additionally opens an existing authority database read-only, so an inbox
// removal with live work is rejected before LaunchAgents are replaced. Schema
// 24 is checked as its implicit default inbox without migration; the later
// activation operation is intentionally after the installer's no-rollback
// boundary. Its output deliberately contains only schema and configured inbox
// identifiers, never paths or secret references.
func runValidateConfig(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("runner-local validate-config", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "owner-only Mac runner configuration")
	checkRetainedMailboxes := flags.Bool("check-retained-mailboxes", false, "read existing mailbox state before replacing services")
	activateMailboxSet := flags.Bool("activate-mailbox-set", false, "record the candidate inbox set at the installer activation boundary")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *configPath == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "runner-local validate-config: --config is required")
		return 2
	}
	if *activateMailboxSet && !*checkRetainedMailboxes {
		fmt.Fprintln(stderr, "runner-local validate-config: --activate-mailbox-set requires --check-retained-mailboxes")
		return 2
	}
	loaded, settings, mailboxes, err := loadSelectedMacConfig(*configPath)
	if err != nil {
		fmt.Fprintln(stderr, "runner-local validate-config: configuration is invalid")
		return 1
	}
	if *checkRetainedMailboxes {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := validateRetainedMailboxSet(ctx, settings.Database, mailboxes); err != nil {
			fmt.Fprintln(stderr, "runner-local validate-config: configured inbox set is not safe to install")
			return 1
		}
	}
	if *activateMailboxSet {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := registerConfiguredMailboxWork(ctx, settings.Database, mailboxes); err != nil {
			fmt.Fprintln(stderr, "runner-local validate-config: candidate inbox set could not be activated")
			return 1
		}
	}
	ids := make([]string, 0, len(mailboxes))
	for _, definition := range mailboxes {
		ids = append(ids, definition.ID)
	}
	fmt.Fprintf(stdout, "runner-local configuration valid: schema_version=%d inboxes=%s\n", loaded.SchemaVersion(), strings.Join(ids, ","))
	return 0
}

func runDoctor(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("runner-local doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "owner-only Mac runner configuration")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *configPath == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "runner-local doctor: --config is required")
		return 2
	}
	service, err := New(*configPath)
	if err != nil {
		_ = opshealth.WriteDoctor(stdout, macDoctorStartupFailureReport(err))
		return 1
	}
	defer service.dbCloser.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	report := service.Doctor(ctx)
	if err := opshealth.WriteDoctor(stdout, report); err != nil {
		fmt.Fprintln(stderr, "runner-local doctor: could not write health report")
		return 1
	}
	if report.Readiness != opshealth.StateReady {
		return 1
	}
	return 0
}

func macDoctorStartupFailureReport(err error) opshealth.Report {
	component, reason := "configuration", "service_configuration_not_ready"
	if errors.Is(err, errMacDatabaseNotReady) {
		component, reason = "sqlite_writes", "database_migration_or_write_failed"
	}
	return opshealth.NewReport("mac_ingress", time.Now(), opshealth.Check{Component: component, State: opshealth.StateNotReady, Reason: reason, RequiredForReadiness: true})
}

// Service owns the process-level composition for the Mac ingress and Router.
type Service struct {
	database            *store.AuthorityStore
	dbCloser            interface{ Close() error }
	api                 *localapi.Server
	localDriver         *dispatcher.LocalDriver
	remoteDriver        *dispatcher.RemoteDriver
	mailboxes           []mailboxRuntime
	mailboxDefinitions  []store.MailboxConfiguration
	legacyMailboxSet    []store.MailboxConfiguration
	pollInterval        time.Duration
	routerHealth        *routerHealthMonitor
	remoteProbe         func(context.Context) map[string]error
	metricsRecorder     *opshealth.Recorder
	thresholds          *opshealth.ThresholdMonitor
	remoteReconcileMu   sync.Mutex
	lastRemoteReconcile time.Time
}

// mailboxRuntime owns the filesystem-facing components for exactly one
// configured inbox. The authority, local API, Router, and dispatcher remain
// shared process services; a mailbox never becomes an execution authority.
type mailboxRuntime struct {
	id              string
	importer        *mailbox.Importer
	processor       *mailbox.SessionProcessor
	ackImporter     *mailbox.AckImporter
	artifactCleaner mailbox.ArtifactCleaner
}

// New constructs the Mac services from an owner-restricted selected config.
// It creates only the configured owner-only service directories and never
// reads or logs secret-file contents.
func New(configPath string) (*Service, error) {
	if err := ensureMacServiceRoot(config.MacServiceRoot); err != nil {
		return nil, err
	}
	loaded, settings, mailboxDefinitions, err := loadSelectedMacConfig(configPath)
	if err != nil {
		return nil, err
	}
	mailboxRoots := make([]string, 0, len(mailboxDefinitions))
	for _, definition := range mailboxDefinitions {
		mailboxRoots = append(mailboxRoots, definition.Root)
	}
	// Open only the existing state directory before validating that this
	// configuration has not removed an inbox with durable work. The complete
	// configured mailbox tree is created only after that check succeeds.
	if err := ensureOwnedDirectoryUnder(settings.ServiceRoot, filepath.Dir(settings.Database)); err != nil {
		return nil, err
	}
	ctx := context.Background()
	db, err := store.Open(ctx, settings.Database)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errMacDatabaseNotReady, err)
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = db.Close()
		}
	}()
	authority, err := store.NewAuthorityStore(db)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errMacDatabaseNotReady, err)
	}
	if err := validateConfiguredMailboxWork(ctx, authority, mailboxDefinitions); err != nil {
		return nil, fmt.Errorf("validate configured mailbox work: %w", err)
	}
	if err := ensureMacServicePaths(settings, mailboxRoots...); err != nil {
		return nil, err
	}
	owner, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, domain.ControllerID(settings.Account))
	if err != nil {
		return nil, fmt.Errorf("construct Mac owner identity: %w", err)
	}
	var routerHealth *routerHealthMonitor
	metricsRecorder := opshealth.NewRecorder()
	thresholds := opshealth.NewThresholdMonitor()
	var metricsImporters []*mailbox.Importer
	api, err := localapi.NewServer(localapi.ServerOptions{
		Authority: authority, Owner: owner, SocketPath: settings.APISocket,
		HealthReport: func(ctx context.Context) opshealth.Report {
			return macIngressHealthReportWithMetrics(ctx, authority, routerHealth, metricsImporters, metricsRecorder, thresholds)
		},
	})
	if err != nil {
		return nil, fmt.Errorf("construct local API: %w", err)
	}
	locald, err := dispatcher.NewLocaldClient(settings.LocalDSocket)
	if err != nil {
		return nil, fmt.Errorf("construct locald client: %w", err)
	}
	routerOwner := fmt.Sprintf("mac-router-%d", os.Getpid())
	localDriver, err := dispatcher.NewLocalDriver(authority, locald, routerOwner, intentLeaseDuration)
	if err != nil {
		return nil, fmt.Errorf("construct local Router driver: %w", err)
	}
	remoteResolver, err := newPinnedRemoteCallerResolver(loaded)
	if err != nil {
		return nil, fmt.Errorf("construct pinned SSH bridge routes: %w", err)
	}
	remoteDriver, err := dispatcher.NewRemoteDriverWithResolver(authority, remoteResolver, routerOwner, intentLeaseDuration)
	if err != nil {
		return nil, fmt.Errorf("construct remote Router driver: %w", err)
	}
	routerHealth = newRouterHealthMonitorForProfiles(remoteDriver.RemoteProfiles())
	mailboxes, err := composeMailboxRuntimes(mailboxDefinitions, authority, owner, api, &loaded, settings.ReconciliationDeadline)
	if err != nil {
		return nil, err
	}
	metricsImporters = mailboxRuntimeImporters(mailboxes)
	service := &Service{
		database: authority, dbCloser: db, api: api, localDriver: localDriver,
		remoteDriver: remoteDriver, mailboxes: mailboxes, routerHealth: routerHealth,
		mailboxDefinitions: configuredMailboxDefinitions(mailboxDefinitions),
		legacyMailboxSet:   legacyDefaultMailboxBaseline(),
		pollInterval:       defaultPollInterval, remoteProbe: remoteDriver.ProbeProfiles,
		metricsRecorder: metricsRecorder, thresholds: thresholds,
	}
	closeOnError = false
	return service, nil
}

// loadSelectedMacConfig validates the selected owner-only document and
// returns its complete, deterministic mailbox registry. It is deliberately
// filesystem-neutral so the installer can validate policy before it replaces
// LaunchAgents or starts a service.
func loadSelectedMacConfig(configPath string) (config.Config, config.MacSettings, []config.MailboxDefinition, error) {
	loaded, err := config.LoadFile(configPath)
	if err != nil {
		return config.Config{}, config.MacSettings{}, nil, fmt.Errorf("load Mac configuration: %w", err)
	}
	settings, ok := loaded.MacSettings()
	if !ok || loaded.Kind() != config.HostKindMac || settings.Account != config.MacAccount {
		return config.Config{}, config.MacSettings{}, nil, errors.New("selected Mac host configuration is required")
	}
	definitions := make([]config.MailboxDefinition, 0, len(loaded.MailboxNames()))
	for _, mailboxID := range loaded.MailboxNames() {
		definition, exists := loaded.Mailbox(mailboxID)
		if !exists || definition.ID != mailboxID || definition.Root == "" {
			return config.Config{}, config.MacSettings{}, nil, errors.New("configured Mac mailbox is invalid")
		}
		definitions = append(definitions, definition)
	}
	if len(definitions) == 0 || definitions[0].ID == "" {
		return config.Config{}, config.MacSettings{}, nil, errors.New("default Mac mailbox configuration is required")
	}
	defaultMailbox, ok := loaded.Mailbox(store.DefaultMailboxID)
	if !ok || defaultMailbox.Root == "" {
		return config.Config{}, config.MacSettings{}, nil, errors.New("default Mac mailbox configuration is required")
	}
	return loaded, settings, definitions, nil
}

// validateConfiguredMailboxWork prevents a configuration edit from removing
// a runtime that still owns accepted work or a live unacknowledged response.
// It is intentionally called before any newly configured mailbox directory is
// created, so a failed restart cannot open a partial new layout.
func validateConfiguredMailboxWork(ctx context.Context, authority *store.AuthorityStore, definitions []config.MailboxDefinition) error {
	if authority == nil || len(definitions) == 0 {
		return errors.New("configured mailbox work cannot be validated")
	}
	return authority.ValidateConfiguredMailboxSet(ctx, configuredMailboxDefinitions(definitions), legacyDefaultMailboxBaseline())
}

func validateRetainedMailboxSet(ctx context.Context, databasePath string, definitions []config.MailboxDefinition) error {
	return store.ValidateConfiguredMailboxSetAtPath(ctx, databasePath, configuredMailboxDefinitions(definitions), legacyDefaultMailboxBaseline())
}

// registerConfiguredMailboxWork is used only at the installer's irreversible
// activation boundary. It records the complete candidate before its paths can
// be opened, so a failed candidate cannot later be replaced by a configuration
// that strands marker-last work in a newly introduced root.
func registerConfiguredMailboxWork(ctx context.Context, databasePath string, definitions []config.MailboxDefinition) error {
	db, err := store.Open(ctx, databasePath)
	if err != nil {
		return err
	}
	defer db.Close()
	authority, err := store.NewAuthorityStore(db)
	if err != nil {
		return err
	}
	return authority.RegisterConfiguredMailboxSet(ctx, configuredMailboxDefinitions(definitions), legacyDefaultMailboxBaseline())
}

func configuredMailboxDefinitions(definitions []config.MailboxDefinition) []store.MailboxConfiguration {
	mailboxes := make([]store.MailboxConfiguration, 0, len(definitions))
	for _, definition := range definitions {
		mailboxes = append(mailboxes, store.MailboxConfiguration{ID: definition.ID, Root: definition.Root})
	}
	return mailboxes
}

func legacyDefaultMailboxBaseline() []store.MailboxConfiguration {
	return []store.MailboxConfiguration{{
		ID: store.DefaultMailboxID, Root: filepath.Join(config.MacServiceRoot, "mailbox"),
	}}
}

func composeMailboxRuntimes(definitions []config.MailboxDefinition, authority *store.AuthorityStore, owner domain.ControllerIdentity, operations mailbox.SessionOperations, resolver mailbox.MailboxExecutionResolver, reconciliationDeadline time.Duration) ([]mailboxRuntime, error) {
	if authority == nil || operations == nil || resolver == nil || len(definitions) == 0 {
		return nil, errors.New("construct mailbox runtimes: configuration is incomplete")
	}
	runtimes := make([]mailboxRuntime, 0, len(definitions))
	seen := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		if definition.ID == "" || definition.Root == "" {
			return nil, errors.New("construct mailbox runtimes: configured mailbox is invalid")
		}
		if _, exists := seen[definition.ID]; exists {
			return nil, errors.New("construct mailbox runtimes: configured mailbox is duplicated")
		}
		seen[definition.ID] = struct{}{}
		importer, err := mailbox.New(mailbox.Options{MailboxID: definition.ID, Root: definition.Root})
		if err != nil {
			return nil, fmt.Errorf("construct mailbox importer for %s: %w", definition.ID, err)
		}
		outbox, err := mailbox.NewOutbox(definition.Root)
		if err != nil {
			return nil, fmt.Errorf("construct mailbox outbox for %s: %w", definition.ID, err)
		}
		eventFiles, err := mailbox.NewEventFiles(definition.Root)
		if err != nil {
			return nil, fmt.Errorf("construct mailbox event files for %s: %w", definition.ID, err)
		}
		processor, err := mailbox.NewSessionProcessor(mailbox.SessionProcessorOptions{
			MailboxID: definition.ID, Importer: importer, Authority: authority, Controller: owner, Operations: operations,
			Outbox: outbox, EventFiles: eventFiles, ExecutionResolver: resolver,
			RemoteUncertaintyWindow: reconciliationDeadline,
		})
		if err != nil {
			return nil, fmt.Errorf("construct mailbox processor for %s: %w", definition.ID, err)
		}
		ackImporter, err := mailbox.NewAckImporter(mailbox.AckImporterOptions{MailboxID: definition.ID, Root: definition.Root, Authority: authority})
		if err != nil {
			return nil, fmt.Errorf("construct mailbox ACK importer for %s: %w", definition.ID, err)
		}
		runtimes = append(runtimes, mailboxRuntime{
			id: definition.ID, importer: importer, processor: processor, ackImporter: ackImporter,
			artifactCleaner: mailbox.ArtifactCleaner{MailboxID: definition.ID, Authority: authority, Outbox: outbox, EventFiles: eventFiles},
		})
	}
	return runtimes, nil
}

func mailboxRuntimeImporters(runtimes []mailboxRuntime) []*mailbox.Importer {
	importers := make([]*mailbox.Importer, 0, len(runtimes))
	for _, runtime := range runtimes {
		importers = append(importers, runtime.importer)
	}
	return importers
}

// Serve starts the owner-only Unix API and dispatch/mailbox workers. Shutdown
// closes the listener before returning so launchd restarts cannot inherit a
// stale socket pathname.
func (s *Service) Serve(ctx context.Context, stdout, stderr io.Writer) (returnErr error) {
	if s == nil || s.api == nil || s.database == nil || s.dbCloser == nil || s.localDriver == nil || s.remoteDriver == nil || len(s.mailboxes) == 0 {
		return errors.New("Mac service is not configured")
	}
	defer func() { returnErr = errors.Join(returnErr, s.dbCloser.Close()) }()
	if ctx == nil {
		ctx = context.Background()
	}
	workerContext, cancelWorkers := context.WithCancel(context.Background())
	defer cancelWorkers()
	dispatchGate := lifecycle.NewGate()
	stopCycles := make(chan struct{})
	var stopCyclesOnce sync.Once
	if err := s.api.Listen(); err != nil {
		return fmt.Errorf("listen on local API socket: %w", err)
	}
	// The installer records its candidate roots before handing off mac.yaml.
	// Repeat that operation after the fully composed service acquires its
	// listener, before mailbox workers begin, so direct starts also bind every
	// marker the service can observe to a recorded root. A construction failure
	// before this call leaves registry state unchanged when no installer boundary
	// was crossed.
	if err := s.activateConfiguredMailboxSet(ctx); err != nil {
		_ = s.api.Close(context.Background())
		return fmt.Errorf("activate configured mailbox set: %w", err)
	}
	fmt.Fprintf(stdout, "runner-local listening on %s\n", s.api.SocketPath())
	serveErr := make(chan error, 1)
	go func() { serveErr <- s.api.Serve() }()
	workersDone := make(chan struct{})
	go func() {
		defer close(workersDone)
		s.runWorkers(workerContext, stopCycles, dispatchGate, stderr)
	}()
	hooks := &macIngressShutdown{
		api: s.api, dispatchGate: dispatchGate, stopCycles: stopCycles,
		stopCyclesOnce: &stopCyclesOnce, cancelWorkers: cancelWorkers,
		workersDone: workersDone,
	}
	coordinator, err := lifecycle.NewCoordinator(hooks, lifecycle.RealClock{}, lifecycle.Config{
		DrainTimeout: 8 * time.Second, CleanupTimeout: 5 * time.Second,
	})
	if err != nil {
		cancelWorkers()
		_ = s.api.Close(context.Background())
		return err
	}
	select {
	case err := <-serveErr:
		shutdownErr := coordinator.Shutdown(context.Background())
		return errors.Join(err, shutdownErr)
	case <-ctx.Done():
		shutdownErr := coordinator.Shutdown(context.Background())
		serveResult := <-serveErr
		if errors.Is(ctx.Err(), context.Canceled) {
			return errors.Join(shutdownErr, serveResult)
		}
		return errors.Join(ctx.Err(), shutdownErr, serveResult)
	}
}

func (s *Service) activateConfiguredMailboxSet(ctx context.Context) error {
	if s == nil || s.database == nil {
		return errors.New("Mac service mailbox configuration is unavailable")
	}
	return s.database.RegisterConfiguredMailboxSet(ctx, s.mailboxDefinitions, s.legacyMailboxSet)
}

func (s *Service) runWorkers(ctx context.Context, stopCycles <-chan struct{}, dispatchGate *lifecycle.Gate, stderr io.Writer) {
	probeContext, cancelProbe := context.WithCancel(ctx)
	probeDone := make(chan struct{})
	go func() {
		defer close(probeDone)
		s.runRemoteHealthProbe(probeContext)
	}()
	defer func() {
		cancelProbe()
		<-probeDone
	}()
	interval := s.pollInterval
	if interval <= 0 {
		interval = defaultPollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	metricsTicker := time.NewTicker(30 * time.Second)
	defer metricsTicker.Stop()
	s.logOperationalMetrics(ctx)
	for {
		select {
		case <-stopCycles:
			return
		default:
		}
		if ctx.Err() != nil {
			return
		}
		s.runCycle(ctx, dispatchGate, stderr)
		select {
		case <-ctx.Done():
			return
		case <-stopCycles:
			return
		case <-ticker.C:
		case <-metricsTicker.C:
			s.logOperationalMetrics(ctx)
		}
	}
}

func (s *Service) logOperationalMetrics(ctx context.Context) {
	if s == nil || s.database == nil {
		return
	}
	durable, err := s.database.ReadOperationalMetrics(ctx)
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
	mailboxBacklog, readyTotal, err := mailboxBacklogByInbox(ctx, s.database, mailboxRuntimeImporters(s.mailboxes))
	if err != nil {
		return
	}
	if len(mailboxBacklog) != 0 {
		metrics.MailboxBacklogByInbox = mailboxBacklog
		metrics.MailboxBacklog += readyTotal
	}
	if s.thresholds != nil {
		s.thresholds.LogThresholds(nil, "mac_ingress", withRecorder(metrics, s.metricsRecorder))
	}
}

func withRecorder(metrics opshealth.Metrics, recorder *opshealth.Recorder) opshealth.Metrics {
	if recorder != nil {
		recorder.AddTo(&metrics)
	}
	return metrics
}

func (s *Service) runCycle(ctx context.Context, dispatchGate *lifecycle.Gate, stderr io.Writer) {
	s.runMailboxCycles(ctx, stderr)
	for i := 0; i < defaultDrainLimit && ctx.Err() == nil; i++ {
		release, gateErr := dispatchGate.Enter()
		if gateErr != nil {
			break
		}
		_, _, err := s.localDriver.DispatchNext(ctx)
		release()
		if errors.Is(err, dispatcher.ErrNoLocalDispatchWork) {
			break
		}
		if err != nil {
			s.recordOperationalError(err, false)
			fmt.Fprintln(stderr, "runner-local: local Router dispatch cycle failed")
			break
		}
	}
	for i := 0; i < defaultDrainLimit && ctx.Err() == nil; i++ {
		release, gateErr := dispatchGate.Enter()
		if gateErr != nil {
			break
		}
		_, _, err := s.remoteDriver.DispatchNext(ctx)
		release()
		if errors.Is(err, dispatcher.ErrNoRemoteDispatchWork) {
			break
		}
		if err != nil {
			s.recordOperationalError(err, false)
			fmt.Fprintln(stderr, "runner-local: remote Router dispatch cycle failed")
			break
		}
	}
	if ctx.Err() == nil && s.acceptedRemoteReconciliationDue(time.Now()) {
		reconciledRemoteWork := false
		release, gateErr := dispatchGate.Enter()
		if gateErr == nil {
			reconciledRemoteWork = true
			err := errors.Join(
				s.remoteDriver.ReconcileAcceptedRemoteSubmits(ctx, defaultDrainLimit),
				s.remoteDriver.ReconcileAcceptedRemoteRuns(ctx, defaultDrainLimit),
			)
			release()
			if err != nil {
				s.recordOperationalError(err, false)
				fmt.Fprintln(stderr, "runner-local: accepted remote work reconciliation cycle failed")
			}
		}
		if reconciledRemoteWork {
			// The first reconciliation runs before dispatch. Run it again after
			// read-only recovery so a terminal one-off can be published in this
			// same cycle rather than waiting for another mailbox tick.
			s.reconcileMailboxRuntimes(ctx, stderr, "post-recovery")
		}
	}
}

// runMailboxCycles services every configured mailbox in deterministic config
// order. An error from one root is recorded and reported without starving the
// remaining configured roots in the same cycle.
func (s *Service) runMailboxCycles(ctx context.Context, stderr io.Writer) {
	if s == nil {
		return
	}
	for _, runtime := range s.mailboxes {
		if runtime.processor == nil || runtime.ackImporter == nil {
			s.recordOperationalError(errors.New("mailbox runtime is not configured"), false)
			fmt.Fprintf(stderr, "runner-local: mailbox %s runtime is not configured\n", runtime.id)
			continue
		}
		if _, err := runtime.processor.Import(ctx); err != nil && ctx.Err() == nil {
			s.recordOperationalError(err, false)
			fmt.Fprintf(stderr, "runner-local: mailbox %s import cycle failed\n", runtime.id)
		}
		if err := runtime.processor.Reconcile(ctx); err != nil && ctx.Err() == nil {
			s.recordOperationalError(err, false)
			fmt.Fprintf(stderr, "runner-local: mailbox %s reconciliation cycle failed\n", runtime.id)
		}
		if _, err := runtime.ackImporter.Import(ctx); err != nil && ctx.Err() == nil {
			s.recordOperationalError(err, false)
			fmt.Fprintf(stderr, "runner-local: mailbox %s ACK cycle failed\n", runtime.id)
		}
		if _, err := runtime.artifactCleaner.Run(ctx); err != nil && ctx.Err() == nil {
			s.recordOperationalError(err, true)
			fmt.Fprintf(stderr, "runner-local: mailbox %s cleanup cycle failed\n", runtime.id)
		}
	}
}

func (s *Service) reconcileMailboxRuntimes(ctx context.Context, stderr io.Writer, stage string) {
	if s == nil {
		return
	}
	for _, runtime := range s.mailboxes {
		if runtime.processor == nil {
			s.recordOperationalError(errors.New("mailbox runtime is not configured"), false)
			fmt.Fprintf(stderr, "runner-local: mailbox %s %s reconciliation is not configured\n", runtime.id, stage)
			continue
		}
		if err := runtime.processor.Reconcile(ctx); err != nil && ctx.Err() == nil {
			s.recordOperationalError(err, false)
			fmt.Fprintf(stderr, "runner-local: mailbox %s %s reconciliation cycle failed\n", runtime.id, stage)
		}
	}
}

func (s *Service) acceptedRemoteReconciliationDue(now time.Time) bool {
	if s == nil {
		return false
	}
	s.remoteReconcileMu.Lock()
	defer s.remoteReconcileMu.Unlock()
	if s.lastRemoteReconcile.IsZero() || !now.Before(s.lastRemoteReconcile.Add(acceptedRemoteReconciliationInterval)) {
		s.lastRemoteReconcile = now
		return true
	}
	return false
}

func (s *Service) recordOperationalError(err error, cleanup bool) {
	if s == nil || err == nil {
		return
	}
	if cleanup && s.metricsRecorder != nil {
		s.metricsRecorder.RecordCleanupFailure()
	}
}

func (s *Service) runRemoteHealthProbe(ctx context.Context) {
	if s == nil || s.remoteDriver == nil || s.routerHealth == nil {
		return
	}
	probe := func() {
		probeContext, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		s.routerHealth.updateProfiles(s.probeRemote(probeContext), time.Now())
	}
	probe()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			probe()
		}
	}
}

// Doctor checks local durable ingress and probes the remote Router without
// making remote availability a prerequisite for local acceptance.
func (s *Service) Doctor(ctx context.Context) opshealth.Report {
	if ctx == nil {
		ctx = context.Background()
	}
	if s != nil && s.remoteDriver != nil && s.routerHealth != nil {
		probeContext, cancel := context.WithTimeout(ctx, 12*time.Second)
		s.routerHealth.updateProfiles(s.probeRemote(probeContext), time.Now())
		cancel()
	}
	if s == nil {
		return opshealth.NewReport("mac_ingress", time.Now(), opshealth.Check{Component: "configuration", State: opshealth.StateNotReady, Reason: "service_configuration_not_ready", RequiredForReadiness: true})
	}
	return macIngressHealthReportWithMetrics(ctx, s.database, s.routerHealth, mailboxRuntimeImporters(s.mailboxes), s.metricsRecorder, s.thresholds)
}

func (s *Service) probeRemote(ctx context.Context) map[string]error {
	if s == nil {
		return map[string]error{"router": dispatcher.ErrRemoteDriverConfiguration}
	}
	if s.remoteProbe != nil {
		return s.remoteProbe(ctx)
	}
	if s.remoteDriver == nil {
		return map[string]error{"router": dispatcher.ErrRemoteDriverConfiguration}
	}
	return s.remoteDriver.ProbeProfiles(ctx)
}

func waitWorkers(done <-chan struct{}, ctx context.Context) error {
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type macIngressShutdown struct {
	api            *localapi.Server
	dispatchGate   *lifecycle.Gate
	stopCycles     chan struct{}
	stopCyclesOnce *sync.Once
	cancelWorkers  context.CancelFunc
	workersDone    <-chan struct{}
	stopErr        error
}

func (h *macIngressShutdown) StopAccepting() {
	if h != nil && h.api != nil {
		h.stopErr = h.api.StopAccepting()
	}
}

func (h *macIngressShutdown) StopDispatch() {
	if h == nil {
		return
	}
	if h.dispatchGate != nil {
		h.dispatchGate.Stop()
	}
	if h.stopCycles != nil && h.stopCyclesOnce != nil {
		h.stopCyclesOnce.Do(func() { close(h.stopCycles) })
	}
}

func (h *macIngressShutdown) Drain(ctx context.Context) error {
	if h == nil || h.api == nil || h.dispatchGate == nil || h.workersDone == nil {
		return errors.New("Mac ingress shutdown is not configured")
	}
	apiErr := h.api.Drain(ctx)
	dispatchErr := h.dispatchGate.Wait(ctx)
	workersErr := waitWorkers(h.workersDone, ctx)
	return errors.Join(h.stopErr, apiErr, dispatchErr, workersErr)
}

func (h *macIngressShutdown) CancelRemaining(ctx context.Context) error {
	if h == nil || h.api == nil || h.cancelWorkers == nil || h.workersDone == nil {
		return errors.New("Mac ingress shutdown is not configured")
	}
	h.cancelWorkers()
	h.api.CancelRequests()
	return errors.Join(h.api.Drain(ctx), waitWorkers(h.workersDone, ctx))
}

func (h *macIngressShutdown) Flush(ctx context.Context) error {
	if h == nil || h.api == nil {
		return errors.New("Mac ingress shutdown is not configured")
	}
	return h.api.Flush(ctx)
}

func (h *macIngressShutdown) CloseStreams(context.Context) error {
	if h == nil || h.api == nil {
		return errors.New("Mac ingress shutdown is not configured")
	}
	return h.api.CloseStreams()
}

func ensureMacServiceRoot(root string) error {
	if root != config.MacServiceRoot || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return errors.New("selected Mac service root is invalid")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("inspect Mac service root: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("Mac service root must be a real directory")
	}
	if err := requireCurrentOwner(info); err != nil {
		return err
	}
	if info.Mode().Perm() != 0o700 {
		if err := os.Chmod(root, 0o700); err != nil {
			return fmt.Errorf("restrict Mac service root: %w", err)
		}
	}
	return nil
}

func ensureMacServicePaths(settings config.MacSettings, mailboxRoots ...string) error {
	paths := []string{
		filepath.Join(settings.ServiceRoot, "bin"), filepath.Join(settings.ServiceRoot, "config"),
		filepath.Join(settings.ServiceRoot, "logs"), filepath.Dir(settings.APISocket),
		filepath.Dir(settings.LocalDSocket), filepath.Dir(settings.Database),
		settings.Workspaces, settings.ScriptTempRoot, settings.Backups,
		filepath.Join(settings.ServiceRoot, "secrets"),
	}
	for _, mailboxRoot := range mailboxRoots {
		paths = append(paths,
			mailboxRoot,
			filepath.Join(mailboxRoot, "inbox"),
			filepath.Join(mailboxRoot, "outbox"),
			filepath.Join(mailboxRoot, "events"),
			filepath.Join(mailboxRoot, "acks"),
		)
	}
	for _, path := range paths {
		if err := ensureOwnedDirectoryUnder(settings.ServiceRoot, path); err != nil {
			return err
		}
	}
	return nil
}

func ensureOwnedDirectoryUnder(root, path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("service directory path is invalid")
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("service directory is outside the selected root")
	}
	current := root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		if err := os.Mkdir(current, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("create owner-only service directory: %w", err)
		}
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("service path must contain only real directories")
		}
		if err := requireCurrentOwner(info); err != nil {
			return err
		}
		if info.Mode().Perm() != 0o700 {
			if err := os.Chmod(current, 0o700); err != nil {
				return fmt.Errorf("restrict service directory: %w", err)
			}
		}
	}
	return nil
}

func requireCurrentOwner(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return errors.New("service directory owner does not match the current user")
	}
	return nil
}
