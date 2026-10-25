// Command runner-lane builds and operates Dev/QAS lanes and a separate,
// explicitly gated in-place maintenance path for the canonical unsuffixed
// PRD service on one existing host. It has no arbitrary service interface.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"remote-session-runner/src/internal/commandstub"
	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/laneinstall"
)

const maxLaneConfigBytes = 1 << 20

var catalogCLIHostOrder = []string{"linux-host", "sandbox-host", "factory-sandbox", "mac-control-plane"}

// catalogCommandSubprocessFixtureBackend is a test-only dependency seam for
// the public-command acceptance harness. Production binaries never populate
// this package-local variable and expose no CLI flag or environment switch
// for fixture roots; their existing platform, account, service, and path
// guards remain the only execution path.
type catalogCommandSubprocessFixtureBackend interface {
	RunCohort(context.Context, catalogCohortCommand, io.Writer, io.Writer) error
	RunSelector(context.Context, string, catalogSelectorCommand, io.Writer, io.Writer) error
	RunPRDRecovery(context.Context, catalogPRDRecoveryCommand, io.Writer, io.Writer) error
}

// Set only by package tests running the subprocess harness. It is nil in all
// normal runner-lane builds.
var catalogCommandSubprocessFixtureBackendForTests catalogCommandSubprocessFixtureBackend

type catalogCohortCommand struct {
	HostID                 string
	Lane                   string
	Action                 string
	OperationID            string
	SuccessorOperation     string
	ReceiptFile            string
	ReleaseManifest        string
	PublisherOwner         string
	PublisherControl       string
	PublishersPaused       bool
	NoNewAccepted          bool
	SubjectHost            string
	EvidenceSHA256         string
	ReplacementPredecessor string
	PreflightFiles         []string
	HistoryFiles           []string
}

type catalogSelectorCommand struct {
	HostID            string
	Lane              string
	ReleaseID         string
	ExpectedTarget    string
	CohortOperationID string
	SelectorOperation string
	EvidenceSHA256    string
}

type catalogPRDRecoveryCommand struct {
	HostID                        string
	ReleaseID                     string
	ExpectedReleaseSHA256         string
	ExpectedBundleSHA256          string
	ExpectedSelectedReleaseID     string
	ExpectedSelectedReleaseSHA256 string
	ExpectedSelectedBundleSHA256  string
	BackupDirectory               string
	ExpectedBackupManifestSHA     string
	LostPairs                     []string
}

func main() {
	args := os.Args[1:]
	if len(args) == 0 || (len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help" || args[0] == "--version" || args[0] == "version")) {
		os.Exit(commandstub.Run("runner-lane", "Build and operate existing-host Dev/QAS lanes; adopt and publish sealed releases; inspect selectors; validate legacy PRD compatibility; run guarded unsuffixed PRD maintenance.", args, os.Stdout, os.Stderr))
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
	case "prd-preflight":
		err = runPRDPreflight(ctx, args[1:], stdout, stderr)
	case "prd-status", "prd-start", "prd-stop":
		err = runPRDService(ctx, args[0], args[1:], stdout, stderr)
	case "prd-backup":
		err = runPRDBackup(ctx, args[1:], stdout, stderr)
	case "prd-install":
		err = runPRDInstall(ctx, args[1:], stdout, stderr)
	case "prd-migrate":
		err = runPRDMigrate(ctx, args[1:], stdout, stderr)
	case "prd-rollback":
		err = runPRDRollback(ctx, args[1:], stdout, stderr)
	case "bridge-install":
		err = runBridgeInstall(args[1:], stdout, stderr)
	case "catalog-manifest":
		err = runCatalogManifest(args[1:], stdout, stderr)
	case "catalog-publish":
		err = runCatalogPublish(args[1:], stdout, stderr)
	case "catalog-inspect":
		err = runCatalogInspect(args[1:], stdout, stderr)
	case "catalog-status":
		err = runCatalogStatus(args[1:], stdout, stderr)
	case "catalog-selector-find":
		err = runCatalogSelectorFind(args[1:], stdout, stderr)
	case "catalog-adopt":
		err = runCatalogAdopt(args[1:], stdout, stderr)
	case "catalog-cohort":
		err = runCatalogCohort(ctx, args[1:], stdout, stderr)
	case "catalog-cohort-renewal":
		err = runCatalogCohortRenewal(ctx, args[1:], stdout, stderr)
	case "catalog-refresh-entrypoint":
		err = runCatalogRefreshEntrypoint(ctx, args[1:], stdout, stderr)
	case "catalog-work-inventory":
		err = runCatalogWorkInventory(ctx, args[1:], stdout, stderr)
	case "catalog-pre-switch-check":
		err = runCatalogPreSwitchCheck(ctx, args[1:], stdout, stderr)
	case "catalog-host-preflight":
		err = runCatalogHostPreflight(ctx, args[1:], stdout, stderr)
	case "catalog-compatibility-check":
		err = runCatalogCompatibilityCheck(ctx, args[1:], stdout, stderr)
	case "catalog-prd-backup":
		err = runCatalogPRDBackup(ctx, args[1:], stdout, stderr)
	case "catalog-prd-recover":
		err = runCatalogPRDRecover(ctx, args[1:], stdout, stderr)
	case "catalog-probe-prepare":
		err = runCatalogProbePrepare(args[1:], stdout, stderr)
	case "catalog-select", "catalog-reconcile", "catalog-rollback":
		err = runCatalogSelectorMaintenance(ctx, args[0], args[1:], stdout, stderr)
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

func runCatalogSelectorFind(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("runner-lane catalog-selector-find", flag.ContinueOnError)
	flags.SetOutput(stderr)
	host := flags.String("host", "", "canonical identity of this existing host")
	lane := flags.String("lane", "", "dev, qas, or prd")
	cohortID := flags.String("cohort-operation-id", "", "exact cohort whose selector journals are inspected")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || !currentCatalogHost(*host) || (*lane != "dev" && *lane != "qas" && *lane != "prd") || !validCLIProbeOperationID(*cohortID) {
		return errors.New("usage: runner-lane catalog-selector-find --host HOST --lane dev|qas|prd --cohort-operation-id ID")
	}
	plan, err := laneinstall.BuildCatalogPlan(*host, config.ReleaseLane(*lane))
	if err != nil {
		return err
	}
	operations, err := laneinstall.FindCatalogSelectorOperationsForPlan(plan, *cohortID)
	if err != nil {
		return err
	}
	return writeJSON(stdout, operations)
}

func runCatalogRefreshEntrypoint(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("runner-lane catalog-refresh-entrypoint", flag.ContinueOnError)
	flags.SetOutput(stderr)
	host := flags.String("host", "", "canonical existing Linux worker identity")
	lane := flags.String("lane", "", "dev or qas")
	cohortOperationID := flags.String("cohort-operation-id", "", "held four-host cohort operation identity")
	expectedCurrentSHA256 := flags.String("expected-current-sha256", "", "exact observed prior launcher digest")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || (*host != "linux-host" && *host != "sandbox-host" && *host != "factory-sandbox") ||
		(*lane != "dev" && *lane != "qas") || *cohortOperationID == "" || len(*expectedCurrentSHA256) != 64 {
		return errors.New("usage: runner-lane catalog-refresh-entrypoint --host LINUX-HOST --lane dev|qas --cohort-operation-id ID --expected-current-sha256 SHA256")
	}
	plan, err := laneinstall.BuildCatalogPlan(*host, config.ReleaseLane(*lane))
	if err != nil {
		return err
	}
	controller, err := laneinstall.NewSimpleServiceController()
	if err != nil {
		return err
	}
	maintenance, err := laneinstall.BeginCatalogLaneMaintenance(ctx, plan, controller)
	if err != nil {
		return err
	}
	defer maintenance.Close()
	receipt, err := laneinstall.ReadCatalogCohortReceipt(plan, *cohortOperationID)
	if err != nil || receipt.Stage != laneinstall.CatalogCohortSwitching {
		return laneinstall.ErrCatalogOperationInvalid
	}
	operation, err := maintenance.RefreshLinuxEntrypoint(ctx, *cohortOperationID, *expectedCurrentSHA256)
	if err != nil {
		return err
	}
	return writeJSON(stdout, operation)
}

func runCatalogManifest(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("runner-lane catalog-manifest", flag.ContinueOnError)
	flags.SetOutput(stderr)
	releaseID := flags.String("release-id", "", "immutable release label")
	factoryManifest := flags.String("factory-manifest", "", "factory-sandbox bundle manifest.json")
	linuxManifest := flags.String("linux-manifest", "", "linux-host bundle manifest.json")
	macManifest := flags.String("mac-manifest", "", "mac-control-plane bundle manifest.json")
	sandboxManifest := flags.String("sandbox-manifest", "", "sandbox-host bundle manifest.json")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *releaseID == "" {
		return errors.New("usage: runner-lane catalog-manifest --release-id ID --factory-manifest FILE --linux-manifest FILE --mac-manifest FILE --sandbox-manifest FILE")
	}
	paths := map[string]string{
		"factory-sandbox":   *factoryManifest,
		"linux-host":        *linuxManifest,
		"mac-control-plane": *macManifest,
		"sandbox-host":      *sandboxManifest,
	}
	manifests := make(map[string]laneinstall.SimpleBundleManifest, len(paths))
	for hostID, path := range paths {
		if path == "" {
			return fmt.Errorf("missing %s bundle manifest", hostID)
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		manifest, err := laneinstall.LoadSimpleBundleManifest(absolute)
		if err != nil {
			return fmt.Errorf("validate %s bundle metadata: %w", hostID, err)
		}
		manifests[hostID] = manifest
	}
	manifest, err := laneinstall.NewCatalogReleaseManifestFromManifests(*releaseID, manifests)
	if err != nil {
		return err
	}
	encoded, err := laneinstall.CatalogReleaseManifestBytes(manifest)
	if err != nil {
		return err
	}
	_, err = stdout.Write(encoded)
	return err
}

func runCatalogPublish(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("runner-lane catalog-publish", flag.ContinueOnError)
	flags.SetOutput(stderr)
	host := flags.String("host", "", "canonical identity of this existing host")
	manifestPath := flags.String("manifest-file", "", "sealed release.json")
	bundlePath := flags.String("bundle-dir", "", "this host's complete native bundle directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *manifestPath == "" || *bundlePath == "" || !currentCatalogHost(*host) {
		return errors.New("usage: runner-lane catalog-publish --host THIS-HOST --manifest-file RELEASE-JSON --bundle-dir LOCAL-BUNDLE")
	}
	manifestFile, err := filepath.Abs(*manifestPath)
	if err != nil {
		return err
	}
	bundleDirectory, err := filepath.Abs(*bundlePath)
	if err != nil {
		return err
	}
	manifest, err := laneinstall.LoadCatalogReleaseManifest(manifestFile)
	if err != nil {
		return err
	}
	bundle, err := laneinstall.LoadSimpleBundleDirectory(bundleDirectory)
	if err != nil {
		return err
	}
	root, err := localCatalogRoot(*host)
	if err != nil {
		return err
	}
	identity, err := laneinstall.PublishCatalogRelease(root, *host, manifest, bundle)
	if err != nil {
		return err
	}
	return writeJSON(stdout, identity)
}

func runCatalogInspect(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("runner-lane catalog-inspect", flag.ContinueOnError)
	flags.SetOutput(stderr)
	host := flags.String("host", "", "canonical identity of this existing host")
	releaseID := flags.String("release-id", "", "immutable release label")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *releaseID == "" || !currentCatalogHost(*host) {
		return errors.New("usage: runner-lane catalog-inspect --host THIS-HOST --release-id ID")
	}
	root, err := localCatalogRoot(*host)
	if err != nil {
		return err
	}
	status, err := laneinstall.InspectCatalogRelease(root, *host, *releaseID)
	if err != nil {
		return err
	}
	return writeJSON(stdout, status)
}

func runCatalogStatus(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("runner-lane catalog-status", flag.ContinueOnError)
	flags.SetOutput(stderr)
	host := flags.String("host", "", "canonical identity of this existing host")
	lane := flags.String("lane", "", "dev, qas, or prd")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || !currentCatalogHost(*host) {
		return errors.New("usage: runner-lane catalog-status --host THIS-HOST --lane dev|qas|prd")
	}
	plan, err := laneinstall.BuildCatalogPlan(*host, config.ReleaseLane(*lane))
	if err != nil {
		return err
	}
	root, err := localCatalogRoot(*host)
	if err != nil {
		return err
	}
	status, err := laneinstall.InspectCatalogSelectorForPlan(plan, root)
	if err != nil {
		return err
	}
	return writeJSON(stdout, status)
}

func runCatalogAdopt(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("runner-lane catalog-adopt", flag.ContinueOnError)
	flags.SetOutput(stderr)
	host := flags.String("host", "", "canonical identity of this existing host")
	lane := flags.String("lane", "", "dev or qas")
	output := flags.String("output-dir", "", "external immutable bundle copy destination")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *output == "" || !currentCatalogHost(*host) || !filepath.IsAbs(*output) || filepath.Clean(*output) != *output {
		return errors.New("usage: runner-lane catalog-adopt --host THIS-HOST --lane dev|qas --output-dir ABSOLUTE-OUTSIDE-LANE-AND-CATALOG")
	}
	plan, err := simplePlan(*host, *lane)
	if err != nil {
		return err
	}
	manifest, err := laneinstall.CopySelectedLegacyBundleForCatalog(plan, *output)
	if err != nil {
		return err
	}
	return writeJSON(stdout, manifest)
}

func runCatalogCohort(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("runner-lane catalog-cohort", flag.ContinueOnError)
	flags.SetOutput(stderr)
	host := flags.String("host", "", "canonical identity of this existing host")
	lane := flags.String("lane", "", "dev, qas, or prd")
	action := flags.String("action", "", "create, discover, inspect, prepare, sync, status, hold, preflight, verify-start, verify, complete, block, resume, rollback-fence-host, rollback-start, rollback-complete, open, reconcile-history, supersede-host, supersession-restart, or supersede")
	operationID := flags.String("operation-id", "", "32-character cohort operation identity")
	successorOperationID := flags.String("successor-operation-id", "", "held successor cohort identity for safe supersession")
	replacementPredecessor := flags.String("replacement-predecessor", "", "exact fully switched verifying PRD predecessor during guarded replacement preflight/fence")
	receiptFile := flags.String("receipt-file", "", "owner-only cohort JSON for prepare or sync")
	releaseManifestFile := flags.String("release-manifest", "", "sealed release.json for supported receipt creation")
	publisherOwner := flags.String("publisher-owner", "", "owner attesting the selected lane's publisher fence")
	publisherControl := flags.String("publisher-control", "", "specific control keeping selected-lane publishers paused or unassigned")
	publishersPaused := flags.Bool("publishers-paused", false, "confirm selected-lane publisher admissions are fenced")
	noNewAccepted := flags.Bool("no-new-accepted", false, "confirm no new work was accepted by the selected lane")
	subjectHost := flags.String("subject-host", "", "host identity recorded by preflight, switch, verify, or rollback-host")
	evidenceSHA := flags.String("evidence-sha256", "", "digest of the bounded host/operator evidence")
	var preflightFiles repeatedStringFlag
	flags.Var(&preflightFiles, "preflight-file", "canonical owner-only catalog-host-preflight report; repeat once per host for create")
	var historyFiles repeatedStringFlag
	flags.Var(&historyFiles, "history-file", "one existing receipt copy for reconcile-history; repeat once per observed history")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if catalogCommandSubprocessFixtureBackendForTests != nil {
		return catalogCommandSubprocessFixtureBackendForTests.RunCohort(ctx, catalogCohortCommand{
			HostID: *host, Lane: *lane, Action: *action, OperationID: *operationID,
			SuccessorOperation: *successorOperationID, ReplacementPredecessor: *replacementPredecessor, ReceiptFile: *receiptFile,
			ReleaseManifest: *releaseManifestFile, PublisherOwner: *publisherOwner,
			PublisherControl: *publisherControl, PublishersPaused: *publishersPaused,
			NoNewAccepted: *noNewAccepted, SubjectHost: *subjectHost,
			EvidenceSHA256: *evidenceSHA, PreflightFiles: append([]string(nil), preflightFiles...),
			HistoryFiles: append([]string(nil), historyFiles...),
		}, stdout, stderr)
	}
	if !currentCatalogHost(*host) {
		return errors.New("usage: runner-lane catalog-cohort --host THIS-HOST --lane dev|qas|prd --action ACTION [--operation-id ID] [--successor-operation-id ID] [--receipt-file FILE] [--subject-host HOST] [--evidence-sha256 SHA256]")
	}
	if *replacementPredecessor != "" && (*lane != "prd" || *action != "fence-host" || !validCLIProbeOperationID(*replacementPredecessor)) {
		return errors.New("--replacement-predecessor is valid only for a PRD fence-host action with an exact operation ID")
	}
	plan, err := laneinstall.BuildCatalogPlan(*host, config.ReleaseLane(*lane))
	if err != nil {
		return err
	}
	if *action == "inspect" {
		if !validCLIProbeOperationID(*operationID) || *successorOperationID != "" || *receiptFile != "" || *releaseManifestFile != "" ||
			len(preflightFiles) != 0 || len(historyFiles) != 0 || *publisherOwner != "" || *publisherControl != "" ||
			*publishersPaused || *noNewAccepted || *subjectHost != "" || *evidenceSHA != "" {
			return errors.New("inspect requires only a host, lane, and operation ID")
		}
		path := filepath.Join(plan.Namespace.Staging, "catalog-cohorts", *operationID+".json")
		info, statErr := os.Lstat(path)
		if errors.Is(statErr, os.ErrNotExist) {
			return writeJSON(stdout, struct {
				Present bool `json:"present"`
			}{false})
		}
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return laneinstall.ErrCatalogOperationInvalid
		}
		receipt, readErr := laneinstall.ReadCatalogCohortReceipt(plan, *operationID)
		if readErr != nil {
			return readErr
		}
		return writeJSON(stdout, struct {
			Present bool                             `json:"present"`
			Receipt laneinstall.CatalogCohortReceipt `json:"receipt"`
		}{true, receipt})
	}
	if *action == "discover" {
		if *operationID != "" || *successorOperationID != "" || *receiptFile != "" || *releaseManifestFile != "" ||
			len(preflightFiles) != 0 || len(historyFiles) != 0 || *publisherOwner != "" || *publisherControl != "" ||
			*publishersPaused || *noNewAccepted || *subjectHost != "" || *evidenceSHA != "" {
			return errors.New("discover is read-only and accepts no operation, receipt, or transition inputs")
		}
		held, operation, holdErr := laneinstall.CatalogCohortAdmissionHeld(plan)
		if holdErr != nil {
			return holdErr
		}
		var receipt *laneinstall.CatalogCohortReceipt
		if held {
			found, readErr := laneinstall.ReadCatalogCohortReceipt(plan, operation)
			if readErr != nil {
				return readErr
			}
			receipt = &found
		}
		return writeJSON(stdout, struct {
			HostID        string                            `json:"host_id"`
			Lane          config.ReleaseLane                `json:"lane"`
			AdmissionHeld bool                              `json:"admission_held"`
			OperationID   string                            `json:"operation_id,omitempty"`
			Receipt       *laneinstall.CatalogCohortReceipt `json:"receipt,omitempty"`
		}{*host, plan.Lane, held, operation, receipt})
	}
	if *action == "create" {
		if *host != "mac-control-plane" || (*lane != "dev" && *lane != "qas" && *lane != "prd") ||
			(*operationID != "" && !validCLIProbeOperationID(*operationID)) || *successorOperationID != "" ||
			*receiptFile != "" || *subjectHost != "" || *evidenceSHA != "" || *releaseManifestFile == "" || len(preflightFiles) != 4 ||
			*publisherOwner == "" || *publisherControl == "" || !*publishersPaused || !*noNewAccepted || len(historyFiles) != 0 {
			return errors.New("create requires the Mac coordinator, a lane, a sealed release manifest, four host preflight reports, and explicit publisher-pause attestation")
		}
		manifest, err := laneinstall.LoadCatalogReleaseManifest(*releaseManifestFile)
		if err != nil {
			return err
		}
		reports := make([]laneinstall.CatalogCohortHostPreflightReport, 0, len(preflightFiles))
		for _, path := range preflightFiles {
			report, reportErr := laneinstall.LoadCatalogCohortHostPreflightReportFile(path)
			if reportErr != nil {
				return reportErr
			}
			reports = append(reports, report)
		}
		pause := laneinstall.CatalogCohortPublisherPause{Owner: *publisherOwner, Control: *publisherControl, Paused: *publishersPaused, NoNewAccepted: *noNewAccepted}
		var receipt laneinstall.CatalogCohortReceipt
		if *operationID == "" {
			receipt, err = laneinstall.NewCatalogCohortReceipt(config.ReleaseLane(*lane), manifest, reports, pause, time.Now().UTC())
		} else {
			receipt, err = laneinstall.NewCatalogCohortReceiptWithOperationID(config.ReleaseLane(*lane), manifest, reports, pause, *operationID, time.Now().UTC())
		}
		if err != nil {
			return err
		}
		path, err := laneinstall.WriteCatalogCohortReceipt(plan, receipt)
		if err != nil {
			return err
		}
		return writeJSON(stdout, struct {
			OperationID string                         `json:"operation_id"`
			Path        string                         `json:"local_receipt_path"`
			Stage       laneinstall.CatalogCohortStage `json:"stage"`
			ReleaseID   string                         `json:"release_id"`
			ReleaseSHA  string                         `json:"release_sha256"`
		}{receipt.OperationID, path, receipt.Stage, receipt.ReleaseID, receipt.ReleaseSHA256})
	}
	if *action == "reconcile-history" {
		if !validCLIProbeOperationID(*operationID) || *receiptFile != "" || *successorOperationID != "" ||
			*subjectHost != "" || *evidenceSHA != "" || *releaseManifestFile != "" || len(preflightFiles) != 0 ||
			*publisherOwner != "" || *publisherControl != "" || *publishersPaused || *noNewAccepted || len(historyFiles) < 2 {
			return errors.New("reconcile-history requires --operation-id and at least two --history-file inputs")
		}
		receipts := make([]laneinstall.CatalogCohortReceipt, 0, len(historyFiles))
		for _, path := range historyFiles {
			receipt, readErr := laneinstall.LoadCatalogCohortReceiptFile(path)
			if readErr != nil || receipt.OperationID != *operationID || receipt.Lane != plan.Lane {
				return laneinstall.ErrCatalogOperationInvalid
			}
			receipts = append(receipts, receipt)
		}
		reconciled, reconcileErr := laneinstall.ReconcileCatalogCohortReceiptHistories(receipts, time.Now().UTC())
		if reconcileErr != nil {
			return reconcileErr
		}
		path, writeErr := laneinstall.SyncCatalogCohortReceipt(context.Background(), plan, reconciled)
		if writeErr != nil {
			return writeErr
		}
		return writeJSON(stdout, struct {
			OperationID string `json:"operation_id"`
			Path        string `json:"local_receipt_path"`
			Stage       string `json:"stage"`
			Transitions int    `json:"transition_count"`
		}{reconciled.OperationID, path, string(reconciled.Stage), len(reconciled.Transitions)})
	}
	if len(historyFiles) != 0 {
		return errors.New("--history-file is only valid with --action reconcile-history")
	}
	if *action == "prepare" {
		if *receiptFile == "" || *operationID != "" || *successorOperationID != "" || *subjectHost != "" || *evidenceSHA != "" ||
			*releaseManifestFile != "" || len(preflightFiles) != 0 || *publisherOwner != "" || *publisherControl != "" || *publishersPaused || *noNewAccepted {
			return errors.New("prepare requires only --receipt-file in addition to host and lane")
		}
		receipt, err := laneinstall.LoadCatalogCohortReceiptFile(*receiptFile)
		if err != nil || receipt.Lane != plan.Lane {
			return laneinstall.ErrCatalogOperationInvalid
		}
		path, err := laneinstall.WriteCatalogCohortReceipt(plan, receipt)
		if err != nil {
			return err
		}
		return writeJSON(stdout, struct {
			OperationID string `json:"operation_id"`
			Path        string `json:"local_receipt_path"`
			Stage       string `json:"stage"`
		}{receipt.OperationID, path, string(receipt.Stage)})
	}
	if *action == "sync" {
		if *receiptFile == "" || *operationID != "" || *successorOperationID != "" || *subjectHost != "" || *evidenceSHA != "" ||
			*releaseManifestFile != "" || len(preflightFiles) != 0 || *publisherOwner != "" || *publisherControl != "" || *publishersPaused || *noNewAccepted {
			return errors.New("sync requires only --receipt-file in addition to host and lane")
		}
		incoming, err := laneinstall.LoadCatalogCohortReceiptFile(*receiptFile)
		if err != nil || incoming.Lane != plan.Lane {
			return laneinstall.ErrCatalogOperationInvalid
		}
		path, err := laneinstall.SyncCatalogCohortReceipt(context.Background(), plan, incoming)
		if err != nil {
			return err
		}
		return writeJSON(stdout, struct {
			OperationID string `json:"operation_id"`
			Path        string `json:"local_receipt_path"`
			Stage       string `json:"stage"`
		}{incoming.OperationID, path, string(incoming.Stage)})
	}
	if *operationID == "" || *receiptFile != "" || *releaseManifestFile != "" || len(preflightFiles) != 0 || *publisherOwner != "" ||
		*publisherControl != "" || *publishersPaused || *noNewAccepted {
		return errors.New("cohort actions require --operation-id and do not accept --receipt-file")
	}
	receipt, err := laneinstall.ReadCatalogCohortReceipt(plan, *operationID)
	if err != nil {
		return err
	}
	if *action == "status" {
		if *subjectHost != "" || *evidenceSHA != "" || *successorOperationID != "" {
			return errors.New("status does not accept host subjects or evidence digests")
		}
		return writeJSON(stdout, receipt)
	}
	allowed := map[string]struct{}{
		"hold": {}, "preflight": {}, "verify-start": {}, "verify": {}, "complete": {}, "block": {},
		"resume": {}, "rollback-fence-host": {}, "rollback-start": {}, "rollback-complete": {}, "open": {}, "supersede-host": {}, "supersession-restart": {}, "supersede": {}, "fence-host": {},
	}
	if _, ok := allowed[*action]; !ok {
		return errors.New("unsupported cohort action")
	}
	perHost := *action == "preflight" || *action == "verify" || *action == "supersede-host" || *action == "fence-host" || *action == "rollback-fence-host"
	if perHost != (*subjectHost != "") {
		return errors.New("host-scoped cohort actions require --subject-host; other actions forbid it")
	}
	if perHost && *subjectHost != *host {
		return errors.New("host-scoped cohort evidence must be recorded by that host")
	}
	needsSuccessor := *action == "supersede-host" || *action == "supersession-restart" || *action == "supersede"
	needsEvidence := !needsSuccessor && *action != "preflight" && *action != "fence-host" && *action != "rollback-fence-host"
	if needsSuccessor != (*successorOperationID != "") || needsSuccessor && *evidenceSHA != "" ||
		needsEvidence && *evidenceSHA == "" || (*action == "preflight" || *action == "fence-host") && *evidenceSHA != "" {
		return errors.New("supersession requires --successor-operation-id and computes its own evidence; other actions require --evidence-sha256")
	}
	if *action == "hold" {
		active, err := catalogCohortLocalServiceActive(context.Background(), plan)
		if err != nil {
			return err
		}
		if active {
			return errors.New("selected lane service must be stopped before the local cohort hold is recorded")
		}
	}
	if *action == "verify-start" || *action == "verify" || *action == "complete" || *action == "open" {
		var verifyErr error
		if *action == "open" && receipt.Stage == laneinstall.CatalogCohortRolledBack {
			verifyErr = verifyLocalCatalogCohortRollbackTarget(plan, receipt)
		} else {
			verifyErr = verifyLocalCatalogCohortTarget(plan, receipt)
		}
		if verifyErr != nil {
			return verifyErr
		}
	}
	if *action == "fence-host" {
		if plan.Lane != config.ReleaseLanePRD {
			return laneinstall.ErrCatalogOperationInvalid
		}
		prdPlan, planErr := laneinstall.BuildPRDUpgradePlan(*host)
		if planErr != nil {
			return planErr
		}
		service, serviceErr := laneinstall.NewPRDServiceController()
		if serviceErr != nil {
			return serviceErr
		}
		var fence laneinstall.CatalogCohortHostFence
		var fenceErr error
		if *replacementPredecessor == "" {
			fence, fenceErr = laneinstall.FenceCatalogPRDHost(ctx, prdPlan, service, receipt)
		} else {
			fence, fenceErr = laneinstall.FenceCatalogPRDReplacementHost(ctx, prdPlan, service, receipt, *replacementPredecessor)
		}
		if fenceErr != nil {
			return fenceErr
		}
		receipt, err = laneinstall.AdvanceCatalogCohortHostFence(receipt, fence, time.Now().UTC())
	} else if *action == "rollback-fence-host" {
		fence, fenceErr := laneinstall.ObserveCatalogCohortRollbackFence(ctx, *host, *lane, *operationID)
		if fenceErr != nil {
			return fenceErr
		}
		receipt, err = laneinstall.AdvanceCatalogCohortRollbackFence(receipt, fence, time.Now().UTC())
	} else if *action == "preflight" {
		var expected laneinstall.CatalogCohortHostPreflight
		for _, hostPreflight := range receipt.Hosts {
			if hostPreflight.HostID == *host {
				expected = hostPreflight
				break
			}
		}
		if expected.HostID == "" {
			return laneinstall.ErrCatalogOperationInvalid
		}
		var report laneinstall.CatalogCohortHostPreflightReport
		if plan.Lane == config.ReleaseLanePRD {
			if err := laneinstall.CatalogCohortAdmissionHoldMatches(plan, receipt.OperationID); err != nil {
				return err
			}
			prdPlan, planErr := laneinstall.BuildPRDUpgradePlan(*host)
			if planErr != nil {
				return planErr
			}
			service, serviceErr := laneinstall.NewPRDServiceController()
			if serviceErr != nil {
				return serviceErr
			}
			maintenance, maintenanceErr := laneinstall.BeginCatalogPRDMaintenance(ctx, prdPlan, service, receipt.OperationID)
			if maintenanceErr != nil {
				return maintenanceErr
			}
			defer maintenance.Close()
			if err := maintenance.UseVerifiedBackup(ctx, expected.BackupDirectory, expected.BackupManifestSHA256); err != nil {
				return err
			}
			report, err = maintenance.CohortHostPreflight(ctx, expected)
		} else {
			active, activeErr := catalogCohortLocalServiceActive(ctx, plan)
			if activeErr != nil || active {
				return laneinstall.ErrSimpleLaneNotQuiescent
			}
			controller, controllerErr := laneinstall.NewSimpleServiceController()
			if controllerErr != nil {
				return controllerErr
			}
			maintenance, maintenanceErr := laneinstall.BeginCatalogLaneMaintenance(ctx, plan, controller)
			if maintenanceErr != nil {
				return maintenanceErr
			}
			defer maintenance.Close()
			if err := maintenance.UseVerifiedBackup(ctx, expected.BackupDirectory, expected.BackupManifestSHA256); err != nil {
				return err
			}
			report, err = maintenance.CohortHostPreflight(ctx, receipt.ReleaseID, expected.RequiredBytes)
		}
		if err != nil {
			return err
		}
		observed := report.Preflight
		if observed.HostID != expected.HostID || observed.PreviousTarget != expected.PreviousTarget ||
			observed.PreviousBundleSHA256 != expected.PreviousBundleSHA256 || observed.PreviousReadSchemaMax != expected.PreviousReadSchemaMax ||
			observed.Target != expected.Target || observed.ConfigurationSHA256 != expected.ConfigurationSHA256 ||
			observed.SchemaVersion != expected.SchemaVersion || observed.WorkInventorySHA256 != expected.WorkInventorySHA256 ||
			!observed.WorkInventoryEmpty || observed.BackupDirectory != expected.BackupDirectory ||
			observed.BackupManifestSHA256 != expected.BackupManifestSHA256 || observed.RequiredBytes != expected.RequiredBytes ||
			!reflect.DeepEqual(observed.LegacyRollback, expected.LegacyRollback) {
			return laneinstall.ErrCatalogOperationInvalid
		}
		evidence, marshalErr := json.Marshal(report)
		if marshalErr != nil {
			return laneinstall.ErrCatalogOperationInvalid
		}
		digest := sha256.Sum256(evidence)
		receipt, err = laneinstall.AdvanceCatalogCohortReceipt(receipt, "preflight", *subjectHost, hex.EncodeToString(digest[:]), time.Now().UTC())
	} else if *action == "supersede-host" {
		successor, readErr := laneinstall.ReadCatalogCohortReceipt(plan, *successorOperationID)
		if readErr != nil {
			return readErr
		}
		observation, observeErr := laneinstall.ObserveCatalogCohortSupersession(context.Background(), plan, receipt, successor)
		if observeErr != nil {
			return observeErr
		}
		receipt, err = laneinstall.AdvanceCatalogCohortSupersedeHost(receipt, successor, observation, time.Now().UTC())
	} else if *action == "supersession-restart" {
		previousID := ""
		for _, transition := range receipt.Transitions {
			if transition.Action == "supersession-restart" {
				previousID = transition.SuccessorID
			} else if transition.Action == "supersede-host" && transition.Supersession != nil {
				previousID = transition.Supersession.SuccessorOperationID
			}
		}
		previous, previousErr := laneinstall.ReadCatalogCohortReceipt(plan, previousID)
		if previousErr != nil {
			return previousErr
		}
		next, nextErr := laneinstall.ReadCatalogCohortReceipt(plan, *successorOperationID)
		if nextErr != nil {
			return nextErr
		}
		receipt, err = laneinstall.AdvanceCatalogCohortSupersessionRestart(receipt, previous, next, time.Now().UTC())
	} else if *action == "supersede" {
		successor, readErr := laneinstall.ReadCatalogCohortReceipt(plan, *successorOperationID)
		if readErr != nil || successor.Stage != laneinstall.CatalogCohortHeld {
			return laneinstall.ErrCatalogOperationInvalid
		}
		receipt, err = laneinstall.AdvanceCatalogCohortSupersede(receipt, *successorOperationID, time.Now().UTC())
	} else {
		if *action == "rollback-start" {
			active, activeErr := catalogCohortLocalServiceActive(ctx, plan)
			if activeErr != nil || active {
				return errors.Join(laneinstall.ErrSimpleLaneNotQuiescent, activeErr)
			}
		}
		receipt, err = laneinstall.AdvanceCatalogCohortReceipt(receipt, *action, *subjectHost, *evidenceSHA, time.Now().UTC())
	}
	if err != nil {
		return err
	}
	path, err := laneinstall.WriteCatalogCohortReceipt(plan, receipt)
	if err != nil {
		return err
	}
	return writeJSON(stdout, struct {
		OperationID string `json:"operation_id"`
		Path        string `json:"local_receipt_path"`
		Stage       string `json:"stage"`
		Transitions int    `json:"transition_count"`
	}{receipt.OperationID, path, string(receipt.Stage), len(receipt.Transitions)})
}

// runCatalogCohortRenewal maintains separate, bounded selected-lane
// continuation evidence. It never rewrites a cohort receipt or starts a service.
func runCatalogCohortRenewal(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("runner-lane catalog-cohort-renewal", flag.ContinueOnError)
	flags.SetOutput(stderr)
	host := flags.String("host", "", "canonical identity of this existing host")
	laneValue := flags.String("lane", "", "selected lane dev|qas|prd")
	action := flags.String("action", "", "observe, issue, sync, or verify")
	operationID := flags.String("operation-id", "", "existing selected-lane cohort operation identity")
	outputFile := flags.String("output-file", "", "private-directory output path for observe")
	renewalFile := flags.String("renewal-file", "", "canonical renewal evidence file for sync")
	var observationFiles repeatedStringFlag
	flags.Var(&observationFiles, "observation-file", "canonical host observation for issue; repeat once per host")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || !currentCatalogHost(*host) || !validCLIProbeOperationID(*operationID) {
		return errors.New("usage: runner-lane catalog-cohort-renewal --host HOST --lane dev|qas|prd --action observe|issue|sync|verify --operation-id ID")
	}
	var lane config.ReleaseLane
	switch *laneValue {
	case "dev":
		lane = config.ReleaseLaneDev
	case "qas":
		lane = config.ReleaseLaneQAS
	case "prd":
		lane = config.ReleaseLanePRD
	default:
		return errors.New("--lane must be dev, qas, or prd")
	}
	plan, err := laneinstall.BuildCatalogPlan(*host, lane)
	if err != nil {
		return err
	}
	switch *action {
	case "observe":
		if *outputFile == "" || *renewalFile != "" || len(observationFiles) != 0 {
			return errors.New("observe requires --output-file and accepts no input files")
		}
		observation, err := laneinstall.ObserveCatalogCohortContinuationHost(ctx, plan, *operationID)
		if err != nil {
			return err
		}
		if err := laneinstall.WriteCatalogCohortContinuationHostObservationFile(*outputFile, observation); err != nil {
			return err
		}
		return writeJSON(stdout, struct {
			HostID      string `json:"host_id"`
			OperationID string `json:"operation_id"`
			ObservedAt  string `json:"observed_at"`
			OutputFile  string `json:"output_file"`
		}{observation.HostID, observation.OperationID, observation.ObservedAt, *outputFile})
	case "issue":
		if *host != "mac-control-plane" || *outputFile != "" || *renewalFile != "" || len(observationFiles) != len(catalogCLIHostOrder) {
			return errors.New("issue requires the Mac coordinator and exactly four --observation-file inputs")
		}
		receipt, err := laneinstall.ReadCatalogCohortReceipt(plan, *operationID)
		if err != nil {
			return err
		}
		observations := make([]laneinstall.CatalogCohortContinuationHostObservation, 0, len(observationFiles))
		for _, path := range observationFiles {
			observation, loadErr := laneinstall.LoadCatalogCohortContinuationHostObservationFile(path)
			if loadErr != nil {
				return loadErr
			}
			observations = append(observations, observation)
		}
		local, err := laneinstall.ObserveCatalogCohortContinuationHost(ctx, plan, *operationID)
		if err != nil {
			return err
		}
		macIndex := len(observations) - 1
		if !laneinstall.SameCatalogCohortContinuationHostState(local, observations[macIndex]) {
			return laneinstall.ErrCatalogOperationInvalid
		}
		// Capacity is checked by the fresh local observation, but it can change
		// between the file capture and coordinator invocation. Bind the current
		// reading and timestamp into the renewal.
		observations[macIndex] = local
		renewal, err := laneinstall.NewCatalogCohortContinuationRenewal(receipt, observations, time.Now().UTC())
		if err != nil {
			return err
		}
		path, err := laneinstall.WriteCatalogCohortContinuationRenewal(plan, renewal)
		if err != nil {
			return err
		}
		return writeJSON(stdout, struct {
			OperationID string `json:"operation_id"`
			RenewalID   string `json:"renewal_id"`
			SHA256      string `json:"sha256"`
			VerifiedAt  string `json:"verified_at"`
			ExpiresAt   string `json:"expires_at"`
			Path        string `json:"path"`
		}{renewal.OperationID, renewal.RenewalID, renewal.SHA256, renewal.VerifiedAt, renewal.ExpiresAt, path})
	case "sync":
		if *renewalFile == "" || *outputFile != "" || len(observationFiles) != 0 {
			return errors.New("sync requires --renewal-file and accepts no observation files")
		}
		renewal, err := laneinstall.LoadCatalogCohortContinuationRenewalFile(*renewalFile)
		if err != nil || renewal.OperationID != *operationID {
			return laneinstall.ErrCatalogOperationInvalid
		}
		path, err := laneinstall.SyncCatalogCohortContinuationRenewal(ctx, plan, renewal)
		if err != nil {
			return err
		}
		return writeJSON(stdout, struct {
			HostID      string `json:"host_id"`
			OperationID string `json:"operation_id"`
			SHA256      string `json:"sha256"`
			Path        string `json:"path"`
		}{*host, renewal.OperationID, renewal.SHA256, path})
	case "verify":
		if *renewalFile != "" || *outputFile != "" || len(observationFiles) != 0 {
			return errors.New("verify accepts no input files")
		}
		renewal, err := laneinstall.VerifyCatalogCohortContinuationRenewal(ctx, plan, *operationID)
		if err != nil {
			return err
		}
		return writeJSON(stdout, struct {
			HostID      string `json:"host_id"`
			OperationID string `json:"operation_id"`
			RenewalID   string `json:"renewal_id"`
			SHA256      string `json:"sha256"`
			VerifiedAt  string `json:"verified_at"`
			ExpiresAt   string `json:"expires_at"`
		}{*host, renewal.OperationID, renewal.RenewalID, renewal.SHA256, renewal.VerifiedAt, renewal.ExpiresAt})
	default:
		return errors.New("action must be observe, issue, sync, or verify")
	}
}

type repeatedStringFlag []string

func (f *repeatedStringFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

func (f *repeatedStringFlag) String() string { return strings.Join(*f, ",") }

// runCatalogProbePrepare creates a one-time capability for the fixed harmless
// five-second worker probe. It emits the exact harmless request body and
// prints the bearer token once for the already-authenticated direct mTLS call.
func runCatalogProbePrepare(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("runner-lane catalog-probe-prepare", flag.ContinueOnError)
	flags.SetOutput(stderr)
	host := flags.String("host", "", "canonical existing host identity")
	lane := flags.String("lane", "", "dev, qas, or prd")
	transport := flags.String("transport", string(laneinstall.CatalogProbeDirectMTLS), "direct-mtls, queued-ssh, or mac-local")
	operationID := flags.String("operation-id", "", "held cohort operation identity")
	controllerID := flags.String("controller-id", "", "existing authenticated controller identity")
	ttlSeconds := flags.Int("ttl-seconds", 120, "one-time probe token lifetime from 1 to 600 seconds")
	observeExisting := flags.Bool("observe-existing", false, "read the exact existing probe scope without minting a capability")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || !currentCatalogHost(*host) ||
		(*lane != "dev" && *lane != "qas" && *lane != "prd") || !validCLIProbeOperationID(*operationID) ||
		*controllerID == "" || *ttlSeconds < 1 || *ttlSeconds > 600 {
		return errors.New("usage: runner-lane catalog-probe-prepare --host HOST --lane dev|qas|prd --transport direct-mtls|queued-ssh|mac-local --operation-id ID --controller-id ID [--ttl-seconds 1..600]")
	}
	probeTransport := laneinstall.CatalogProbeTransport(*transport)
	var controllerType domain.ControllerType
	switch probeTransport {
	case laneinstall.CatalogProbeDirectMTLS:
		controllerType = domain.ControllerTypeDirectMTLS
	case laneinstall.CatalogProbeQueuedSSH:
		controllerType = domain.ControllerTypeQueuedMac
	case laneinstall.CatalogProbeMacLocal:
		controllerType = domain.ControllerTypeLocalUser
	default:
		return errors.New("unsupported catalog maintenance probe transport")
	}
	plan, err := laneinstall.BuildCatalogPlan(*host, config.ReleaseLane(*lane))
	if err != nil {
		return err
	}
	loaded, err := config.LoadFile(plan.Namespace.ConfigFile)
	if err != nil {
		return errors.New("host lane configuration could not be validated")
	}
	binding, err := laneinstall.ResolveCatalogMaintenanceProbeBinding(loaded, plan, probeTransport, controllerType, *controllerID)
	if err != nil {
		return err
	}
	idempotencyKey := "catalog-probe-" + *operationID + "-" + *host
	if probeTransport != laneinstall.CatalogProbeDirectMTLS {
		idempotencyKey += "-" + string(probeTransport)
	}
	requestBody, err := laneinstall.CatalogMaintenanceProbeRequestBodyForBinding(plan, probeTransport, binding)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(requestBody)
	if *observeExisting {
		observation, err := observeCatalogProbeScope(plan, *operationID, probeTransport, controllerType, *controllerID,
			binding.Environment, binding.ConfigurationSHA, idempotencyKey, hex.EncodeToString(digest[:]))
		if err != nil {
			return err
		}
		return writeJSON(stdout, observation)
	}
	expiresAt := time.Now().UTC().Add(time.Duration(*ttlSeconds) * time.Second)
	token, err := laneinstall.PrepareCatalogMaintenanceProbeForTransportWithConfig(plan, *operationID, probeTransport, controllerType, *controllerID, idempotencyKey, requestBody, expiresAt, loaded)
	if err != nil {
		return err
	}
	return writeJSON(stdout, struct {
		HostID              string                            `json:"host_id"`
		Lane                string                            `json:"lane"`
		Transport           laneinstall.CatalogProbeTransport `json:"transport"`
		OperationID         string                            `json:"operation_id"`
		ControllerType      domain.ControllerType             `json:"controller_type"`
		ControllerID        string                            `json:"controller_id"`
		Environment         string                            `json:"environment"`
		ConfigurationSHA256 string                            `json:"configuration_sha256"`
		IdempotencyKey      string                            `json:"idempotency_key"`
		RequestSHA256       string                            `json:"request_body_sha256"`
		RequestBody         json.RawMessage                   `json:"request_body"`
		ExpiresAt           string                            `json:"expires_at"`
		Token               string                            `json:"one_time_token"`
		State               string                            `json:"state"`
		JobID               string                            `json:"job_id,omitempty"`
		IntentID            string                            `json:"intent_id,omitempty"`
	}{*host, *lane, probeTransport, *operationID, controllerType, *controllerID, binding.Environment, binding.ConfigurationSHA,
		idempotencyKey, hex.EncodeToString(digest[:]), requestBody, expiresAt.Format(time.RFC3339Nano), token, "ready", "", ""})
}

type catalogProbeScopeObservation struct {
	HostID              string                            `json:"host_id"`
	Lane                string                            `json:"lane"`
	Transport           laneinstall.CatalogProbeTransport `json:"transport"`
	OperationID         string                            `json:"operation_id"`
	ControllerType      domain.ControllerType             `json:"controller_type"`
	ControllerID        string                            `json:"controller_id"`
	Environment         string                            `json:"environment"`
	ConfigurationSHA256 string                            `json:"configuration_sha256"`
	IdempotencyKey      string                            `json:"idempotency_key"`
	RequestSHA256       string                            `json:"request_body_sha256"`
	CreatedAt           string                            `json:"created_at"`
	ExpiresAt           string                            `json:"expires_at"`
	State               string                            `json:"state"`
	JobID               string                            `json:"job_id,omitempty"`
	IntentID            string                            `json:"intent_id,omitempty"`
}

type catalogProbeScopeRecord struct {
	FormatVersion     int                               `json:"format_version"`
	OperationID       string                            `json:"operation_id"`
	HostID            string                            `json:"host_id"`
	Lane              string                            `json:"lane"`
	Transport         laneinstall.CatalogProbeTransport `json:"transport"`
	ControllerType    domain.ControllerType             `json:"controller_type"`
	ControllerID      string                            `json:"controller_id"`
	Environment       string                            `json:"environment"`
	ConfigurationSHA  string                            `json:"configuration_sha256"`
	CreatedAt         string                            `json:"created_at"`
	ExpiresAt         string                            `json:"expires_at"`
	TokenSHA256       string                            `json:"token_sha256"`
	IdempotencyKey    string                            `json:"idempotency_key"`
	RequestBodySHA256 string                            `json:"request_body_sha256"`
	State             string                            `json:"state"`
	JobID             string                            `json:"job_id,omitempty"`
	IntentID          string                            `json:"intent_id,omitempty"`
}

// observeCatalogProbeScope is a read-only recovery path for a controller that
// lost its bounded result after the host consumed the one-time capability. It
// validates the complete immutable binding before returning job identity and
// never emits the token digest or changes scope state.
func observeCatalogProbeScope(plan laneinstall.Plan, operationID string, transport laneinstall.CatalogProbeTransport,
	controllerType domain.ControllerType, controllerID, environment, configurationSHA, idempotencyKey, requestSHA string) (catalogProbeScopeObservation, error) {
	if len(operationID) != 32 || strings.Trim(operationID, "0123456789abcdef") != "" ||
		!validCatalogProbeHostID(plan.HostID) || (plan.Lane != config.ReleaseLaneDev && plan.Lane != config.ReleaseLaneQAS && plan.Lane != config.ReleaseLanePRD) ||
		transport == "" || environment == "" || len(configurationSHA) != 64 || len(requestSHA) != 64 || idempotencyKey == "" {
		return catalogProbeScopeObservation{}, errors.New("catalog probe observation binding is invalid")
	}
	root := filepath.Join(plan.Namespace.Staging, "catalog-probes")
	rootInfo, rootErr := os.Lstat(root)
	if errors.Is(rootErr, os.ErrNotExist) {
		return catalogProbeScopeObservation{HostID: plan.HostID, Lane: string(plan.Lane), Transport: transport,
			OperationID: operationID, ControllerType: controllerType, ControllerID: controllerID, Environment: environment,
			ConfigurationSHA256: configurationSHA, IdempotencyKey: idempotencyKey, RequestSHA256: requestSHA, State: "absent"}, nil
	}
	var rootStat unix.Stat_t
	if rootErr != nil || rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() || rootInfo.Mode().Perm() != 0o700 ||
		unix.Lstat(root, &rootStat) != nil || rootStat.Uid != uint32(os.Getuid()) {
		return catalogProbeScopeObservation{}, errors.New("catalog probe scope directory is not private and owner-controlled")
	}
	name := operationID + ".json"
	if transport != laneinstall.CatalogProbeDirectMTLS {
		name = operationID + "-" + string(transport) + ".json"
	}
	path := filepath.Join(root, name)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return catalogProbeScopeObservation{HostID: plan.HostID, Lane: string(plan.Lane), Transport: transport,
			OperationID: operationID, ControllerType: controllerType, ControllerID: controllerID, Environment: environment,
			ConfigurationSHA256: configurationSHA, IdempotencyKey: idempotencyKey, RequestSHA256: requestSHA, State: "absent"}, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > 1<<16 {
		return catalogProbeScopeObservation{}, errors.New("catalog probe scope is not a private regular file")
	}
	var stat unix.Stat_t
	if unix.Lstat(path, &stat) != nil || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 {
		return catalogProbeScopeObservation{}, errors.New("catalog probe scope ownership or link count is invalid")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return catalogProbeScopeObservation{}, errors.New("catalog probe scope could not be read")
	}
	var scope catalogProbeScopeRecord
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&scope) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return catalogProbeScopeObservation{}, errors.New("catalog probe scope JSON is invalid")
	}
	canonical, err := json.MarshalIndent(scope, "", "  ")
	if err != nil || !bytes.Equal(data, append(canonical, '\n')) || scope.FormatVersion != 3 ||
		scope.OperationID != operationID || scope.HostID != plan.HostID || scope.Lane != string(plan.Lane) || scope.Transport != transport ||
		scope.ControllerType != controllerType || scope.ControllerID != controllerID || scope.Environment != environment ||
		scope.ConfigurationSHA != configurationSHA || scope.IdempotencyKey != idempotencyKey || scope.RequestBodySHA256 != requestSHA ||
		!validProbeScopeDigest(scope.TokenSHA256) || !validProbeScopeTime(scope.CreatedAt, scope.ExpiresAt) ||
		(scope.State != "ready" && scope.State != "reserved" && scope.State != "accepted") ||
		(scope.State == "ready" && (scope.JobID != "" || scope.IntentID != "")) ||
		(scope.State == "accepted" && !validProbeScopeJobID(scope.JobID)) ||
		(scope.JobID != "" && !validProbeScopeJobID(scope.JobID)) ||
		(scope.IntentID != "" && (transport != laneinstall.CatalogProbeMacLocal || scope.State != "reserved" && scope.State != "accepted")) {
		return catalogProbeScopeObservation{}, errors.New("catalog probe scope does not match the requested immutable binding")
	}
	return catalogProbeScopeObservation{HostID: scope.HostID, Lane: scope.Lane, Transport: scope.Transport, OperationID: scope.OperationID,
		ControllerType: scope.ControllerType, ControllerID: scope.ControllerID, Environment: scope.Environment,
		ConfigurationSHA256: scope.ConfigurationSHA, IdempotencyKey: scope.IdempotencyKey, RequestSHA256: scope.RequestBodySHA256,
		CreatedAt: scope.CreatedAt, ExpiresAt: scope.ExpiresAt, State: scope.State, JobID: scope.JobID, IntentID: scope.IntentID}, nil
}

func validProbeScopeDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validProbeScopeTime(createdText, expiresText string) bool {
	created, err1 := time.Parse(time.RFC3339Nano, createdText)
	expires, err2 := time.Parse(time.RFC3339Nano, expiresText)
	return err1 == nil && err2 == nil && expires.After(created) && expires.Sub(created) <= 10*time.Minute
}

func validProbeScopeJobID(value string) bool {
	return len(value) == 36 && strings.HasPrefix(value, "job-") && strings.Trim(value[4:], "0123456789abcdef") == ""
}

func validCatalogProbeHostID(value string) bool {
	for _, host := range catalogCLIHostOrder {
		if value == host {
			return true
		}
	}
	return false
}

func validCLIProbeOperationID(value string) bool {
	return len(value) == 32 && strings.Trim(value, "0123456789abcdef") == ""
}

func runCatalogSelectorMaintenance(ctx context.Context, action string, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("runner-lane "+action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	host := flags.String("host", "", "canonical existing host ID")
	lane := flags.String("lane", "", "dev, qas, or prd")
	releaseID := flags.String("release-id", "", "sealed catalog release label for catalog-select")
	expectedTarget := flags.String("expected-target", "", "exact existing selector target for catalog-select")
	cohortOperationID := flags.String("cohort-operation-id", "", "held four-host cohort receipt identity")
	selectorOperationID := flags.String("selector-operation-id", "", "host-local selector journal identity for reconcile or rollback")
	evidenceSHA := flags.String("evidence-sha256", "", "bounded operator switch evidence digest")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if catalogCommandSubprocessFixtureBackendForTests != nil {
		return catalogCommandSubprocessFixtureBackendForTests.RunSelector(ctx, action, catalogSelectorCommand{
			HostID: *host, Lane: *lane, ReleaseID: *releaseID,
			ExpectedTarget: *expectedTarget, CohortOperationID: *cohortOperationID,
			SelectorOperation: *selectorOperationID, EvidenceSHA256: *evidenceSHA,
		}, stdout, stderr)
	}
	if !currentCatalogHost(*host) || (*lane != "dev" && *lane != "qas" && *lane != "prd") {
		return errors.New("usage: runner-lane catalog-select|catalog-reconcile|catalog-rollback --host HOST --lane dev|qas|prd --cohort-operation-id ID [operation-specific flags]")
	}
	if action == "catalog-select" {
		if *releaseID == "" || *cohortOperationID == "" || (*lane != "prd" && *expectedTarget == "") || *evidenceSHA == "" || *selectorOperationID != "" {
			return errors.New("catalog-select requires release, expected target, cohort operation, and evidence digest")
		}
	} else if *selectorOperationID == "" || *cohortOperationID == "" || *releaseID != "" || *expectedTarget != "" || *evidenceSHA != "" {
		return errors.New("catalog-reconcile and catalog-rollback require --selector-operation-id and --cohort-operation-id")
	}
	plan, err := laneinstall.BuildCatalogPlan(*host, config.ReleaseLane(*lane))
	if err != nil {
		return err
	}
	if action != "catalog-select" {
		recorded, err := laneinstall.InspectCatalogSelectorOperationForPlan(plan, *selectorOperationID)
		if err != nil || recorded.CohortOperationID != *cohortOperationID {
			return laneinstall.ErrCatalogOperationInvalid
		}
	}
	if *lane == "prd" {
		prdPlan, err := laneinstall.BuildPRDUpgradePlan(*host)
		if err != nil {
			return err
		}
		controller, err := laneinstall.NewPRDServiceController()
		if err != nil {
			return err
		}
		maintenance, err := laneinstall.BeginCatalogPRDMaintenance(ctx, prdPlan, controller, *cohortOperationID)
		if err != nil {
			return err
		}
		defer maintenance.Close()
		var operation laneinstall.CatalogSelectorOperation
		switch action {
		case "catalog-select":
			operation, err = runCatalogSelectorSelect(func() (laneinstall.CatalogSelectorOperation, error) {
				receipt, readErr := laneinstall.ReadCatalogCohortReceipt(plan, *cohortOperationID)
				if readErr != nil {
					return laneinstall.CatalogSelectorOperation{}, readErr
				}
				var backup laneinstall.CatalogCohortHostPreflight
				for _, hostPreflight := range receipt.Hosts {
					if hostPreflight.HostID == *host {
						backup = hostPreflight
						break
					}
				}
				if backup.HostID == "" {
					return laneinstall.CatalogSelectorOperation{}, laneinstall.ErrCatalogOperationInvalid
				}
				if err := maintenance.UseVerifiedBackup(ctx, backup.BackupDirectory, backup.BackupManifestSHA256); err != nil {
					return laneinstall.CatalogSelectorOperation{}, err
				}
				return maintenance.Select(ctx, *releaseID, *expectedTarget, *cohortOperationID, *evidenceSHA)
			})
		case "catalog-reconcile":
			operation, err = maintenance.Reconcile(ctx, *selectorOperationID)
		case "catalog-rollback":
			operation, err = maintenance.Rollback(ctx, *selectorOperationID)
		}
		if err != nil {
			return err
		}
		return writeJSON(stdout, operation)
	}
	controller, err := laneinstall.NewSimpleServiceController()
	if err != nil {
		return err
	}
	maintenance, err := laneinstall.BeginCatalogLaneMaintenance(ctx, plan, controller)
	if err != nil {
		return err
	}
	defer maintenance.Close()
	var operation laneinstall.CatalogSelectorOperation
	switch action {
	case "catalog-select":
		operation, err = runCatalogSelectorSelect(func() (laneinstall.CatalogSelectorOperation, error) {
			receipt, readErr := laneinstall.ReadCatalogCohortReceipt(plan, *cohortOperationID)
			if readErr != nil {
				return laneinstall.CatalogSelectorOperation{}, readErr
			}
			var backup laneinstall.CatalogCohortHostPreflight
			for _, hostPreflight := range receipt.Hosts {
				if hostPreflight.HostID == *host {
					backup = hostPreflight
					break
				}
			}
			if backup.HostID == "" {
				return laneinstall.CatalogSelectorOperation{}, laneinstall.ErrCatalogOperationInvalid
			}
			if err := maintenance.UseVerifiedBackup(ctx, backup.BackupDirectory, backup.BackupManifestSHA256); err != nil {
				return laneinstall.CatalogSelectorOperation{}, err
			}
			return maintenance.Select(ctx, *releaseID, *expectedTarget, *cohortOperationID, *evidenceSHA)
		})
	case "catalog-reconcile":
		operation, err = maintenance.Reconcile(ctx, *selectorOperationID)
	case "catalog-rollback":
		operation, err = maintenance.Rollback(ctx, *selectorOperationID)
	}
	if err != nil {
		return err
	}
	return writeJSON(stdout, operation)
}

func runCatalogSelectorSelect(selectOperation func() (laneinstall.CatalogSelectorOperation, error)) (laneinstall.CatalogSelectorOperation, error) {
	if selectOperation == nil {
		return laneinstall.CatalogSelectorOperation{}, laneinstall.ErrCatalogOperationInvalid
	}
	operation, err := selectOperation()
	if err != nil {
		return laneinstall.CatalogSelectorOperation{}, err
	}
	if operation.OperationID == "" || operation.HostID == "" || operation.NextTarget == "" || operation.BackupManifestSHA256 == "" {
		return laneinstall.CatalogSelectorOperation{}, laneinstall.ErrCatalogOperationInvalid
	}
	return operation, nil
}

func runCatalogPRDBackup(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("runner-lane catalog-prd-backup", flag.ContinueOnError)
	flags.SetOutput(stderr)
	host := flags.String("host", "", "canonical existing host ID")
	releaseID := flags.String("release-id", "", "sealed catalog release label to bind the backup")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *releaseID == "" || !currentCatalogHost(*host) {
		return errors.New("usage: runner-lane catalog-prd-backup --host HOST --release-id RELEASE")
	}
	plan, err := laneinstall.BuildPRDUpgradePlan(*host)
	if err != nil {
		return err
	}
	root, err := laneinstall.DefaultCatalogRootForPlan(laneinstall.Plan{HostID: *host, Lane: config.ReleaseLanePRD, Namespace: plan.Namespace})
	if err != nil {
		return err
	}
	target, err := laneinstall.InspectCatalogRelease(root, *host, *releaseID)
	if err != nil {
		return err
	}
	receipt, err := laneinstall.CreatePRDCatalogBackup(ctx, plan, target.Identity.BundleSHA256)
	if err != nil {
		return err
	}
	availableBytes, requiredBytes, err := laneinstall.ReadPRDCatalogSelectorCapacity(plan)
	if err != nil {
		return err
	}
	return writeJSON(stdout, struct {
		HostID               string `json:"host_id"`
		Lane                 string `json:"lane"`
		ReleaseID            string `json:"release_id"`
		ReleaseSHA256        string `json:"release_sha256"`
		BundleSHA256         string `json:"bundle_sha256"`
		Directory            string `json:"backup_directory"`
		ManifestSHA256       string `json:"backup_manifest_sha256"`
		SchemaVersion        int    `json:"schema_version"`
		DatabaseBackupSHA256 string `json:"database_backup_sha256"`
		AvailableBytes       uint64 `json:"available_bytes_after_backup"`
		RequiredBytes        uint64 `json:"required_selector_headroom_bytes"`
	}{*host, "prd", *releaseID, target.Identity.ReleaseSHA256, target.Identity.BundleSHA256, receipt.Directory,
		laneinstall.PRDBackupManifestSHA256(receipt.Directory), receipt.Manifest.SchemaVersion, receipt.Manifest.DatabaseSHA256,
		availableBytes, requiredBytes})
}

// runCatalogPRDRecover performs exact retained-pair recovery with the
// host-native executable from a sealed catalog release. It does not build,
// install, start, or select a service binary. The service must already be
// stopped and a fresh consistent backup must be supplied.
func runCatalogPRDRecover(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("runner-lane catalog-prd-recover", flag.ContinueOnError)
	flags.SetOutput(stderr)
	host := flags.String("host", "", "canonical existing host ID")
	releaseID := flags.String("release-id", "", "Dev-accepted sealed catalog release ID")
	releaseSHA := flags.String("release-sha256", "", "exact sealed release manifest SHA-256")
	bundleSHA := flags.String("bundle-sha256", "", "exact host bundle SHA-256")
	selectedReleaseID := flags.String("expected-selected-release-id", "", "exact current selected catalog release ID; omit only for an unselected legacy selector")
	selectedReleaseSHA := flags.String("expected-selected-release-sha256", "", "exact current selected catalog release manifest SHA-256")
	selectedBundleSHA := flags.String("expected-selected-bundle-sha256", "", "exact current selected host bundle SHA-256")
	backupDir := flags.String("backup-dir", "", "fresh consistent pre-recovery backup directory")
	backupSHA := flags.String("backup-manifest-sha256", "", "exact backup manifest SHA-256")
	var lostPairs repeatedStringFlag
	flags.Var(&lostPairs, "lost-pair", "exact terminal-lost SESSION_ID:COMMAND_ID pair; repeat for the complete host set")
	if err := flags.Parse(args); err != nil {
		return err
	}
	_, knownHost := laneinstall.CatalogPRDRecoveryCommandComponent(*host)
	if flags.NArg() != 0 || *releaseID == "" || *releaseSHA == "" || *bundleSHA == "" ||
		*backupDir == "" || *backupSHA == "" || len(lostPairs) == 0 || !knownHost {
		return errors.New("usage: runner-lane catalog-prd-recover --host HOST --release-id RELEASE --release-sha256 SHA256 --bundle-sha256 SHA256 [--expected-selected-release-id ID --expected-selected-release-sha256 SHA256 --expected-selected-bundle-sha256 SHA256] --backup-dir DIR --backup-manifest-sha256 SHA256 --lost-pair SESSION_ID:COMMAND_ID [...]")
	}
	request := laneinstall.CatalogPRDRecoveryRequest{
		ReleaseID: *releaseID, ExpectedReleaseSHA256: *releaseSHA, ExpectedBundleSHA256: *bundleSHA,
		ExpectedSelectedReleaseID: *selectedReleaseID, ExpectedSelectedReleaseSHA256: *selectedReleaseSHA,
		ExpectedSelectedBundleSHA256: *selectedBundleSHA,
		BackupDirectory:              *backupDir, ExpectedBackupManifestSHA: *backupSHA, LostPairs: append([]string(nil), lostPairs...),
	}
	if err := laneinstall.ValidateCatalogPRDRecoverySelectedRelease(request); err != nil {
		return err
	}
	if catalogCommandSubprocessFixtureBackendForTests != nil {
		return catalogCommandSubprocessFixtureBackendForTests.RunPRDRecovery(ctx, catalogPRDRecoveryCommand{
			HostID: *host, ReleaseID: request.ReleaseID, ExpectedReleaseSHA256: request.ExpectedReleaseSHA256,
			ExpectedBundleSHA256:          request.ExpectedBundleSHA256,
			ExpectedSelectedReleaseID:     request.ExpectedSelectedReleaseID,
			ExpectedSelectedReleaseSHA256: request.ExpectedSelectedReleaseSHA256,
			ExpectedSelectedBundleSHA256:  request.ExpectedSelectedBundleSHA256,
			BackupDirectory:               request.BackupDirectory,
			ExpectedBackupManifestSHA:     request.ExpectedBackupManifestSHA, LostPairs: request.LostPairs,
		}, stdout, stderr)
	}
	if !currentCatalogHost(*host) {
		return errors.New("catalog-prd-recover must run on the selected canonical host")
	}
	plan, err := laneinstall.BuildPRDUpgradePlan(*host)
	if err != nil {
		return err
	}
	service, err := laneinstall.NewPRDServiceController()
	if err != nil {
		return err
	}
	report, err := laneinstall.RecoverCatalogPRDLostPairs(ctx, plan, service, request, nil)
	if err != nil {
		return err
	}
	return writeJSON(stdout, report)
}

// runCatalogCompatibilityCheck validates a typed legacy rollback proof against
// the current host's installed PRD binaries, configuration, and schema. It
// calls only the read-only PRD preflight path; unlike catalog-host-preflight,
// it creates no backup and does not stop or change the service.
func runCatalogCompatibilityCheck(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("runner-lane catalog-compatibility-check", flag.ContinueOnError)
	flags.SetOutput(stderr)
	host := flags.String("host", "", "canonical identity of this existing host")
	compatibilityFile := flags.String("legacy-compatibility-file", "", "owner-only tested legacy rollback compatibility report")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || !currentCatalogHost(*host) || *compatibilityFile == "" {
		return errors.New("usage: runner-lane catalog-compatibility-check --host THIS-HOST --legacy-compatibility-file FILE")
	}
	compatibility, err := laneinstall.LoadCatalogLegacyRollbackCompatibilityFile(*compatibilityFile)
	if err != nil {
		return err
	}
	plan, err := laneinstall.BuildPRDUpgradePlan(*host)
	if err != nil {
		return err
	}
	service, err := laneinstall.NewPRDServiceController()
	if err != nil {
		return err
	}
	actual, err := laneinstall.ReadPRDPreflight(ctx, plan, service)
	if err != nil {
		return err
	}
	if actual.LegacyBundleSHA256 == "" || len(actual.MissingComponents) != 0 ||
		laneinstall.ValidateCatalogLegacyRollbackCompatibility(compatibility, actual.LegacyBundleSHA256,
			actual.ConfigSHA256, actual.SchemaVersion) != nil {
		return laneinstall.ErrCatalogRollbackUnsafe
	}
	return writeJSON(stdout, struct {
		HostID             string `json:"host_id"`
		Mode               string `json:"validation_mode"`
		LegacyBundleSHA256 string `json:"legacy_bundle_sha256"`
		ConfigurationSHA   string `json:"configuration_sha256"`
		DatabaseSchema     int    `json:"database_schema"`
		ServiceActive      bool   `json:"service_active"`
		WorkInventorySHA   string `json:"work_inventory_sha256"`
	}{*host, "read_only_no_backup", actual.LegacyBundleSHA256, actual.ConfigSHA256, actual.SchemaVersion,
		actual.ServiceActive, actual.Work.SHA256()})
}

// runCatalogHostPreflight emits host-local receipt evidence. Dev/QAS use their
// established stop-and-backup path. PRD takes an online consistent backup and
// a read-only inventory first; its legacy service is stopped later by the
// ordered, receipt-recorded fence-host action, avoiding the bootstrap cycle.
func runCatalogHostPreflight(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("runner-lane catalog-host-preflight", flag.ContinueOnError)
	flags.SetOutput(stderr)
	host := flags.String("host", "", "canonical identity of this existing host")
	lane := flags.String("lane", "", "dev, qas, or prd")
	releaseID := flags.String("release-id", "", "sealed catalog release to preflight")
	requiredBytes := flags.Uint64("required-free-bytes", 0, "remaining free space required after the consistent backup")
	legacyCompatibilityFile := flags.String("legacy-compatibility-file", "", "owner-only tested legacy rollback compatibility report (PRD legacy only)")
	replacementPredecessor := flags.String("replacement-predecessor", "", "exact fully switched verifying PRD operation whose selected release is the rollback baseline")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || !currentCatalogHost(*host) || (*lane != "dev" && *lane != "qas" && *lane != "prd") ||
		*releaseID == "" || *requiredBytes == 0 {
		return errors.New("usage: runner-lane catalog-host-preflight --host HOST --lane dev|qas|prd --release-id RELEASE --required-free-bytes BYTES [--legacy-compatibility-file FILE] [--replacement-predecessor OPERATION]")
	}
	if *lane == "prd" {
		if *replacementPredecessor != "" && (*legacyCompatibilityFile != "" || !validCLIProbeOperationID(*replacementPredecessor)) {
			return errors.New("replacement preflight requires one exact operation ID and forbids legacy compatibility input")
		}
		var compatibility *laneinstall.CatalogLegacyRollbackCompatibility
		if *legacyCompatibilityFile != "" {
			loaded, loadErr := laneinstall.LoadCatalogLegacyRollbackCompatibilityFile(*legacyCompatibilityFile)
			if loadErr != nil {
				return loadErr
			}
			compatibility = &loaded
		}
		prdPlan, err := laneinstall.BuildPRDUpgradePlan(*host)
		if err != nil {
			return err
		}
		service, err := laneinstall.NewPRDServiceController()
		if err != nil {
			return err
		}
		var report laneinstall.CatalogCohortHostPreflightReport
		if *replacementPredecessor == "" {
			report, err = laneinstall.ReadPRDCatalogCohortHostPreflight(ctx, prdPlan, service, *releaseID, *requiredBytes, compatibility)
		} else {
			report, err = laneinstall.ReadPRDCatalogReplacementHostPreflight(ctx, prdPlan, service, *releaseID, *requiredBytes, *replacementPredecessor)
		}
		if err != nil {
			return err
		}
		return writeJSON(stdout, report)
	}
	if *legacyCompatibilityFile != "" || *replacementPredecessor != "" {
		return errors.New("legacy compatibility and replacement predecessor inputs are valid only for PRD preflight")
	}
	plan, err := laneinstall.BuildCatalogPlan(*host, config.ReleaseLane(*lane))
	if err != nil {
		return err
	}
	root, err := laneinstall.DefaultCatalogRootForPlan(plan)
	if err != nil {
		return err
	}
	if _, err := laneinstall.InspectCatalogRelease(root, *host, *releaseID); err != nil {
		return err
	}
	controller, err := laneinstall.NewSimpleServiceController()
	if err != nil {
		return err
	}
	maintenance, err := stopAndBeginCatalogLaneMaintenance(ctx, plan, controller, func() (*laneinstall.CatalogLaneMaintenance, error) {
		return laneinstall.BeginCatalogLaneMaintenance(ctx, plan, controller)
	})
	if err != nil {
		return err
	}
	defer maintenance.Close()
	if _, err := maintenance.Backup(ctx); err != nil {
		return err
	}
	preflight, err := maintenance.CohortHostPreflight(ctx, *releaseID, *requiredBytes)
	if err != nil {
		return err
	}
	return writeJSON(stdout, preflight)
}

type catalogHostMaintenanceService interface {
	laneinstall.SimpleServiceProbe
	Stop(context.Context, laneinstall.Plan) error
}

func stopAndBeginCatalogLaneMaintenance(ctx context.Context, plan laneinstall.Plan, service catalogHostMaintenanceService,
	begin func() (*laneinstall.CatalogLaneMaintenance, error)) (*laneinstall.CatalogLaneMaintenance, error) {
	if service == nil || begin == nil {
		return nil, laneinstall.ErrSimpleLanePreflight
	}
	if err := service.Stop(ctx, plan); err != nil {
		return nil, fmt.Errorf("stop selected lane before catalog preflight: %w", err)
	}
	return begin()
}

func runCatalogWorkInventory(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("runner-lane catalog-work-inventory", flag.ContinueOnError)
	flags.SetOutput(stderr)
	host := flags.String("host", "", "canonical identity of this existing host")
	lane := flags.String("lane", "", "dev, qas, or prd")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || !validCatalogWorkInventoryScope(*host, *lane) {
		return errors.New("usage: runner-lane catalog-work-inventory --host HOST --lane dev|qas|prd")
	}
	if *lane == "prd" {
		plan, err := laneinstall.BuildPRDUpgradePlan(*host)
		if err != nil {
			return err
		}
		controller, err := laneinstall.NewPRDServiceController()
		if err != nil {
			return err
		}
		inventory, err := laneinstall.ReadCatalogPRDWorkInventory(ctx, plan, controller)
		if err != nil {
			return err
		}
		return writeJSON(stdout, inventory)
	}
	plan, err := laneinstall.BuildCatalogPlan(*host, config.ReleaseLane(*lane))
	if err != nil {
		return err
	}
	controller, err := laneinstall.NewSimpleServiceController()
	if err != nil {
		return err
	}
	inventory, err := laneinstall.ReadCatalogWorkInventory(ctx, plan, controller)
	if err != nil {
		return err
	}
	return writeJSON(stdout, inventory)
}

func validCatalogWorkInventoryScope(host, lane string) bool {
	return currentCatalogHost(host) && supportedCatalogWorkInventoryLane(lane)
}

func supportedCatalogWorkInventoryLane(lane string) bool {
	return lane == "dev" || lane == "qas" || lane == "prd"
}

func runCatalogPreSwitchCheck(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("runner-lane catalog-pre-switch-check", flag.ContinueOnError)
	flags.SetOutput(stderr)
	host := flags.String("host", "", "canonical identity of this existing host")
	lane := flags.String("lane", "", "dev or qas")
	operationID := flags.String("cohort-operation-id", "", "exact held cohort operation expected on this host")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || !currentCatalogHost(*host) || (*lane != "dev" && *lane != "qas") || !validCLIProbeOperationID(*operationID) {
		return errors.New("usage: runner-lane catalog-pre-switch-check --host HOST --lane dev|qas --cohort-operation-id ID")
	}
	plan, err := laneinstall.BuildCatalogPlan(*host, config.ReleaseLane(*lane))
	if err != nil {
		return err
	}
	controller, err := laneinstall.NewSimpleServiceController()
	if err != nil {
		return err
	}
	inventory, err := laneinstall.ReadCatalogWorkInventory(ctx, plan, controller)
	if err != nil {
		return err
	}
	if inventory.ServiceActive || !inventory.WorkInventoryEmpty {
		return laneinstall.ErrSimpleLaneNotQuiescent
	}
	if err := laneinstall.CatalogCohortAdmissionHoldMatches(plan, *operationID); err != nil {
		return err
	}
	receipt, err := laneinstall.ReadCatalogCohortReceipt(plan, *operationID)
	if err != nil || receipt.Stage != laneinstall.CatalogCohortHeld || len(receipt.Transitions) == 0 ||
		countCatalogReceiptSwitches(receipt) != 0 {
		return laneinstall.ErrCatalogOperationInvalid
	}
	return writeJSON(stdout, struct {
		HostID             string                         `json:"host_id"`
		Lane               config.ReleaseLane             `json:"lane"`
		OperationID        string                         `json:"cohort_operation_id"`
		HoldConfirmed      bool                           `json:"admission_hold_confirmed"`
		ReceiptStage       laneinstall.CatalogCohortStage `json:"receipt_stage"`
		ObservedAt         string                         `json:"observed_at"`
		ServiceActive      bool                           `json:"service_active"`
		WorkInventory      any                            `json:"work_inventory"`
		WorkInventoryEmpty bool                           `json:"work_inventory_empty"`
	}{*host, config.ReleaseLane(*lane), *operationID, true, receipt.Stage, inventory.ObservedAt,
		inventory.ServiceActive, inventory.WorkInventory, inventory.WorkInventoryEmpty})
}

func countCatalogReceiptSwitches(receipt laneinstall.CatalogCohortReceipt) int {
	count := 0
	for _, transition := range receipt.Transitions {
		if transition.Action == "switch" || transition.Action == "switch-reconciled" {
			count++
		}
	}
	return count
}

func currentCatalogHost(hostID string) bool {
	if !laneinstall.CatalogHostMatchesPlatform(hostID, runtime.GOOS, runtime.GOARCH) {
		return false
	}
	account := config.LinuxAccount
	if hostID == "mac-control-plane" {
		account = config.MacAccount
	}
	current, err := user.Current()
	if err != nil || current.Username != account {
		return false
	}
	prd, err := laneinstall.BuildPRDUpgradePlan(hostID)
	if err != nil {
		return false
	}
	loaded, err := config.LoadFile(prd.Namespace.ConfigFile)
	if err != nil {
		return false
	}
	configured, _, err := laneinstall.CatalogPlanFromConfiguration(loaded)
	return err == nil && configured.HostID == hostID && configured.Lane == config.ReleaseLanePRD
}

func catalogCohortLocalServiceActive(ctx context.Context, plan laneinstall.Plan) (bool, error) {
	if plan.Lane == config.ReleaseLanePRD {
		prdPlan, err := laneinstall.BuildPRDUpgradePlan(plan.HostID)
		if err != nil {
			return false, err
		}
		controller, err := laneinstall.NewPRDServiceController()
		if err != nil {
			return false, err
		}
		return controller.Status(ctx, prdPlan)
	}
	controller, err := laneinstall.NewSimpleServiceController()
	if err != nil {
		return false, err
	}
	return controller.Status(ctx, plan)
}

func verifyLocalCatalogCohortTarget(plan laneinstall.Plan, receipt laneinstall.CatalogCohortReceipt) error {
	active, err := catalogCohortLocalServiceActive(context.Background(), plan)
	if err != nil || !active {
		return errors.New("selected lane service must be active for cohort verification")
	}
	root, err := laneinstall.DefaultCatalogRootForPlan(plan)
	if err != nil {
		return err
	}
	status, err := laneinstall.InspectCatalogSelectorForPlan(plan, root)
	if err != nil || status.Kind != "catalog" || status.Release == nil ||
		status.Release.Identity.ReleaseID != receipt.ReleaseID || status.Release.Identity.ReleaseSHA256 != receipt.ReleaseSHA256 {
		return errors.New("local selected release does not match the held cohort identity")
	}
	for _, host := range receipt.Hosts {
		if host.HostID == plan.HostID && status.Release.Identity.BundleSHA256 == host.Target.BundleSHA256 {
			return nil
		}
	}
	return errors.New("local selected host bundle does not match the held cohort identity")
}

func verifyLocalCatalogCohortRollbackTarget(plan laneinstall.Plan, receipt laneinstall.CatalogCohortReceipt) error {
	active, err := catalogCohortLocalServiceActive(context.Background(), plan)
	if err != nil || !active {
		return errors.New("selected lane service must be active before rollback admissions are reopened")
	}
	root, err := laneinstall.DefaultCatalogRootForPlan(plan)
	if err != nil {
		return err
	}
	status, err := laneinstall.InspectCatalogSelectorForPlan(plan, root)
	if err != nil {
		return errors.New("rollback target selector is unavailable")
	}
	for _, host := range receipt.Hosts {
		if host.HostID != plan.HostID {
			continue
		}
		matched := false
		var selectedID, selectedSHA string
		if status.Release != nil {
			selectedID = status.Release.Identity.ReleaseID
			selectedSHA = status.Release.Identity.BundleSHA256
		} else {
			selectedSHA = status.Bundle.BundleSHA256
		}
		if strings.HasPrefix(host.PreviousTarget, "releases/") {
			matched = selectedID == filepath.Base(filepath.Dir(host.PreviousTarget))
		} else if filepath.IsAbs(host.PreviousTarget) {
			matched = status.Kind == "legacy" && status.Bundle.BundleSHA256 == host.PreviousBundleSHA256
		}
		if !matched || selectedSHA != host.PreviousBundleSHA256 {
			return errors.New("local selected release does not match the recorded rollback predecessor")
		}
		active, activeErr := catalogCohortLocalServiceActive(context.Background(), plan)
		if activeErr != nil || !active {
			return errors.New("selected lane service must be active before rollback admissions are reopened")
		}
		if plan.Lane == config.ReleaseLanePRD {
			prdPlan, planErr := laneinstall.BuildPRDUpgradePlan(plan.HostID)
			if planErr != nil {
				return planErr
			}
			service, serviceErr := laneinstall.NewPRDServiceController()
			if serviceErr != nil {
				return serviceErr
			}
			preflight, preflightErr := laneinstall.ReadPRDPreflight(context.Background(), prdPlan, service)
			if preflightErr != nil || !preflight.Work.Empty() || preflight.ConfigSHA256 != host.ConfigurationSHA256 || preflight.SchemaVersion != host.SchemaVersion {
				return errors.Join(laneinstall.ErrSimpleLaneNotQuiescent, preflightErr)
			}
		} else {
			service, serviceErr := laneinstall.NewSimpleServiceController()
			if serviceErr != nil {
				return serviceErr
			}
			inventory, inventoryErr := laneinstall.ReadCatalogWorkInventory(context.Background(), plan, service)
			if inventoryErr != nil || !inventory.WorkInventoryEmpty || inventory.ConfigurationSHA256 != host.ConfigurationSHA256 || inventory.DatabaseSchema != host.SchemaVersion {
				return errors.Join(laneinstall.ErrSimpleLaneNotQuiescent, inventoryErr)
			}
		}
		return nil
	}
	return errors.New("local host is absent from rollback cohort receipt")
}

func localCatalogRoot(hostID string) (string, error) {
	if !currentCatalogHost(hostID) {
		return "", errors.New("release catalog host does not match the current platform and account")
	}
	home := filepath.Join("/home", config.LinuxAccount)
	if hostID == "mac-control-plane" {
		home = filepath.Join("/Users", config.MacAccount)
	}
	return filepath.Join(home, "projects-runtime", "runner-releases"), nil
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
	sourceRevision := flags.String("source-revision", "", "optional frozen source commit in a clean detached checkout; later origin/dev changes must be Markdown-only")
	output := flags.String("output-dir", "", "absolute bundle directory outside the source checkout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *output == "" {
		return errors.New("usage: runner-lane bundle-build --host HOST --lane dev|qas --repo CHECKOUT --output-dir ABSOLUTE-OUTSIDE-CHECKOUT [--go TOOLCHAIN] [--source-revision FROZEN-SHA]")
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
	var bundle laneinstall.SimpleBundle
	if *sourceRevision == "" {
		bundle, err = laneinstall.BuildSimpleBundle(ctx, plan, repositoryRoot, *goBinary)
	} else {
		bundle, err = laneinstall.BuildSimpleBundleAtRevision(ctx, plan, repositoryRoot, *goBinary, *sourceRevision)
	}
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

func runPRDPreflight(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("runner-lane prd-preflight", flag.ContinueOnError)
	flags.SetOutput(stderr)
	host := flags.String("host", "", "canonical existing host ID")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	plan, err := laneinstall.BuildPRDUpgradePlan(*host)
	if err != nil {
		return err
	}
	controller, err := laneinstall.NewPRDServiceController()
	if err != nil {
		return err
	}
	preflight, err := laneinstall.ReadPRDPreflight(ctx, plan, controller)
	if err != nil {
		return err
	}
	return writeJSON(stdout, preflight)
}

func runPRDService(ctx context.Context, action string, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("runner-lane "+action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	host := flags.String("host", "", "canonical existing host ID")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	plan, err := laneinstall.BuildPRDUpgradePlan(*host)
	if err != nil {
		return err
	}
	controller, err := laneinstall.NewPRDServiceController()
	if err != nil {
		return err
	}
	if action == "prd-status" {
		active, err := controller.Status(ctx, plan)
		if err != nil {
			return err
		}
		return writeJSON(stdout, struct {
			HostID string `json:"host_id"`
			Lane   string `json:"lane"`
			Active bool   `json:"active"`
		}{plan.HostID, "prd", active})
	}
	if action == "prd-stop" {
		err = controller.Stop(ctx, plan)
	} else {
		err = controller.Start(ctx, plan)
	}
	if err != nil {
		return err
	}
	return writeJSON(stdout, struct {
		HostID string `json:"host_id"`
		Lane   string `json:"lane"`
		Action string `json:"action"`
	}{plan.HostID, "prd", strings.TrimPrefix(action, "prd-")})
}

func runPRDBackup(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("runner-lane prd-backup", flag.ContinueOnError)
	flags.SetOutput(stderr)
	host := flags.String("host", "", "canonical existing host ID")
	bundleSHA := flags.String("bundle-sha256", "", "exact accepted host bundle SHA-256")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *bundleSHA == "" {
		return errors.New("usage: runner-lane prd-backup --host HOST --bundle-sha256 SHA256")
	}
	plan, err := laneinstall.BuildPRDUpgradePlan(*host)
	if err != nil {
		return err
	}
	receipt, err := laneinstall.CreatePRDBackup(ctx, plan, *bundleSHA)
	if err != nil {
		return err
	}
	return writeJSON(stdout, receipt)
}

func runPRDInstall(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("runner-lane prd-install", flag.ContinueOnError)
	flags.SetOutput(stderr)
	host := flags.String("host", "", "canonical existing host ID")
	bundleDir := flags.String("bundle-dir", "", "exact accepted per-host candidate bundle")
	bundleSHA := flags.String("bundle-sha256", "", "exact accepted host bundle SHA-256")
	backupDir := flags.String("backup-dir", "", "verified owner-only PRD backup under the host's canonical backup root")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *bundleDir == "" || *bundleSHA == "" || *backupDir == "" {
		return errors.New("usage: runner-lane prd-install --host HOST --bundle-dir CANDIDATE --bundle-sha256 SHA256 --backup-dir BACKUP")
	}
	plan, err := laneinstall.BuildPRDUpgradePlan(*host)
	if err != nil {
		return err
	}
	bundle, err := laneinstall.LoadSimpleBundleDirectory(*bundleDir)
	if err != nil {
		return err
	}
	controller, err := laneinstall.NewPRDServiceController()
	if err != nil {
		return err
	}
	installed, err := laneinstall.InstallPRDBundle(ctx, plan, controller, bundle, *bundleSHA, *backupDir)
	if err != nil {
		return err
	}
	return writeJSON(stdout, struct {
		HostID         string                      `json:"host_id"`
		Lane           string                      `json:"lane"`
		SourceRevision string                      `json:"source_revision"`
		BundleSHA256   string                      `json:"bundle_sha256"`
		Components     []laneinstall.PRDBackupFile `json:"installed_components"`
	}{plan.HostID, "prd", bundle.Manifest.SourceRevision, bundle.Manifest.BundleSHA256, installed})
}

func runPRDMigrate(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("runner-lane prd-migrate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	host := flags.String("host", "", "canonical existing host ID")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	plan, err := laneinstall.BuildPRDUpgradePlan(*host)
	if err != nil {
		return err
	}
	controller, err := laneinstall.NewPRDServiceController()
	if err != nil {
		return err
	}
	schema, inventory, err := laneinstall.MigratePRDDatabase(ctx, plan, controller)
	if err != nil {
		return err
	}
	return writeJSON(stdout, struct {
		HostID    string                       `json:"host_id"`
		Lane      string                       `json:"lane"`
		Schema    int                          `json:"schema_version"`
		Inventory laneinstall.PRDWorkInventory `json:"post_migration_work_inventory"`
	}{plan.HostID, "prd", schema, inventory})
}

func runPRDRollback(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("runner-lane prd-rollback", flag.ContinueOnError)
	flags.SetOutput(stderr)
	host := flags.String("host", "", "canonical existing host ID")
	backupDir := flags.String("backup-dir", "", "verified owner-only PRD backup under the host's canonical backup root")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *backupDir == "" {
		return errors.New("usage: runner-lane prd-rollback --host HOST --backup-dir BACKUP")
	}
	plan, err := laneinstall.BuildPRDUpgradePlan(*host)
	if err != nil {
		return err
	}
	controller, err := laneinstall.NewPRDServiceController()
	if err != nil {
		return err
	}
	if err := laneinstall.RollbackPRD(ctx, plan, controller, *backupDir); err != nil {
		return err
	}
	return writeJSON(stdout, struct {
		HostID string `json:"host_id"`
		Lane   string `json:"lane"`
		Result string `json:"result"`
	}{plan.HostID, "prd", "original binaries and exact pre-migration database restored; service remains stopped"})
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
