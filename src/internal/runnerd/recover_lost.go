package runnerd

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	hostruntime "remote-session-runner/src/internal/runtime"
	"remote-session-runner/src/internal/store"
)

// runRecoverLost performs a narrowly scoped operator repair for one terminal
// lost runtime. runnerd.service must be stopped so no in-memory executor can
// race this one-shot operation. The existing runtime reconciler establishes
// ownership and cleanup; this command never executes or replays stored script bytes.
func runRecoverLost(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("runnerd recover-lost", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "owner-only Linux runner configuration")
	sessionValue := flags.String("session-id", "", "lost session ID to recover")
	commandValue := flags.String("command-id", "", "lost command ID to recover")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *configPath == "" || *sessionValue == "" || *commandValue == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "runnerd recover-lost: --config, --session-id, and --command-id are required")
		return 2
	}
	sessionID, err := domain.NewSessionID(*sessionValue)
	if err != nil {
		fmt.Fprintf(stderr, "runnerd recover-lost: invalid session ID: %v\n", err)
		return 2
	}
	commandID, err := domain.NewCommandID(*commandValue)
	if err != nil {
		fmt.Fprintf(stderr, "runnerd recover-lost: invalid command ID: %v\n", err)
		return 2
	}
	loaded, err := config.LoadFile(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "runnerd recover-lost: load config: %v\n", err)
		return 1
	}
	settings, ok := loaded.LinuxSettings()
	if !ok || loaded.Kind() != config.HostKindLinux {
		fmt.Fprintln(stderr, "runnerd recover-lost: Linux host configuration is required")
		return 1
	}
	releaseLifecycleLock, err := acquireRunnerdLifecycleLock(settings.ServiceRoot)
	if err != nil {
		fmt.Fprintf(stderr, "runnerd recover-lost: %v\n", err)
		return 1
	}
	defer releaseLifecycleLock()
	if err := requireRunnerdServiceStopped(); err != nil {
		fmt.Fprintf(stderr, "runnerd recover-lost: %v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	profile, err := hostruntime.NewLinuxProcessProfile(hostruntime.LinuxRuntimeOptions{
		Account: settings.Account, ServiceRoot: settings.ServiceRoot, WorkspaceRoot: settings.Workspaces, ShellPath: "/usr/bin/bash",
	})
	if err != nil {
		fmt.Fprintf(stderr, "runnerd recover-lost: host process profile: %v\n", err)
		return 1
	}
	profileReport, err := profile.Doctor(ctx)
	if err != nil || !profileReport.Ready {
		fmt.Fprintln(stderr, "runnerd recover-lost: host process profile is not ready")
		return 1
	}
	database, err := store.Open(ctx, settings.Database)
	if err != nil {
		fmt.Fprintf(stderr, "runnerd recover-lost: open authority database: %v\n", err)
		return 1
	}
	defer database.Close()
	authority, err := store.NewAuthorityStore(database)
	if err != nil {
		fmt.Fprintf(stderr, "runnerd recover-lost: construct authority store: %v\n", err)
		return 1
	}
	environments := make([]domain.Environment, 0, len(loaded.EnvironmentNames()))
	for _, name := range loaded.EnvironmentNames() {
		registered, present := loaded.Environment(name)
		if !present {
			fmt.Fprintf(stderr, "runnerd recover-lost: configured environment %q disappeared\n", name)
			return 1
		}
		environments = append(environments, registered.Policy())
	}
	service, _, err := NewLinuxExecutionService(authority, hostruntime.LinuxRuntimeOptions{
		Account: settings.Account, WorkspaceRoot: settings.Workspaces, ShellPath: "/usr/bin/bash",
	}, environments...)
	if err != nil {
		fmt.Fprintf(stderr, "runnerd recover-lost: construct execution service: %v\n", err)
		return 1
	}
	result, err := service.RecoverLostRuntime(ctx, execution.LostRuntimeRecoveryRequest{SessionID: sessionID, CommandID: commandID})
	if err != nil {
		fmt.Fprintf(stderr, "runnerd recover-lost: %v\n", err)
		return 1
	}
	if result.AlreadyRecovered {
		fmt.Fprintf(stdout, "runnerd recover-lost: already_recovered session_id=%s command_id=%s\n", result.Session.SessionID, result.Command.CommandID)
		return 0
	}
	if result.CommandSlot.StopConfirmedAt == nil || result.CommandSlot.ReleasedAt == nil || result.SessionReservation.CleanupConfirmedAt == nil || result.SessionReservation.ReleasedAt == nil {
		fmt.Fprintln(stderr, "runnerd recover-lost: confirmed recovery did not persist both capacity releases")
		return 1
	}
	fmt.Fprintf(stdout, "runnerd recover-lost: recovered session_id=%s command_id=%s runtime_generation=%s command_slot_released=true session_reservation_released=true\n", result.Session.SessionID, result.Command.CommandID, result.Runtime.RuntimeGeneration)
	return 0
}

func requireRunnerdServiceStopped() error {
	return requireRunnerdServiceStoppedWith(runnerdSystemdValue, os.ReadFile)
}

func requireRunnerdServiceStoppedWith(systemdValue func(string) (string, error), readFile func(string) ([]byte, error)) error {
	activeState, err := systemdValue("ActiveState")
	if err != nil {
		return fmt.Errorf("read runnerd.service active state: %w", err)
	}
	if activeState != "inactive" && activeState != "failed" {
		return fmt.Errorf("runnerd.service state=%q; stop it before lost-runtime recovery", activeState)
	}
	mainPID, err := systemdValue("MainPID")
	if err != nil {
		return fmt.Errorf("read runnerd.service main PID: %w", err)
	}
	if mainPID != "0" {
		return fmt.Errorf("runnerd.service main PID=%q; stop it before lost-runtime recovery", mainPID)
	}
	controlGroup, err := systemdValue("ControlGroup")
	if err != nil {
		return fmt.Errorf("read runnerd.service control group: %w", err)
	}
	// systemd removes a unit's cgroup after a clean stop on this host. With an
	// inactive unit and MainPID=0, an absent ControlGroup is therefore the
	// confirmed empty-cgroup state, not a reason to reject recovery. Keep a
	// failed unit conservative: it must still present an inspectable cgroup.
	if controlGroup == "" {
		if activeState == "inactive" {
			return nil
		}
		return fmt.Errorf("runnerd.service control group is unavailable after %s state", activeState)
	}
	if !filepath.IsAbs(controlGroup) || strings.Contains(controlGroup, "..") {
		return fmt.Errorf("runnerd.service control group=%q is invalid", controlGroup)
	}
	members, err := readFile("/sys/fs/cgroup" + controlGroup + "/cgroup.procs")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read runnerd.service control group: %w", err)
	}
	if len(strings.Fields(string(members))) != 0 {
		return fmt.Errorf("runnerd.service control group still has processes; stop it before lost-runtime recovery")
	}
	return nil
}

func runnerdSystemdValue(property string) (string, error) {
	output, err := exec.Command("systemctl", "show", "--property="+property, "--value", "runnerd.service").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}
