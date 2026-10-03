package runnerlocald

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"syscall"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/unixsocket"
)

// controlledRestartSocketBoundarySettings contains only the paths and account
// needed to prove that a frozen, unloaded old locald cannot still own its
// private socket. It deliberately has no database field: this boundary check
// must never open, migrate, or otherwise touch the authority database.
type controlledRestartSocketBoundarySettings struct {
	Kind         config.HostKind
	Account      string
	ServiceRoot  string
	LocalDSocket string
}

type controlledRestartSocketBoundaryClearer func(controlledRestartSocketBoundarySettings) error

// runControlledRestartSocketBoundary removes a stale old locald socket only
// after validating the selected owner-only Mac configuration and the current
// account. A live listener, foreign/non-socket path, path replacement, or an
// unsafe service root fails closed. It prints no paths or identifiers on an
// error so the installer can use it as a narrow process-boundary gate.
func runControlledRestartSocketBoundary(args []string, stdout, stderr io.Writer) int {
	return runControlledRestartSocketBoundaryWith(
		args,
		stdout,
		stderr,
		loadControlledRestartSocketBoundarySettings,
		currentControlledRestartSocketBoundaryAccount,
		clearControlledRestartSocketBoundary,
	)
}

func runControlledRestartSocketBoundaryWith(
	args []string,
	stdout, stderr io.Writer,
	loadSettings func(string) (controlledRestartSocketBoundarySettings, error),
	currentAccount func() (string, error),
	clearBoundary controlledRestartSocketBoundaryClearer,
) int {
	flags := flag.NewFlagSet("runner-locald controlled-restart-socket-boundary", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "owner-only Mac runner configuration")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *configPath == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "runner-locald controlled-restart-socket-boundary: --config is required")
		return 2
	}
	if loadSettings == nil || currentAccount == nil || clearBoundary == nil {
		fmt.Fprintln(stderr, "runner-locald controlled-restart-socket-boundary: could not prove old locald socket boundary")
		return 1
	}
	settings, err := loadSettings(*configPath)
	if err != nil || !validControlledRestartSocketBoundarySettings(settings, config.MacServiceRoot) {
		fmt.Fprintln(stderr, "runner-locald controlled-restart-socket-boundary: could not prove old locald socket boundary")
		return 1
	}
	account, err := currentAccount()
	if err != nil || account != settings.Account {
		fmt.Fprintln(stderr, "runner-locald controlled-restart-socket-boundary: could not prove old locald socket boundary")
		return 1
	}
	if err := clearBoundary(settings); err != nil {
		fmt.Fprintln(stderr, "runner-locald controlled-restart-socket-boundary: could not prove old locald socket boundary")
		return 1
	}
	fmt.Fprintln(stdout, "runner-locald controlled-restart-socket-boundary: old locald socket is absent")
	return 0
}

func loadControlledRestartSocketBoundarySettings(path string) (controlledRestartSocketBoundarySettings, error) {
	loaded, err := config.LoadFile(path)
	if err != nil {
		return controlledRestartSocketBoundarySettings{}, err
	}
	settings, ok := loaded.MacSettings()
	if !ok {
		return controlledRestartSocketBoundarySettings{}, errors.New("Mac settings are unavailable")
	}
	return controlledRestartSocketBoundarySettings{
		Kind:         loaded.Kind(),
		Account:      settings.Account,
		ServiceRoot:  settings.ServiceRoot,
		LocalDSocket: settings.LocalDSocket,
	}, nil
}

func currentControlledRestartSocketBoundaryAccount() (string, error) {
	current, err := user.Current()
	if err != nil {
		return "", err
	}
	return current.Username, nil
}

func clearControlledRestartSocketBoundary(settings controlledRestartSocketBoundarySettings) error {
	return clearControlledRestartSocketBoundaryAtRoot(settings, config.MacServiceRoot, os.Geteuid(), unixsocket.RemoveStaleOwned)
}

func clearControlledRestartSocketBoundaryAtRoot(
	settings controlledRestartSocketBoundarySettings,
	expectedServiceRoot string,
	expectedUID int,
	removeStale func(string) error,
) error {
	if !validControlledRestartSocketBoundarySettings(settings, expectedServiceRoot) || removeStale == nil {
		return errors.New("controlled restart socket settings are invalid")
	}
	socketPath := filepath.Join(expectedServiceRoot, "run", "locald.sock")
	if err := verifyControlledRestartSocketRoot(expectedServiceRoot, expectedUID); err != nil {
		return err
	}
	if err := removeStale(socketPath); err != nil {
		return err
	}
	// Revalidate the owner-only root after RemoveStaleOwned's own same-file
	// check. This catches a directory substitution before a candidate can be
	// enabled, while the helper itself has opened no authority database.
	if err := verifyControlledRestartSocketRoot(expectedServiceRoot, expectedUID); err != nil {
		return err
	}
	if info, err := os.Lstat(socketPath); err == nil {
		return fmt.Errorf("old locald socket remains as %s", info.Mode().Type())
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func validControlledRestartSocketBoundarySettings(settings controlledRestartSocketBoundarySettings, expectedServiceRoot string) bool {
	if settings.Kind != config.HostKindMac || settings.Account != config.MacAccount || expectedServiceRoot == "" || settings.ServiceRoot != expectedServiceRoot {
		return false
	}
	return filepath.Clean(settings.LocalDSocket) == filepath.Join(expectedServiceRoot, "run", "locald.sock")
}

func verifyControlledRestartSocketRoot(serviceRoot string, expectedUID int) error {
	if serviceRoot == "" || !filepath.IsAbs(serviceRoot) {
		return errors.New("service root is invalid")
	}
	for _, path := range []string{serviceRoot, filepath.Join(serviceRoot, "run")} {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
			return errors.New("service root is not an owner-only real directory")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(stat.Uid) != expectedUID {
			return errors.New("service root owner is invalid")
		}
	}
	return nil
}
