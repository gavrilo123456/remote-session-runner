// Package runnerlocal composes the Mac ingress, mailbox, and Router process.
// The local execution authority remains in the separate runner-locald process.
package runnerlocal

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"remote-session-runner/src/internal/commandstub"
	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/dispatcher"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/localapi"
	"remote-session-runner/src/internal/mailbox"
	"remote-session-runner/src/internal/opshealth"
	"remote-session-runner/src/internal/sshclient"
	"remote-session-runner/src/internal/store"
)

const (
	defaultPollInterval = 250 * time.Millisecond
	defaultDrainLimit   = 64
	intentLeaseDuration = 2 * time.Minute
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
	database        *store.AuthorityStore
	dbCloser        interface{ Close() error }
	api             *localapi.Server
	localDriver     *dispatcher.LocalDriver
	remoteDriver    *dispatcher.RemoteDriver
	mailbox         *mailbox.SessionProcessor
	ackImporter     *mailbox.AckImporter
	artifactCleaner mailbox.ArtifactCleaner
	pollInterval    time.Duration
	routerHealth    *routerHealthMonitor
	remoteProbe     func(context.Context) error
	metricsRecorder *opshealth.Recorder
	thresholds      *opshealth.ThresholdMonitor
	mailboxImporter *mailbox.Importer
}

// New constructs the Mac services from an owner-restricted selected config.
// It creates only the configured owner-only service directories and never
// reads or logs secret-file contents.
func New(configPath string) (*Service, error) {
	if err := ensureMacServiceRoot(config.MacServiceRoot); err != nil {
		return nil, err
	}
	loaded, err := config.LoadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("load Mac configuration: %w", err)
	}
	settings, ok := loaded.MacSettings()
	if !ok || loaded.Kind() != config.HostKindMac || settings.Account != config.MacAccount {
		return nil, errors.New("selected Mac host configuration is required")
	}
	if err := ensureMacServicePaths(settings); err != nil {
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
	owner, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, domain.ControllerID(settings.Account))
	if err != nil {
		return nil, fmt.Errorf("construct Mac owner identity: %w", err)
	}
	routerHealth := newRouterHealthMonitor()
	metricsRecorder := opshealth.NewRecorder()
	thresholds := opshealth.NewThresholdMonitor()
	var metricsImporter *mailbox.Importer
	api, err := localapi.NewServer(localapi.ServerOptions{
		Authority: authority, Owner: owner, SocketPath: settings.APISocket,
		HealthReport: func(ctx context.Context) opshealth.Report {
			return macIngressHealthReportWithMetrics(ctx, authority, routerHealth, metricsImporter, metricsRecorder, thresholds)
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
	dispatcherKey, ok := loaded.SecretReference(config.SecretDispatcherSSHKey)
	if !ok {
		return nil, errors.New("dispatcher SSH key reference is missing")
	}
	ssh, err := sshclient.New(sshclient.Config{
		User: "ubuntu", Host: "129.151.232.40", IdentityFile: dispatcherKey.File,
		KnownHostsFile: settings.SSHKnownHosts,
	})
	if err != nil {
		return nil, fmt.Errorf("construct pinned SSH bridge client: %w", err)
	}
	remoteDriver, err := dispatcher.NewRemoteDriver(authority, ssh, routerOwner, intentLeaseDuration)
	if err != nil {
		return nil, fmt.Errorf("construct remote Router driver: %w", err)
	}
	importer, err := mailbox.NewImporter(settings.MailboxRoot, nil)
	if err != nil {
		return nil, fmt.Errorf("construct mailbox importer: %w", err)
	}
	metricsImporter = importer
	outbox, err := mailbox.NewOutbox(settings.MailboxRoot)
	if err != nil {
		return nil, fmt.Errorf("construct mailbox outbox: %w", err)
	}
	eventFiles, err := mailbox.NewEventFiles(settings.MailboxRoot)
	if err != nil {
		return nil, fmt.Errorf("construct mailbox event files: %w", err)
	}
	processor, err := mailbox.NewSessionProcessor(mailbox.SessionProcessorOptions{
		Importer: importer, Authority: authority, Controller: owner, Operations: api,
		Outbox: outbox, EventFiles: eventFiles,
		RemoteUncertaintyWindow: settings.ReconciliationDeadline,
	})
	if err != nil {
		return nil, fmt.Errorf("construct mailbox processor: %w", err)
	}
	ackImporter, err := mailbox.NewAckImporter(mailbox.AckImporterOptions{Root: settings.MailboxRoot, Authority: authority})
	if err != nil {
		return nil, fmt.Errorf("construct mailbox ACK importer: %w", err)
	}
	service := &Service{
		database: authority, dbCloser: db, api: api, localDriver: localDriver,
		remoteDriver: remoteDriver, mailbox: processor, ackImporter: ackImporter,
		artifactCleaner: mailbox.ArtifactCleaner{Authority: authority, Outbox: outbox, EventFiles: eventFiles}, routerHealth: routerHealth,
		pollInterval: defaultPollInterval, remoteProbe: remoteDriver.Probe,
		metricsRecorder: metricsRecorder, thresholds: thresholds, mailboxImporter: metricsImporter,
	}
	closeOnError = false
	return service, nil
}

// Serve starts the owner-only Unix API and dispatch/mailbox workers. Shutdown
// closes the listener before returning so launchd restarts cannot inherit a
// stale socket pathname.
func (s *Service) Serve(ctx context.Context, stdout, stderr io.Writer) (returnErr error) {
	if s == nil || s.api == nil || s.database == nil || s.dbCloser == nil || s.localDriver == nil || s.remoteDriver == nil || s.mailbox == nil || s.ackImporter == nil {
		return errors.New("Mac service is not configured")
	}
	defer func() { returnErr = errors.Join(returnErr, s.dbCloser.Close()) }()
	if ctx == nil {
		ctx = context.Background()
	}
	workerContext, cancelWorkers := context.WithCancel(ctx)
	defer cancelWorkers()
	if err := s.api.Listen(); err != nil {
		return fmt.Errorf("listen on local API socket: %w", err)
	}
	fmt.Fprintf(stdout, "runner-local listening on %s\n", s.api.SocketPath())
	serveErr := make(chan error, 1)
	go func() { serveErr <- s.api.Serve() }()
	workersDone := make(chan struct{})
	go func() {
		defer close(workersDone)
		s.runWorkers(workerContext, stderr)
	}()
	select {
	case err := <-serveErr:
		cancelWorkers()
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		closeErr := s.api.Close(stopCtx)
		workerErr := waitWorkers(workersDone, stopCtx)
		return errors.Join(err, closeErr, workerErr)
	case <-ctx.Done():
		cancelWorkers()
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		closeErr := s.api.Close(stopCtx)
		serveResult := <-serveErr
		workerErr := waitWorkers(workersDone, stopCtx)
		if errors.Is(serveResult, http.ErrServerClosed) {
			serveResult = nil
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return errors.Join(closeErr, serveResult, workerErr)
		}
		return errors.Join(ctx.Err(), closeErr, serveResult, workerErr)
	}
}

func (s *Service) runWorkers(ctx context.Context, stderr io.Writer) {
	probeDone := make(chan struct{})
	go func() {
		defer close(probeDone)
		s.runRemoteHealthProbe(ctx)
	}()
	defer func() { <-probeDone }()
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
		if ctx.Err() != nil {
			return
		}
		s.runCycle(ctx, stderr)
		select {
		case <-ctx.Done():
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
	if s.mailboxImporter != nil {
		pending, err := s.mailboxImporter.ReadyRequestCount(ctx)
		if err != nil {
			return
		}
		metrics.MailboxBacklog += pending
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

func (s *Service) runCycle(ctx context.Context, stderr io.Writer) {
	if _, err := s.mailbox.Import(ctx); err != nil && ctx.Err() == nil {
		s.recordOperationalError(err, false)
		fmt.Fprintln(stderr, "runner-local: mailbox import cycle failed")
	}
	if err := s.mailbox.Reconcile(ctx); err != nil && ctx.Err() == nil {
		s.recordOperationalError(err, false)
		fmt.Fprintln(stderr, "runner-local: mailbox reconciliation cycle failed")
	}
	if _, err := s.ackImporter.Import(ctx); err != nil && ctx.Err() == nil {
		s.recordOperationalError(err, false)
		fmt.Fprintln(stderr, "runner-local: mailbox ACK cycle failed")
	}
	if _, err := s.artifactCleaner.Run(ctx); err != nil && ctx.Err() == nil {
		s.recordOperationalError(err, true)
		fmt.Fprintln(stderr, "runner-local: mailbox cleanup cycle failed")
	}
	for i := 0; i < defaultDrainLimit && ctx.Err() == nil; i++ {
		_, _, err := s.localDriver.DispatchNext(ctx)
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
		_, _, err := s.remoteDriver.DispatchNext(ctx)
		if errors.Is(err, dispatcher.ErrNoRemoteDispatchWork) {
			break
		}
		if err != nil {
			s.recordOperationalError(err, false)
			s.routerHealth.update(err, time.Now())
			fmt.Fprintln(stderr, "runner-local: remote Router dispatch cycle failed")
			break
		}
		s.routerHealth.update(nil, time.Now())
	}
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
		s.routerHealth.update(s.probeRemote(probeContext), time.Now())
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
		s.routerHealth.update(s.probeRemote(probeContext), time.Now())
		cancel()
	}
	if s == nil {
		return opshealth.NewReport("mac_ingress", time.Now(), opshealth.Check{Component: "configuration", State: opshealth.StateNotReady, Reason: "service_configuration_not_ready", RequiredForReadiness: true})
	}
	return macIngressHealthReportWithMetrics(ctx, s.database, s.routerHealth, s.mailboxImporter, s.metricsRecorder, s.thresholds)
}

func (s *Service) probeRemote(ctx context.Context) error {
	if s == nil {
		return dispatcher.ErrRemoteDriverConfiguration
	}
	if s.remoteProbe != nil {
		return s.remoteProbe(ctx)
	}
	if s.remoteDriver == nil {
		return dispatcher.ErrRemoteDriverConfiguration
	}
	return s.remoteDriver.Probe(ctx)
}

func waitWorkers(done <-chan struct{}, ctx context.Context) error {
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
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

func ensureMacServicePaths(settings config.MacSettings) error {
	paths := []string{
		filepath.Join(settings.ServiceRoot, "bin"), filepath.Join(settings.ServiceRoot, "config"),
		filepath.Join(settings.ServiceRoot, "logs"), filepath.Dir(settings.APISocket),
		filepath.Dir(settings.LocalDSocket), filepath.Dir(settings.Database), settings.MailboxRoot,
		settings.Workspaces, settings.ScriptTempRoot, settings.Backups,
		filepath.Join(settings.ServiceRoot, "secrets"),
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
