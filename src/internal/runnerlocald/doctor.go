package runnerlocald

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/opshealth"
	hostruntime "remote-session-runner/src/internal/runtime"
	"remote-session-runner/src/internal/store"
)

func runDoctor(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("runner-locald doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "owner-only Mac runner configuration")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *configPath == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "runner-locald doctor: --config is required")
		return 2
	}
	report := diagnoseMacLocalExecutor(*configPath)
	if err := opshealth.WriteDoctor(stdout, report); err != nil {
		fmt.Fprintln(stderr, "runner-locald doctor: could not write health report")
		return 1
	}
	if report.Readiness != opshealth.StateReady {
		return 1
	}
	return 0
}

func diagnoseMacLocalExecutor(configPath string) opshealth.Report {
	checks := make([]opshealth.Check, 0, 2)
	loaded, err := config.LoadFile(configPath)
	if err != nil {
		checks = append(checks, opshealth.Check{Component: "configuration", State: opshealth.StateNotReady, Reason: "configuration_not_ready", RequiredForReadiness: true})
		return opshealth.NewReport("mac_local_executor", time.Now(), checks...)
	}
	settings, ok := loaded.MacSettings()
	if !ok || loaded.Kind() != config.HostKindMac || settings.Account != config.MacAccount {
		checks = append(checks, opshealth.Check{Component: "mac_host_profile", State: opshealth.StateNotReady, Reason: "host_profile_not_ready", RequiredForReadiness: true})
		return opshealth.NewReport("mac_local_executor", time.Now(), checks...)
	}
	db, err := store.Open(context.Background(), settings.Database)
	if err != nil {
		checks = append(checks, opshealth.Check{Component: "sqlite_writes", State: opshealth.StateNotReady, Reason: "database_not_ready", RequiredForReadiness: true})
		return opshealth.NewReport("mac_local_executor", time.Now(), checks...)
	}
	defer db.Close()
	authority, err := store.NewAuthorityStore(db)
	if err != nil {
		checks = append(checks, opshealth.Check{Component: "sqlite_writes", State: opshealth.StateNotReady, Reason: "database_not_ready", RequiredForReadiness: true})
		return opshealth.NewReport("mac_local_executor", time.Now(), checks...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	dbCheck := opshealth.Check{Component: "sqlite_writes", State: opshealth.StateReady, RequiredForReadiness: true}
	if authority.CheckWritable(ctx, "mac_local_executor") != nil {
		dbCheck.State = opshealth.StateNotReady
		dbCheck.Reason = "database_write_failed"
	}
	cancel()
	checks = append(checks, dbCheck)

	profile := opshealth.Check{Component: "mac_host_profile", State: opshealth.StateReady, RequiredForReadiness: true}
	environments := make([]domain.Environment, 0, len(loaded.EnvironmentNames()))
	for _, name := range loaded.EnvironmentNames() {
		registered, present := loaded.Environment(name)
		if !present {
			profile.State = opshealth.StateNotReady
			profile.Reason = "host_profile_not_ready"
			break
		}
		environments = append(environments, registered.Policy())
	}
	if profile.State == opshealth.StateReady {
		if _, _, err := NewMacExecutionService(authority, hostruntime.MacRuntimeOptions{Account: settings.Account, WorkspaceRoot: settings.Workspaces, ShellPath: "/bin/bash"}, environments...); err != nil {
			profile.State = opshealth.StateNotReady
			profile.Reason = "host_profile_not_ready"
		}
	}
	checks = append(checks, profile)
	return opshealth.NewReport("mac_local_executor", time.Now(), checks...)
}
