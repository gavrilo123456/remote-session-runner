package laneinstall

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/store"
)

var ErrSimpleServiceTarget = errors.New("selected lane service does not match this host or account")

type SimpleServiceAction string

const (
	SimpleServiceStatus SimpleServiceAction = "status"
	SimpleServiceStart  SimpleServiceAction = "start"
	SimpleServiceStop   SimpleServiceAction = "stop"
)

// SimpleServiceController operates on exactly the service identity derived by
// BuildSimplePlan. It has no API for a caller-supplied unit, label, or command.
type SimpleServiceController struct {
	runner         simpleCommandRunner
	ready          func(Plan) error
	stoppedProof   func(Plan) error
	initialInstall func(context.Context, Plan) error
	resolvePath    func(string) (string, error)
	uid            string
	user           string
	goos           string
	goarch         string
}

type simpleCommandResult struct {
	ExitCode int
	Output   string
}

type simpleCommandRunner interface {
	Run(context.Context, string, ...string) (simpleCommandResult, error)
}

type execSimpleCommandRunner struct{}

func (execSimpleCommandRunner) Run(ctx context.Context, name string, args ...string) (simpleCommandResult, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdin = os.Stdin
	output, err := command.CombinedOutput()
	result := simpleCommandResult{ExitCode: 0, Output: string(output)}
	if err == nil {
		return result, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		result.ExitCode = exitError.ExitCode()
		return result, nil
	}
	return result, err
}

func NewSimpleServiceController() (*SimpleServiceController, error) {
	current, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("resolve service operator identity: %w", err)
	}
	return &SimpleServiceController{runner: execSimpleCommandRunner{}, uid: current.Uid, user: current.Username, goos: runtime.GOOS, goarch: runtime.GOARCH}, nil
}

func (c *SimpleServiceController) validate(plan Plan) error {
	if c == nil || c.runner == nil || validateSimplePlan(plan) != nil {
		return ErrSimpleServiceTarget
	}
	want := simpleHostPlatforms[plan.HostID]
	if c.goos != want.goos || c.goarch != want.goarch || c.user != plan.Namespace.Account {
		return ErrSimpleServiceTarget
	}
	return nil
}

func (c *SimpleServiceController) Status(ctx context.Context, plan Plan) (bool, error) {
	ctx, cancel := simpleBoundedContext(ctx, 5*time.Second)
	defer cancel()
	if err := c.validate(plan); err != nil {
		return false, err
	}
	if plan.Namespace.Host == config.HostKindMac {
		local, err := c.macAgentStatus(ctx, plan.Namespace.LocalAgentLabel)
		if err != nil {
			return false, err
		}
		locald, err := c.macAgentStatus(ctx, plan.Namespace.LocalDAgentLabel)
		return local || locald, err
	}
	result, err := c.runner.Run(ctx, "/usr/bin/systemctl", "is-active", plan.Namespace.RunnerUnit)
	if err != nil {
		return false, err
	}
	if result.ExitCode == 0 && strings.TrimSpace(result.Output) == "active" {
		return true, nil
	}
	if result.ExitCode == 3 || (result.ExitCode == 0 && strings.TrimSpace(result.Output) != "active") {
		return false, nil
	}
	if result.ExitCode == 4 {
		unitPath := filepath.Join("/etc/systemd/system", plan.Namespace.RunnerUnit)
		if _, statErr := os.Lstat(unitPath); errors.Is(statErr, os.ErrNotExist) {
			return false, nil
		} else if statErr != nil {
			return false, fmt.Errorf("inspect selected lane systemd unit: %w", statErr)
		}
	}
	return false, fmt.Errorf("systemd status for selected service failed (exit %d): %s", result.ExitCode, strings.TrimSpace(result.Output))
}

func (c *SimpleServiceController) Start(ctx context.Context, plan Plan) error {
	ctx, cancel := simpleBoundedContext(ctx, 45*time.Second)
	defer cancel()
	if err := c.validate(plan); err != nil {
		return err
	}
	if err := c.requireReady(ctx, plan); err != nil {
		return err
	}
	if plan.Namespace.Host == config.HostKindMac {
		started := make([]string, 0, 2)
		for _, agent := range []struct{ label, plist string }{
			{plan.Namespace.LocalAgentLabel, plan.Namespace.LocalAgentPlist},
			{plan.Namespace.LocalDAgentLabel, plan.Namespace.LocalDAgentPlist},
		} {
			active, statusErr := c.macAgentStatus(ctx, agent.label)
			if statusErr != nil {
				return statusErr
			}
			if active {
				continue
			}
			domain := "gui/" + c.uid
			if err := c.launchctl(ctx, "bootstrap", domain, agent.plist); err != nil {
				c.stopMacAgents(ctx, plan, started)
				return err
			}
			if err := c.launchctl(ctx, "kickstart", "-k", domain+"/"+agent.label); err != nil {
				c.stopMacAgents(ctx, plan, append(started, agent.label))
				return err
			}
			started = append(started, agent.label)
		}
		for _, label := range []string{plan.Namespace.LocalAgentLabel, plan.Namespace.LocalDAgentLabel} {
			active, err := c.macAgentStatus(ctx, label)
			if err != nil {
				return err
			}
			if !active {
				return fmt.Errorf("Mac selected lane agent %s did not become active", label)
			}
		}
		return c.completeInitialInstall(ctx, plan)
	}
	active, err := c.Status(ctx, plan)
	if err != nil {
		return err
	}
	if active {
		// A previous start may have brought the service up before its fresh
		// database finished initializing. Retry its completion proof instead
		// of silently leaving the first-install marker behind.
		return c.completeInitialInstall(ctx, plan)
	}
	result, err := c.runner.Run(ctx, "/usr/bin/sudo", "/usr/bin/systemctl", "start", plan.Namespace.RunnerUnit)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("systemd start for selected lane failed (exit %d): %s", result.ExitCode, strings.TrimSpace(result.Output))
	}
	active, err = c.Status(ctx, plan)
	if err != nil {
		return fmt.Errorf("read selected lane systemd post-start state: %w", err)
	}
	if !active {
		return fmt.Errorf("selected lane systemd unit did not become active")
	}
	return c.completeInitialInstall(ctx, plan)
}

func (c *SimpleServiceController) completeInitialInstall(ctx context.Context, plan Plan) error {
	if c.initialInstall != nil {
		return c.initialInstall(ctx, plan)
	}
	return completeSimpleInitialInstall(ctx, plan)
}

func completeSimpleInitialInstall(ctx context.Context, plan Plan) error {
	marker, exists, err := readSimpleInstallMarker(plan.Namespace)
	if err != nil || !exists {
		return err
	}
	if marker.HostID != plan.HostID || marker.Lane != plan.Lane {
		return ErrSimpleLanePreflight
	}
	current, err := CurrentSimpleBundleSHA256(plan)
	if err != nil || current == "" {
		return ErrSimpleLanePreflight
	}
	_, bundle, err := selectedSimpleBundleForPlan(plan)
	if err != nil || bundle.Manifest.HostID != plan.HostID {
		return ErrSimpleLanePreflight
	}
	if err := waitForSimpleInitialDatabase(ctx, func() error {
		databaseExists, err := simplePathExists(plan.Namespace.Database)
		if err != nil {
			return err
		}
		if !databaseExists {
			return ErrSimpleLanePreflight
		}
		return validateSimpleInstalledDatabase(ctx, plan, bundle.Manifest.DatabaseSchemaMax)
	}); err != nil {
		return err
	}
	return removeSimpleInstallMarker(plan.Namespace, plan.HostID, plan.Lane)
}

func waitForSimpleInitialDatabase(ctx context.Context, validate func() error) error {
	if validate == nil {
		return ErrSimpleLanePreflight
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		err := validate()
		if err == nil {
			return nil
		}
		if !errors.Is(err, ErrSimpleLanePreflight) {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("initial lane database did not become ready before startup deadline: %w", ErrSimpleLanePreflight)
		case <-ticker.C:
		}
	}
}

func (c *SimpleServiceController) requireReady(ctx context.Context, plan Plan) error {
	if c.ready != nil {
		return c.ready(plan)
	}
	if !simplePlanArtifactsValid(plan) {
		return ErrSimpleLanePreflight
	}
	current, err := simpleCurrentBundleForPlan(plan)
	if err != nil || current == "" {
		return ErrSimpleLanePreflight
	}
	_, bundle, err := selectedSimpleBundleForPlan(plan)
	if err != nil || bundle.Manifest.HostID != plan.HostID {
		return ErrSimpleLanePreflight
	}
	if _, err := loadSimpleLaneAuthority(plan); err != nil {
		return err
	}
	marker, markerExists, err := readSimpleInstallMarker(plan.Namespace)
	if err != nil || (markerExists && (marker.HostID != plan.HostID || marker.Lane != plan.Lane)) {
		return ErrSimpleLanePreflight
	}
	if !markerExists {
		if err := validateSimpleInstalledDatabase(ctx, plan, bundle.Manifest.DatabaseSchemaMax); err != nil {
			return err
		}
	}
	for _, artifact := range plan.Artifacts {
		if artifact.Kind == ArtifactSystemdUnit {
			if err := verifySimpleSystemdArtifact(artifact); err != nil {
				return ErrSimpleLanePreflight
			}
		} else if err := verifyExistingSimpleArtifact(artifact); err != nil {
			return ErrSimpleLanePreflight
		}
	}
	return nil
}

func validateSimpleInstalledDatabase(ctx context.Context, plan Plan, maximumSchema int) error {
	exists, err := simplePathExists(plan.Namespace.Database)
	if err != nil {
		return err
	}
	if !exists {
		for _, sidecar := range []string{plan.Namespace.DatabaseWAL, plan.Namespace.DatabaseSHM} {
			present, err := simplePathExists(sidecar)
			if err != nil || present {
				return ErrSimpleLanePreflight
			}
		}
		if plan.Namespace.Host == config.HostKindMac {
			return requireSimpleMailboxEmpty(plan.Namespace.MailboxRoot)
		}
		return nil
	}
	database, err := store.OpenExistingRestartPreflightReadOnly(ctx, plan.Namespace.Database)
	if err != nil {
		return ErrSimpleLanePreflight
	}
	defer database.Close()
	var schema int
	if err := database.QueryRowContext(ctx, "PRAGMA user_version").Scan(&schema); err != nil || schema <= 0 || schema > maximumSchema {
		return ErrSimpleLanePreflight
	}
	return nil
}

func requireSimpleMailboxEmpty(root string) error {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return ErrSimpleLaneNotQuiescent
	}
	return nil
}

func (c *SimpleServiceController) Stop(ctx context.Context, plan Plan) error {
	ctx, cancel := simpleBoundedContext(ctx, 45*time.Second)
	defer cancel()
	if err := c.validate(plan); err != nil {
		return err
	}
	active, err := c.Status(ctx, plan)
	if err != nil {
		return err
	}
	if plan.Namespace.Host == config.HostKindMac {
		if active {
			labels := []string{plan.Namespace.LocalAgentLabel, plan.Namespace.LocalDAgentLabel}
			if err := c.stopMacAgents(ctx, plan, labels); err != nil {
				return err
			}
			if err := c.waitForMacLaneStop(ctx, plan); err != nil {
				return err
			}
		}
		// launchd can unload while an older runner-local child still owns its
		// Unix socket. Prove the lane has no work before gracefully stopping
		// only a listener whose executable, account, working directory, and
		// socket all identify this exact lane.
		if err := c.proveStoppedLaneQuiescence(ctx, plan); err != nil {
			return err
		}
		if err := c.stopMacOrphanSocketListeners(ctx, plan); err != nil {
			return err
		}
		return c.proveStoppedLaneQuiescence(ctx, plan)
	}
	if !active {
		return nil
	}
	result, err := c.runner.Run(ctx, "/usr/bin/sudo", "/usr/bin/systemctl", "stop", plan.Namespace.RunnerUnit)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("systemd stop for selected lane failed (exit %d): %s", result.ExitCode, strings.TrimSpace(result.Output))
	}
	active, err = c.Status(ctx, plan)
	if err != nil {
		return fmt.Errorf("read selected lane systemd post-stop state: %w", err)
	}
	if active {
		return fmt.Errorf("selected lane systemd unit remained active after stop")
	}
	result, err = c.runner.Run(ctx, "/usr/bin/systemctl", "show", "--property=Result", "--value", plan.Namespace.RunnerUnit)
	if err != nil {
		return fmt.Errorf("read selected lane systemd stop result: %w", err)
	}
	if result.ExitCode != 0 || strings.TrimSpace(result.Output) != "success" {
		return fmt.Errorf("selected lane systemd stop did not complete cleanly (exit %d, result %q)", result.ExitCode, strings.TrimSpace(result.Output))
	}
	return c.proveStoppedLaneQuiescence(ctx, plan)
}

func (c *SimpleServiceController) waitForMacLaneStop(ctx context.Context, plan Plan) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		active, err := c.Status(ctx, plan)
		if err != nil {
			return err
		}
		if !active {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("selected Mac lane agents remained active after stop: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func simpleBoundedContext(parent context.Context, maximum time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, maximum)
}

func (c *SimpleServiceController) proveStoppedLaneQuiescence(ctx context.Context, plan Plan) error {
	if c.stoppedProof != nil {
		return c.stoppedProof(plan)
	}
	lockFD, err := acquireSimpleLaneLock(plan)
	if err != nil {
		return err
	}
	defer func() {
		_ = unix.Flock(lockFD, unix.LOCK_UN)
		_ = unix.Close(lockFD)
	}()
	active, err := c.Status(ctx, plan)
	if err != nil {
		return err
	}
	if active {
		return ErrSimpleLaneNotQuiescent
	}
	current, err := simpleCurrentBundleForPlan(plan)
	if err != nil {
		return err
	}
	_, err = readSimpleLaneQuiescence(ctx, plan, current)
	return err
}

func (c *SimpleServiceController) macAgentStatus(ctx context.Context, label string) (bool, error) {
	result, err := c.runner.Run(ctx, "/bin/launchctl", "print", "gui/"+c.uid+"/"+label)
	if err != nil {
		return false, err
	}
	switch result.ExitCode {
	case 0:
		return true, nil
	case 113:
		return false, nil
	default:
		return false, fmt.Errorf("launchctl status for selected service failed (exit %d): %s", result.ExitCode, strings.TrimSpace(result.Output))
	}
}

func (c *SimpleServiceController) launchctl(ctx context.Context, args ...string) error {
	result, err := c.runner.Run(ctx, "/bin/launchctl", args...)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("launchctl %s failed (exit %d): %s", strings.Join(args, " "), result.ExitCode, strings.TrimSpace(result.Output))
	}
	return nil
}

func (c *SimpleServiceController) stopMacAgents(ctx context.Context, plan Plan, labels []string) error {
	for index := len(labels) - 1; index >= 0; index-- {
		label := labels[index]
		active, err := c.macAgentStatus(ctx, label)
		if err != nil {
			return err
		}
		if !active {
			continue
		}
		if err := c.launchctl(ctx, "bootout", "gui/"+c.uid+"/"+label); err != nil {
			return err
		}
	}
	return nil
}
