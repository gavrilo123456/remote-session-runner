package runnerd

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/opshealth"
	hostruntime "remote-session-runner/src/internal/runtime"
	"remote-session-runner/src/internal/store"
)

func runDoctor(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("runnerd doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "owner-only Linux runner configuration")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *configPath == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "runnerd doctor: --config is required")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	report := diagnoseLinuxRunner(ctx, *configPath)
	if err := opshealth.WriteDoctor(stdout, report); err != nil {
		fmt.Fprintln(stderr, "runnerd doctor: could not write health report")
		return 1
	}
	if report.Readiness != opshealth.StateReady {
		return 1
	}
	return 0
}

func diagnoseLinuxRunner(ctx context.Context, configPath string) opshealth.Report {
	checks := make([]opshealth.Check, 0, 3)
	loaded, err := config.LoadFile(configPath)
	if err != nil {
		component, reason := "configuration", "configuration_not_ready"
		if errors.Is(err, config.ErrConfigHostProfile) {
			component, reason = "linux_host_profile", "host_profile_not_ready"
		} else if errors.Is(err, config.ErrInvalidSecretRef) {
			component, reason = "direct_mtls", "mtls_configuration_not_ready"
		}
		checks = append(checks, opshealth.Check{Component: component, State: opshealth.StateNotReady, Reason: reason, RequiredForReadiness: true})
		return opshealth.NewReport("linux_runnerd", time.Now(), checks...)
	}
	settings, ok := loaded.LinuxSettings()
	if !ok || loaded.Kind() != config.HostKindLinux {
		checks = append(checks, opshealth.Check{Component: "configuration", State: opshealth.StateNotReady, Reason: "configuration_not_ready", RequiredForReadiness: true})
		return opshealth.NewReport("linux_runnerd", time.Now(), checks...)
	}

	database := opshealth.Check{Component: "sqlite_writes", State: opshealth.StateReady, RequiredForReadiness: true}
	db, openErr := store.Open(ctx, settings.Database)
	var authority *store.AuthorityStore
	if openErr != nil {
		database.State = opshealth.StateNotReady
		database.Reason = "database_migration_or_write_failed"
	} else {
		defer db.Close()
		var authorityErr error
		authority, authorityErr = store.NewAuthorityStore(db)
		if authorityErr != nil || authority.CheckWritable(ctx, "linux_runnerd") != nil {
			database.State = opshealth.StateNotReady
			database.Reason = "database_write_failed"
		}
	}
	checks = append(checks, database)

	profile := opshealth.Check{Component: "linux_host_profile", State: opshealth.StateReady, RequiredForReadiness: true}
	profileDoctor, profileErr := hostruntime.NewLinuxProcessProfile(hostruntime.LinuxRuntimeOptions{
		Account: settings.Account, ServiceRoot: settings.ServiceRoot, WorkspaceRoot: settings.Workspaces, ShellPath: "/usr/bin/bash",
	})
	if profileErr != nil {
		profile.State = opshealth.StateNotReady
		profile.Reason = "host_profile_not_ready"
	} else if profileReport, err := profileDoctor.Doctor(ctx); err != nil || !profileReport.Ready {
		profile.State = opshealth.StateNotReady
		profile.Reason = "host_profile_not_ready"
	}
	checks = append(checks, profile)

	tls := opshealth.Check{Component: "direct_mtls", State: opshealth.StateReady, RequiredForReadiness: true}
	key, found := loaded.SecretReference(config.SecretLinuxServerTLSKey)
	if !found {
		tls.State = opshealth.StateNotReady
		tls.Reason = "mtls_configuration_not_ready"
	} else if _, err := NewDirectHTTPSServer(DirectHTTPSServerOptions{
		BindAddress: settings.DirectHTTPSBind, ServerCertificate: settings.ServerCertificate,
		ServerPrivateKey: key.File, ClientCA: settings.ClientCA,
		ClientPrincipalMap: settings.ClientPrincipalMap, Handler: http.NotFoundHandler(),
	}); err != nil {
		tls.State = opshealth.StateNotReady
		tls.Reason = "mtls_configuration_not_ready"
	}
	checks = append(checks, tls)
	report := opshealth.NewReport("linux_runnerd", time.Now(), checks...)
	if authority != nil {
		if durable, err := authority.ReadOperationalMetrics(ctx); err == nil {
			metrics := opshealth.Metrics{
				ActiveSessionSlots: durable.ActiveSessionSlots, ActiveCommandSlots: durable.ActiveCommandSlots,
				QueuedCommands: durable.QueuedCommands, QueuedIntents: durable.QueuedIntents,
				DispatchAttemptsTotal: durable.DispatchAttemptsTotal, ReconciliationAgeSeconds: durable.ReconciliationAgeSeconds,
				EventLagEvents: durable.EventLagEvents, EventGapsTotal: durable.EventGapsTotal,
				OutputTruncationsTotal: durable.OutputTruncationsTotal,
				StorageErrorsTotal:     durable.StorageErrorsTotal, CleanupFailuresTotal: durable.CleanupFailuresTotal,
				MailboxBacklog: durable.MailboxBacklog,
			}
			report = opshealth.AddMetrics(report, metrics, nil, nil, nil)
		}
	}
	return report
}

func linuxRunnerHealthReport(ctx context.Context, authority *store.AuthorityStore, profileReady, tlsReady bool) opshealth.Report {
	database := opshealth.Check{Component: "sqlite_writes", State: opshealth.StateReady, RequiredForReadiness: true}
	if authority == nil || authority.CheckWritable(ctx, "linux_runnerd") != nil {
		database.State = opshealth.StateNotReady
		database.Reason = "database_write_failed"
	}
	profile := opshealth.Check{Component: "linux_host_profile", State: opshealth.StateReady, RequiredForReadiness: true}
	if !profileReady {
		profile.State = opshealth.StateNotReady
		profile.Reason = "host_profile_not_ready"
	}
	tls := opshealth.Check{Component: "direct_mtls", State: opshealth.StateReady, RequiredForReadiness: true}
	if !tlsReady {
		tls.State = opshealth.StateNotReady
		tls.Reason = "mtls_configuration_not_ready"
	}
	return opshealth.NewReport("linux_runnerd", time.Now(), database, profile, tls)
}

func linuxRunnerHealthReportWithMetrics(ctx context.Context, authority *store.AuthorityStore, profileReady, tlsReady bool, thresholds *opshealth.ThresholdMonitor) opshealth.Report {
	report := linuxRunnerHealthReport(ctx, authority, profileReady, tlsReady)
	if authority == nil {
		return report
	}
	durable, err := authority.ReadOperationalMetrics(ctx)
	if err != nil {
		return report
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
	return opshealth.AddMetrics(report, metrics, nil, thresholds, nil)
}
