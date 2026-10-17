// Command runner-lane builds and operates only the selected Dev or QAS lane
// on one canonical existing host. It has no PRD target or arbitrary service
// name interface.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"remote-session-runner/src/internal/commandstub"
	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/laneinstall"
)

const maxLaneConfigBytes = 1 << 20

func main() {
	args := os.Args[1:]
	if len(args) == 0 || (len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help" || args[0] == "--version" || args[0] == "version")) {
		os.Exit(commandstub.Run("runner-lane", "Build, install, inspect, start, stop and roll back one existing-host Dev or QAS lane.", args, os.Stdout, os.Stderr))
	}
	os.Exit(run(args, os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if len(args) == 0 {
		return 2
	}
	var err error
	switch args[0] {
	case "plan":
		err = runPlan(args[1:], stdout, stderr)
	case "status", "start", "stop":
		err = runService(ctx, args[0], args[1:], stdout, stderr)
	case "bundle-build":
		err = runBundleBuild(ctx, args[1:], stdout, stderr)
	case "bundle-stage":
		err = runBundleStage(args[1:], stdout, stderr)
	case "install":
		err = runInstall(ctx, args[1:], stdout, stderr)
	case "rollback":
		err = runRollback(ctx, args[1:], stdout, stderr)
	case "bridge-install":
		err = runBridgeInstall(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "runner-lane: unknown command %q\n", args[0])
		return 2
	}
	if err != nil {
		fmt.Fprintf(stderr, "runner-lane %s failed: %v\n", args[0], err)
		return 1
	}
	return 0
}

func runPlan(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("runner-lane plan", flag.ContinueOnError)
	flags.SetOutput(stderr)
	host := flags.String("host", "", "canonical existing host ID")
	lane := flags.String("lane", "", "dev or qas")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	plan, err := simplePlan(*host, *lane)
	if err != nil {
		return err
	}
	return writeJSON(stdout, struct {
		HostID    string                 `json:"host_id"`
		Lane      config.ReleaseLane     `json:"lane"`
		Namespace config.LaneNamespace   `json:"namespace"`
		Artifacts []laneinstall.Artifact `json:"artifacts"`
	}{plan.HostID, plan.Lane, plan.Namespace, plan.Artifacts})
}

func runService(ctx context.Context, action string, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("runner-lane "+action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	host := flags.String("host", "", "canonical existing host ID")
	lane := flags.String("lane", "", "dev or qas")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	plan, err := simplePlan(*host, *lane)
	if err != nil {
		return err
	}
	controller, err := laneinstall.NewSimpleServiceController()
	if err != nil {
		return err
	}
	switch action {
	case "status":
		active, err := controller.Status(ctx, plan)
		if err != nil {
			return err
		}
		return writeJSON(stdout, struct {
			HostID string             `json:"host_id"`
			Lane   config.ReleaseLane `json:"lane"`
			Active bool               `json:"active"`
		}{plan.HostID, plan.Lane, active})
	case "start":
		err = controller.Start(ctx, plan)
	case "stop":
		err = controller.Stop(ctx, plan)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "host=%s lane=%s action=%s complete\n", plan.HostID, plan.Lane, action)
	return nil
}

func runBundleBuild(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("runner-lane bundle-build", flag.ContinueOnError)
	flags.SetOutput(stderr)
	host := flags.String("host", "", "canonical existing host ID")
	lane := flags.String("lane", "", "dev or qas; selects staging namespace only")
	repository := flags.String("repo", ".", "clean synchronized Mac or worker dev checkout")
	goBinary := flags.String("go", "", "Go 1.27.1 executable; defaults to the repository host toolchain")
	output := flags.String("output-dir", "", "absolute bundle directory outside the source checkout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *output == "" {
		return errors.New("usage: runner-lane bundle-build --host HOST --lane dev|qas --repo CHECKOUT --output-dir ABSOLUTE-OUTSIDE-CHECKOUT [--go TOOLCHAIN]")
	}
	plan, err := simplePlan(*host, *lane)
	if err != nil {
		return err
	}
	repositoryRoot, err := filepath.Abs(*repository)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(*output) {
		return errors.New("bundle output path must be a clean absolute path")
	}
	outputPath, err := filepath.Abs(*output)
	if err != nil || filepath.Clean(outputPath) != outputPath {
		return errors.New("bundle output path must be a clean absolute path")
	}
	if pathWithin(repositoryRoot, outputPath) {
		return errors.New("bundle output must be outside the source checkout")
	}
	bundle, err := laneinstall.BuildSimpleBundle(ctx, plan, repositoryRoot, *goBinary)
	if err != nil {
		return err
	}
	if err := laneinstall.WriteSimpleBundleDirectory(outputPath, bundle); err != nil {
		return err
	}
	return writeJSON(stdout, bundle.Manifest)
}

func runBundleStage(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("runner-lane bundle-stage", flag.ContinueOnError)
	flags.SetOutput(stderr)
	host := flags.String("host", "", "canonical existing host ID")
	lane := flags.String("lane", "", "dev or qas")
	bundlePath := flags.String("bundle-dir", "", "transferred immutable candidate directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *bundlePath == "" {
		return errors.New("usage: runner-lane bundle-stage --host HOST --lane dev|qas --bundle-dir CANDIDATE")
	}
	plan, err := simplePlan(*host, *lane)
	if err != nil {
		return err
	}
	bundle, err := laneinstall.LoadSimpleBundleDirectory(*bundlePath)
	if err != nil {
		return err
	}
	staged, err := laneinstall.StageSimpleBundle(plan, bundle)
	if err != nil {
		return err
	}
	return writeJSON(stdout, struct {
		HostID     string `json:"host_id"`
		Lane       string `json:"lane"`
		BundleSHA  string `json:"bundle_sha256"`
		StagedPath string `json:"staged_path"`
	}{plan.HostID, string(plan.Lane), bundle.Manifest.BundleSHA256, staged})
}

func runInstall(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("runner-lane install", flag.ContinueOnError)
	flags.SetOutput(stderr)
	host := flags.String("host", "", "canonical existing host ID")
	lane := flags.String("lane", "", "dev or qas")
	bundlePath := flags.String("bundle-dir", "", "immutable candidate directory already transferred to this host")
	configPath := flags.String("config-file", "", "complete owner-only V3 lane configuration for first install")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *bundlePath == "" {
		return errors.New("usage: runner-lane install --host HOST --lane dev|qas --bundle-dir CANDIDATE [--config-file FIRST-INSTALL-CONFIG]")
	}
	plan, err := simplePlan(*host, *lane)
	if err != nil {
		return err
	}
	bundle, err := laneinstall.LoadSimpleBundleDirectory(*bundlePath)
	if err != nil || bundle.Manifest.HostID != plan.HostID {
		return laneinstall.ErrInvalidSimpleBundle
	}
	stagedPath, err := laneinstall.StageSimpleBundle(plan, bundle)
	if err != nil {
		return err
	}
	var configContents []byte
	if *configPath != "" {
		configContents, err = readPrivateConfig(*configPath)
		if err != nil {
			return err
		}
	}
	controller, err := laneinstall.NewSimpleServiceController()
	if err != nil {
		return err
	}
	maintenance, err := laneinstall.BeginSimpleLaneMaintenance(ctx, plan, controller)
	if err != nil {
		return err
	}
	defer maintenance.Close()
	var backupDirectory string
	if maintenance.Fresh() {
		if len(configContents) == 0 {
			return errors.New("first install needs --config-file with a complete owner-only V3 configuration")
		}
		if err := maintenance.PrepareLayout(ctx); err != nil {
			return err
		}
		if err := maintenance.InstallConfiguration(ctx, configContents); err != nil {
			return err
		}
	} else {
		backup, err := maintenance.Backup(ctx)
		if err != nil {
			return err
		}
		backupDirectory = backup.Directory()
	}
	activation, err := maintenance.InstallBundle(ctx, stagedPath)
	if err != nil {
		return err
	}
	if err := maintenance.InstallArtifacts(ctx); err != nil {
		return err
	}
	return writeJSON(stdout, struct {
		HostID          string `json:"host_id"`
		Lane            string `json:"lane"`
		SourceRevision  string `json:"source_revision"`
		BundleSHA256    string `json:"bundle_sha256"`
		PreviousBundle  string `json:"previous_bundle_sha256,omitempty"`
		BackupDirectory string `json:"backup_directory,omitempty"`
		ServiceRunning  bool   `json:"service_running"`
	}{plan.HostID, string(plan.Lane), bundle.Manifest.SourceRevision, activation.ActivatedBundleSHA256(), activation.PreviousBundleSHA256(), backupDirectory, false})
}

func runRollback(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("runner-lane rollback", flag.ContinueOnError)
	flags.SetOutput(stderr)
	host := flags.String("host", "", "canonical existing host ID")
	lane := flags.String("lane", "", "dev or qas")
	backup := flags.String("backup-dir", "", "verified pre-install backup directory under the selected lane")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *backup == "" {
		return errors.New("usage: runner-lane rollback --host HOST --lane dev|qas --backup-dir VERIFIED-BACKUP")
	}
	plan, err := simplePlan(*host, *lane)
	if err != nil {
		return err
	}
	controller, err := laneinstall.NewSimpleServiceController()
	if err != nil {
		return err
	}
	maintenance, err := laneinstall.BeginSimpleLaneMaintenance(ctx, plan, controller)
	if err != nil {
		return err
	}
	defer maintenance.Close()
	if err := maintenance.RollbackFromBackup(ctx, *backup); err != nil {
		return err
	}
	current, err := laneinstall.CurrentSimpleBundleSHA256(plan)
	if err != nil {
		return err
	}
	return writeJSON(stdout, struct {
		HostID         string `json:"host_id"`
		Lane           string `json:"lane"`
		CurrentBundle  string `json:"current_bundle_sha256"`
		ServiceRunning bool   `json:"service_running"`
	}{plan.HostID, string(plan.Lane), current, false})
}

func runBridgeInstall(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("runner-lane bridge-install", flag.ContinueOnError)
	flags.SetOutput(stderr)
	host := flags.String("host", "", "canonical Linux worker profile")
	lane := flags.String("lane", "", "dev or qas")
	localHost := flags.String("local-host-id", "", "identity of this host: mac-control-plane or the selected Linux worker")
	dispatcherKeyPath := flags.String("dispatcher-public-key", "", "lane-specific Ed25519 dispatcher public key")
	hostKeyPath := flags.String("host-public-key", "", "existing ssh-ed25519 host public key")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *dispatcherKeyPath == "" || *hostKeyPath == "" {
		return errors.New("usage: runner-lane bridge-install --host LINUX-PROFILE --lane dev|qas --local-host-id HOST --dispatcher-public-key FILE --host-public-key FILE")
	}
	dispatcherKey, err := readPublicKey(*dispatcherKeyPath)
	if err != nil {
		return err
	}
	hostKey, err := readPublicKey(*hostKeyPath)
	if err != nil {
		return err
	}
	plan, err := laneinstall.BuildSimpleQueuedSSHPlan(*host, config.ReleaseLane(*lane), dispatcherKey, hostKey)
	if err != nil {
		return err
	}
	if err := laneinstall.InstallSimpleQueuedSSHArtifacts(plan, *localHost); err != nil {
		return err
	}
	return writeJSON(stdout, struct {
		HostID string `json:"host_id"`
		Lane   string `json:"lane"`
		Port   int    `json:"port"`
		Effect string `json:"effect"`
	}{plan.HostID, string(plan.Lane), plan.Port, "selected-lane forced bridge key and wrapper only; no sshd or Runner service restart"})
}

func simplePlan(host, lane string) (laneinstall.Plan, error) {
	return laneinstall.BuildSimplePlan(host, config.ReleaseLane(strings.ToLower(lane)))
}

func readPrivateConfig(path string) ([]byte, error) {
	if filepath.Clean(path) != path || !filepath.IsAbs(path) {
		return nil, errors.New("configuration input must be an absolute clean path")
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() <= 0 || info.Size() > maxLaneConfigBytes {
		return nil, errors.New("configuration input must be a regular owner-only 0600 file within the size limit")
	}
	return os.ReadFile(path)
}

func readPublicKey(path string) (string, error) {
	if filepath.Clean(path) != path || !filepath.IsAbs(path) {
		return "", errors.New("public key input must be an absolute clean path")
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 4096 {
		return "", errors.New("public key input must be a regular bounded file")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(contents)), nil
}

func pathWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && (relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))))
}

func writeJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
