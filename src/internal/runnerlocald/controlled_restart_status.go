package runnerlocald

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/store"
)

// controlledRestartStatusSettings is the minimal active-config selection the
// status command needs. It intentionally excludes endpoints, routes, and all
// secret references.
type controlledRestartStatusSettings struct {
	Kind     config.HostKind
	Account  string
	Database string
}

// runControlledRestartStatus reports only a stable durable-state token. It
// performs no migration or authority mutation, and it deliberately keeps all
// diagnostic detail out of command output so no identities or payload data
// can escape an installer branch.
func runControlledRestartStatus(args []string, stdout, stderr io.Writer) int {
	return runControlledRestartStatusWithSettings(args, stdout, stderr, loadControlledRestartStatusSettings)
}

func runControlledRestartStatusWithSettings(args []string, stdout, stderr io.Writer, loadSettings func(string) (controlledRestartStatusSettings, error)) int {
	return runControlledRestartStatusWithSettingsAndReader(args, stdout, stderr, loadSettings, readControlledRestartStatus)
}

func runControlledRestartStatusWithSettingsAndReader(args []string, stdout, stderr io.Writer, loadSettings func(string) (controlledRestartStatusSettings, error), readStatus func(context.Context, string) (store.ControlledRestartStatus, error)) int {
	flags := flag.NewFlagSet("runner-locald controlled-restart-status", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "owner-only Mac runner configuration")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *configPath == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "runner-locald controlled-restart-status: --config is required")
		return 2
	}
	if loadSettings == nil || readStatus == nil {
		fmt.Fprintln(stderr, "runner-locald controlled-restart-status: status is unavailable")
		return 1
	}
	settings, err := loadSettings(*configPath)
	if err != nil || settings.Kind != config.HostKindMac || settings.Account != config.MacAccount || settings.Database == "" {
		fmt.Fprintln(stderr, "runner-locald controlled-restart-status: status is unavailable")
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	status, err := readStatus(ctx, settings.Database)
	if err != nil || !isControlledRestartStatus(status) {
		fmt.Fprintln(stderr, "runner-locald controlled-restart-status: status is unavailable")
		return 1
	}
	fmt.Fprintln(stdout, status)
	return 0
}

func isControlledRestartStatus(status store.ControlledRestartStatus) bool {
	switch status {
	case store.ControlledRestartStatusLegacy,
		store.ControlledRestartStatusPrepared,
		store.ControlledRestartStatusActive,
		store.ControlledRestartStatusMigratedWithoutPlan:
		return true
	default:
		return false
	}
}

func loadControlledRestartStatusSettings(path string) (controlledRestartStatusSettings, error) {
	loaded, err := config.LoadFile(path)
	if err != nil {
		return controlledRestartStatusSettings{}, err
	}
	settings, ok := loaded.MacSettings()
	if !ok {
		return controlledRestartStatusSettings{}, errors.New("Mac settings are unavailable")
	}
	return controlledRestartStatusSettings{Kind: loaded.Kind(), Account: settings.Account, Database: settings.Database}, nil
}

func readControlledRestartStatus(ctx context.Context, databasePath string) (store.ControlledRestartStatus, error) {
	database, err := store.OpenExistingControlledRestartStatusReadOnly(ctx, databasePath)
	if err != nil {
		return "", err
	}
	defer database.Close()
	return store.ReadControlledRestartStatus(ctx, database)
}
