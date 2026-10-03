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

var errRestartPreflightNotQuiescent = errors.New("local execution is not quiescent")

const restartPreflightAuthorityMissingExit = 3

// restartPreflightSettings is the minimal active-config selection needed by
// the restart guard. It intentionally omits all secret references and route
// configuration.
type restartPreflightSettings struct {
	Kind     config.HostKind
	Account  string
	Database string
}

func runRestartPreflight(args []string, stdout, stderr io.Writer) int {
	return runRestartPreflightWithSettings(args, stdout, stderr, loadRestartPreflightSettings)
}

func runRestartPreflightWithSettings(args []string, stdout, stderr io.Writer, loadSettings func(string) (restartPreflightSettings, error)) int {
	flags := flag.NewFlagSet("runner-locald preflight-restart", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "owner-only Mac runner configuration")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *configPath == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "runner-locald preflight-restart: --config is required")
		return 2
	}
	if loadSettings == nil {
		fmt.Fprintln(stderr, "runner-locald preflight-restart: could not prove local execution is quiescent")
		return 1
	}
	settings, err := loadSettings(*configPath)
	if err != nil {
		fmt.Fprintln(stderr, "runner-locald preflight-restart: could not prove local execution is quiescent")
		return 1
	}
	if settings.Kind != config.HostKindMac || settings.Account != config.MacAccount || settings.Database == "" {
		fmt.Fprintln(stderr, "runner-locald preflight-restart: could not prove local execution is quiescent")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	metrics, err := preflightMacLocalExecution(ctx, settings.Database)
	if errors.Is(err, errRestartPreflightNotQuiescent) {
		fmt.Fprintf(stderr, "runner-locald preflight-restart: local execution is not quiescent (active_session_slots=%d active_command_slots=%d queued_commands=%d resumable_jobs=%d)\n", metrics.ActiveSessionSlots, metrics.ActiveCommandSlots, metrics.QueuedCommands, metrics.ResumableJobs)
		return 1
	}
	if errors.Is(err, store.ErrDatabaseMissing) {
		fmt.Fprintln(stderr, "runner-locald preflight-restart: active local authority database is unavailable")
		return restartPreflightAuthorityMissingExit
	}
	if err != nil {
		fmt.Fprintln(stderr, "runner-locald preflight-restart: could not prove local execution is quiescent")
		return 1
	}
	fmt.Fprintln(stdout, "runner-locald preflight-restart: local execution is quiescent")
	return 0
}

func loadRestartPreflightSettings(path string) (restartPreflightSettings, error) {
	loaded, err := config.LoadFile(path)
	if err != nil {
		return restartPreflightSettings{}, err
	}
	settings, ok := loaded.MacSettings()
	if !ok {
		return restartPreflightSettings{}, errors.New("Mac settings are unavailable")
	}
	return restartPreflightSettings{Kind: loaded.Kind(), Account: settings.Account, Database: settings.Database}, nil
}

func preflightMacLocalExecution(ctx context.Context, databasePath string) (store.RestartPreflightMetrics, error) {
	database, err := store.OpenExistingRestartPreflightReadOnly(ctx, databasePath)
	if err != nil {
		return store.RestartPreflightMetrics{}, err
	}
	defer database.Close()
	authority, err := store.NewAuthorityStore(database)
	if err != nil {
		return store.RestartPreflightMetrics{}, err
	}
	metrics, err := authority.ReadRestartPreflightMetrics(ctx)
	if err != nil {
		return store.RestartPreflightMetrics{}, err
	}
	if metrics.ActiveSessionSlots != 0 || metrics.ActiveCommandSlots != 0 || metrics.QueuedCommands != 0 || metrics.ResumableJobs != 0 {
		return metrics, errRestartPreflightNotQuiescent
	}
	return metrics, nil
}
