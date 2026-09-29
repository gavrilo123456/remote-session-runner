//go:build p132hostcleanup

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
)

const (
	p132CleanupDatabasePath = "/home/ubuntu/.local/share/remote-session-runner/state/remote.db"
	p132BackgroundFixture   = `/bin/sleep 120 & child=$!; printf 'P132_CHILD_PID=%s\nP132_RUNNING\n' "$child"; wait "$child"`
	p132ForegroundFixture   = `/bin/sh -c 'printf "P132_CHILD_PID=%s\nP132_RUNNING\n" "$$"; exec /bin/sleep 120'`
)

// TestP132UbuntuCleanupConfirmedLostFixture releases only the P132 test's
// retained reservations after both its recorded process and the old
// runnerd.service cgroup have been confirmed empty.
func TestP132UbuntuCleanupConfirmedLostFixture(t *testing.T) {
	if os.Getenv("RSR_P132_HOST_CLEANUP") != "1" {
		t.Skip("set RSR_P132_HOST_CLEANUP=1 to clean a confirmed-dead P132 fixture")
	}
	if runtime.GOOS != "linux" {
		t.Fatalf("P132 fixture cleanup must run on Linux, got %s", runtime.GOOS)
	}
	current, err := user.Current()
	if err != nil || current.Username != "ubuntu" || current.Uid != "1001" {
		t.Fatalf("P132 fixture cleanup account=%v err=%v, want ubuntu uid 1001", current, err)
	}
	sessionID, err := domain.NewSessionID(os.Getenv("RSR_P132_HOST_SESSION_ID"))
	if err != nil {
		t.Fatalf("invalid P132 cleanup session ID: %v", err)
	}
	commandID, err := domain.NewCommandID(os.Getenv("RSR_P132_HOST_COMMAND_ID"))
	if err != nil {
		t.Fatalf("invalid P132 cleanup command ID: %v", err)
	}
	if err := p132CleanupRequireNoRunnerdDescendants(); err != nil {
		t.Fatalf("runnerd.service process boundary is not clear: %v", err)
	}
	db := p132OpenUbuntuDatabaseForFixtureCleanup(t)
	authority, err := NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	session, err := authority.GetSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("read P132 cleanup session: %v", err)
	}
	command, err := authority.GetCommand(ctx, commandID)
	if err != nil {
		t.Fatalf("read P132 cleanup command: %v", err)
	}
	if command.SessionID != sessionID || string(command.ScriptBytes) != p132BackgroundFixture && string(command.ScriptBytes) != p132ForegroundFixture {
		t.Fatal("refuse cleanup: command is not one of the exact P132 host fixtures")
	}
	if session.Environment != "linux-dev" || session.Target.Kind() != domain.TargetKindRemote || session.Target.Profile() != "linux-host" ||
		session.Controller.Type() != domain.ControllerTypeDirectMTLS || session.Controller.ID() != "tomasz.walczuk" {
		t.Fatalf("refuse cleanup: session identity is target=%s/%s environment=%q controller=%s/%s, want remote/linux-host linux-dev direct_mtls/tomasz.walczuk",
			session.Target.Kind(), session.Target.Profile(), session.Environment, session.Controller.Type(), session.Controller.ID())
	}
	events, err := authority.ListCommandEvents(ctx, commandID)
	if err != nil {
		t.Fatalf("read P132 cleanup event tail: %v", err)
	}
	childPID := p132CleanupChildPID(t, events)
	if err := syscall.Kill(childPID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("refuse cleanup: P132 fixture process PID %d is not confirmed absent: %v", childPID, err)
	}

	sessionReservation, err := authority.GetSessionReservation(ctx, sessionID)
	if err != nil {
		t.Fatalf("read P132 session reservation: %v", err)
	}
	commandSlot, err := authority.GetCommandSlot(ctx, commandID)
	if err != nil {
		t.Fatalf("read P132 command slot: %v", err)
	}
	if session.State == domain.SessionStateClosed && command.State == domain.CommandStateCancelled {
		if command.OutputComplete != true || sessionReservation.CleanupConfirmedAt == nil || sessionReservation.ReleasedAt == nil || commandSlot.StopConfirmedAt == nil || commandSlot.ReleasedAt == nil {
			t.Fatal("refuse cleanup: completed fixture has inconsistent release records")
		}
		t.Log("P132 fixture was already closed with confirmed, released capacity")
		return
	}
	if session.State != domain.SessionStateLost || command.State != domain.CommandStateLost || command.OutputComplete || command.FinalEventSequence == nil ||
		len(events) == 0 || events[len(events)-1].Sequence != *command.FinalEventSequence || events[len(events)-1].Type != "command_lost" {
		t.Fatal("refuse cleanup: fixture is not the expected truthful lost outcome")
	}
	if sessionReservation.CleanupConfirmedAt != nil || sessionReservation.ReleasedAt != nil || commandSlot.StopConfirmedAt != nil || commandSlot.ReleasedAt != nil {
		t.Fatal("refuse cleanup: lost fixture already has a capacity release record")
	}
	sessionReservations, err := authority.CountLiveSessionReservations(ctx)
	if err != nil || sessionReservations != 1 {
		t.Fatalf("refuse cleanup: live session reservations=%d err=%v, want only the P132 fixture", sessionReservations, err)
	}
	commandSlots, err := authority.CountLiveCommandSlots(ctx)
	if err != nil || commandSlots != 1 {
		t.Fatalf("refuse cleanup: live command slots=%d err=%v, want only the P132 fixture", commandSlots, err)
	}
	if err := authority.ConfirmCommandSlotRelease(ctx, commandID); err != nil {
		t.Fatalf("confirm the P132 command process stop: %v", err)
	}
	if err := authority.ConfirmSessionCleanup(ctx, sessionID); err != nil {
		t.Fatalf("confirm the P132 session cleanup: %v", err)
	}
	if sessionReservations, err = authority.CountLiveSessionReservations(ctx); err != nil || sessionReservations != 0 {
		t.Fatalf("live session reservations after P132 fixture cleanup=%d err=%v, want 0", sessionReservations, err)
	}
	if commandSlots, err = authority.CountLiveCommandSlots(ctx); err != nil || commandSlots != 0 {
		t.Fatalf("live command slots after P132 fixture cleanup=%d err=%v, want 0", commandSlots, err)
	}
	fmt.Println("P132_HOST_FIXTURE_CLEANUP=confirmed process_absent=true cgroup_clear=true session_reservations=0 command_slots=0")
}

func p132CleanupChildPID(t *testing.T, events []CommandEventRecord) int {
	t.Helper()
	var output strings.Builder
	for _, event := range events {
		if event.Type == "stdout" {
			output.Write(event.Payload)
		}
	}
	text := output.String()
	if !strings.Contains(text, "P132_RUNNING") {
		t.Fatal("refuse cleanup: P132 child marker is absent from durable stdout")
	}
	marker := "P132_CHILD_PID="
	start := strings.Index(text, marker)
	if start < 0 {
		t.Fatal("refuse cleanup: P132 child PID marker is absent from durable stdout")
	}
	start += len(marker)
	end := strings.IndexByte(text[start:], '\n')
	if end < 0 {
		t.Fatal("refuse cleanup: P132 child PID marker is incomplete")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(text[start : start+end]))
	if err != nil || pid < 2 {
		t.Fatalf("refuse cleanup: invalid P132 child PID: %v", err)
	}
	return pid
}

func p132CleanupRequireNoRunnerdDescendants() error {
	activeOutput, err := exec.Command("systemctl", "show", "-p", "ActiveState", "--value", "runnerd.service").Output()
	if err != nil {
		return fmt.Errorf("read runnerd.service state: %w", err)
	}
	activeState := strings.TrimSpace(string(activeOutput))
	if activeState != "active" && activeState != "inactive" {
		return fmt.Errorf("runnerd.service state=%q, want active or inactive", activeState)
	}
	groupOutput, err := exec.Command("systemctl", "show", "-p", "ControlGroup", "--value", "runnerd.service").Output()
	if err != nil {
		return fmt.Errorf("read runnerd.service cgroup: %w", err)
	}
	controlGroup := strings.TrimSpace(string(groupOutput))
	if controlGroup == "" || !filepath.IsAbs(controlGroup) || strings.Contains(controlGroup, "..") {
		return fmt.Errorf("invalid runnerd.service cgroup path %q", controlGroup)
	}
	mainPID := 0
	if activeState == "active" {
		mainOutput, err := exec.Command("systemctl", "show", "-p", "MainPID", "--value", "runnerd.service").Output()
		if err != nil {
			return fmt.Errorf("read runnerd.service MainPID: %w", err)
		}
		mainPID, err = strconv.Atoi(strings.TrimSpace(string(mainOutput)))
		if err != nil || mainPID < 2 {
			return fmt.Errorf("invalid active runnerd.service MainPID %q", strings.TrimSpace(string(mainOutput)))
		}
	}
	cgroupProcs := filepath.Join("/sys/fs/cgroup/systemd", strings.TrimPrefix(controlGroup, string(filepath.Separator)), "cgroup.procs")
	contents, err := os.ReadFile(cgroupProcs)
	if errors.Is(err, os.ErrNotExist) && activeState == "inactive" {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read runnerd.service process group: %w", err)
	}
	var pids []int
	for _, item := range strings.Fields(string(contents)) {
		pid, err := strconv.Atoi(item)
		if err != nil || pid < 2 {
			return fmt.Errorf("invalid PID in runnerd.service process group")
		}
		pids = append(pids, pid)
	}
	if activeState == "active" && (len(pids) != 1 || pids[0] != mainPID) {
		return fmt.Errorf("runnerd.service cgroup contains %d processes beyond its MainPID", len(pids)-1)
	}
	if activeState == "inactive" && len(pids) != 0 {
		return fmt.Errorf("stopped runnerd.service cgroup still contains %d processes", len(pids))
	}
	return nil
}

func p132OpenUbuntuDatabaseForFixtureCleanup(t *testing.T) *sql.DB {
	t.Helper()
	databaseURI := url.URL{Scheme: "file", Path: p132CleanupDatabasePath}
	query := databaseURI.Query()
	query.Set("_busy_timeout", strconv.FormatInt(BusyTimeout.Milliseconds(), 10))
	databaseURI.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", databaseURI.String())
	if err != nil {
		t.Fatalf("open Ubuntu authority database for fixture cleanup: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("connect to Ubuntu authority database for fixture cleanup: %v", err)
	}
	return db
}
