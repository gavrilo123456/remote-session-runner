package runnerlocald

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/store"
)

func TestControlledRestartStatusCommandReadsCurrentAuthorityReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "authority.db")
	database, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := runControlledRestartStatusWithSettings(
		[]string{"--config", "/private/tmp/controlled-restart-status.yaml"},
		&stdout,
		&stderr,
		func(string) (controlledRestartStatusSettings, error) {
			return controlledRestartStatusSettings{Kind: config.HostKindMac, Account: config.MacAccount, Database: path}, nil
		},
	)
	if code != 0 || stdout.String() != string(store.ControlledRestartStatusMigratedWithoutPlan)+"\n" || stderr.Len() != 0 {
		t.Fatalf("status command exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestControlledRestartStatusCommandPrintsOnlyStableState(t *testing.T) {
	for _, want := range []store.ControlledRestartStatus{
		store.ControlledRestartStatusLegacy,
		store.ControlledRestartStatusPrepared,
		store.ControlledRestartStatusActive,
		store.ControlledRestartStatusMigratedWithoutPlan,
	} {
		t.Run(string(want), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runControlledRestartStatusWithSettingsAndReader(
				[]string{"--config", "/private/tmp/controlled-restart-status.yaml"},
				&stdout,
				&stderr,
				func(path string) (controlledRestartStatusSettings, error) {
					if path != "/private/tmp/controlled-restart-status.yaml" {
						t.Fatalf("selected config=%q", path)
					}
					return controlledRestartStatusSettings{Kind: config.HostKindMac, Account: config.MacAccount, Database: "/private/tmp/controlled-restart-status.db"}, nil
				},
				func(context.Context, string) (store.ControlledRestartStatus, error) { return want, nil },
			)
			if code != 0 || stdout.String() != string(want)+"\n" || stderr.Len() != 0 {
				t.Fatalf("status command exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestControlledRestartStatusCommandFailsClosedWithoutOutput(t *testing.T) {
	for _, fixture := range []struct {
		name         string
		args         []string
		loadSettings func(string) (controlledRestartStatusSettings, error)
		readStatus   func(context.Context, string) (store.ControlledRestartStatus, error)
		wantCode     int
		wantError    string
	}{
		{
			name:      "missing config",
			args:      nil,
			wantCode:  2,
			wantError: "runner-locald controlled-restart-status: --config is required\n",
		},
		{
			name: "invalid host settings",
			args: []string{"--config", "/private/tmp/controlled-restart-status.yaml"},
			loadSettings: func(string) (controlledRestartStatusSettings, error) {
				return controlledRestartStatusSettings{Kind: config.HostKindLinux, Account: config.LinuxAccount, Database: "/private/tmp/controlled-restart-status.db"}, nil
			},
			wantCode:  1,
			wantError: "runner-locald controlled-restart-status: status is unavailable\n",
		},
		{
			name: "malformed authority",
			args: []string{"--config", "/private/tmp/controlled-restart-status.yaml"},
			loadSettings: func(string) (controlledRestartStatusSettings, error) {
				return controlledRestartStatusSettings{Kind: config.HostKindMac, Account: config.MacAccount, Database: "/private/tmp/controlled-restart-status.db"}, nil
			},
			readStatus: func(context.Context, string) (store.ControlledRestartStatus, error) {
				return "", errors.New("fixture includes a command identifier that must never be printed")
			},
			wantCode:  1,
			wantError: "runner-locald controlled-restart-status: status is unavailable\n",
		},
		{
			name: "unrecognized status",
			args: []string{"--config", "/private/tmp/controlled-restart-status.yaml"},
			loadSettings: func(string) (controlledRestartStatusSettings, error) {
				return controlledRestartStatusSettings{Kind: config.HostKindMac, Account: config.MacAccount, Database: "/private/tmp/controlled-restart-status.db"}, nil
			},
			readStatus: func(context.Context, string) (store.ControlledRestartStatus, error) {
				return "unexpected-status", nil
			},
			wantCode:  1,
			wantError: "runner-locald controlled-restart-status: status is unavailable\n",
		},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runControlledRestartStatusWithSettingsAndReader(fixture.args, &stdout, &stderr, fixture.loadSettings, fixture.readStatus)
			if code != fixture.wantCode || stdout.Len() != 0 || stderr.String() != fixture.wantError {
				t.Fatalf("status command exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestRunDispatchesControlledRestartStatus(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"controlled-restart-status", "--config", filepath.Join(t.TempDir(), "missing.yaml")}, &stdout, &stderr)
	if code != 1 || stdout.Len() != 0 || stderr.String() != "runner-locald controlled-restart-status: status is unavailable\n" {
		t.Fatalf("Run(controlled-restart-status) exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}
