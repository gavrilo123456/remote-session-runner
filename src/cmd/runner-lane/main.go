// Command runner-lane builds and operates Dev/QAS lanes and a separate,
// explicitly gated in-place maintenance path for the canonical unsuffixed
// PRD service on one existing host. It has no arbitrary service interface.
package main

import (
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
	"runtime"
	"strings"
	"syscall"
	"time"

	"remote-session-runner/src/internal/commandstub"
	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/laneinstall"
)

const maxLaneConfigBytes = 1 << 20

func main() {
	args := os.Args[1:]
	if len(args) == 0 || (len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help" || args[0] == "--version" || args[0] == "version")) {
		os.Exit(commandstub.Run("runner-lane", "Build and operate existing-host Dev/QAS lanes; adopt and publish sealed releases; inspect selectors; run guarded unsuffixed PRD maintenance.", args, os.Stdout, os.Stderr))
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
	case "catalog-adopt":
		err = runCatalogAdopt(args[1:], stdout, stderr)
	case "catalog-cohort":
		err = runCatalogCohort(args[1:], stdout, stderr)
	case "catalog-prd-backup":
		err = runCatalogPRDBackup(ctx, args[1:], stdout, stderr)
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

func runCatalogCohort(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("runner-lane catalog-cohort", flag.ContinueOnError)
	flags.SetOutput(stderr)
	host := flags.String("host", "", "canonical identity of this existing host")
	lane := flags.String("lane", "", "dev, qas, or prd")
	action := flags.String("action", "", "prepare, sync, status, hold, preflight, verify-start, verify, complete, block, resume, rollback-start, rollback-complete, or open")
	operationID := flags.String("operation-id", "", "32-character cohort operation identity")
	receiptFile := flags.String("receipt-file", "", "owner-only cohort JSON for prepare or sync")
	subjectHost := flags.String("subject-host", "", "host identity recorded by preflight, switch, verify, or rollback-host")
	evidenceSHA := flags.String("evidence-sha256", "", "digest of the bounded host/operator evidence")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || !currentCatalogHost(*host) {
		return errors.New("usage: runner-lane catalog-cohort --host THIS-HOST --lane dev|qas|prd --action ACTION [--operation-id ID] [--receipt-file FILE] [--subject-host HOST] [--evidence-sha256 SHA256]")
	}
	plan, err := laneinstall.BuildCatalogPlan(*host, config.ReleaseLane(*lane))
	if err != nil {
		return err
	}
	if *action == "prepare" {
		if *receiptFile == "" || *operationID != "" || *subjectHost != "" || *evidenceSHA != "" {
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
		if *receiptFile == "" || *operationID != "" || *subjectHost != "" || *evidenceSHA != "" {
			return errors.New("sync requires only --receipt-file in addition to host and lane")
		}
		incoming, err := laneinstall.LoadCatalogCohortReceiptFile(*receiptFile)
		if err != nil || incoming.Lane != plan.Lane {
			return laneinstall.ErrCatalogOperationInvalid
		}
		path, err := laneinstall.WriteCatalogCohortReceipt(plan, incoming)
		if err != nil {
			return err
		}
		return writeJSON(stdout, struct {
			OperationID string `json:"operation_id"`
			Path        string `json:"local_receipt_path"`
			Stage       string `json:"stage"`
		}{incoming.OperationID, path, string(incoming.Stage)})
	}
	if *operationID == "" || *receiptFile != "" {
		return errors.New("cohort actions require --operation-id and do not accept --receipt-file")
	}
	receipt, err := laneinstall.ReadCatalogCohortReceipt(plan, *operationID)
	if err != nil {
		return err
	}
	if *action == "status" {
		if *subjectHost != "" || *evidenceSHA != "" {
			return errors.New("status does not accept host subjects or evidence digests")
		}
		return writeJSON(stdout, receipt)
	}
	allowed := map[string]struct{}{
		"hold": {}, "preflight": {}, "verify-start": {}, "verify": {}, "complete": {}, "block": {},
		"resume": {}, "rollback-start": {}, "rollback-complete": {}, "open": {},
	}
	if _, ok := allowed[*action]; !ok || *evidenceSHA == "" {
		return errors.New("cohort transition requires a supported --action and --evidence-sha256")
	}
	perHost := *action == "preflight" || *action == "verify"
	if perHost != (*subjectHost != "") {
		return errors.New("preflight and verify require --subject-host; other actions forbid it")
	}
	if perHost && *subjectHost != *host {
		return errors.New("host-scoped cohort evidence must be recorded by that host")
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
		if err := verifyLocalCatalogCohortTarget(plan, receipt); err != nil {
			return err
		}
	}
	receipt, err = laneinstall.AdvanceCatalogCohortReceipt(receipt, *action, *subjectHost, *evidenceSHA, time.Now().UTC())
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

// runCatalogProbePrepare creates a one-time capability for the fixed harmless
// five-second worker probe. It emits the exact harmless request body and
// prints the bearer token once for the already-authenticated direct mTLS call.
func runCatalogProbePrepare(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("runner-lane catalog-probe-prepare", flag.ContinueOnError)
	flags.SetOutput(stderr)
	host := flags.String("host", "", "canonical Linux worker identity")
	lane := flags.String("lane", "", "dev, qas, or prd")
	operationID := flags.String("operation-id", "", "held cohort operation identity")
	controllerID := flags.String("controller-id", "", "existing direct-mTLS controller identity")
	ttlSeconds := flags.Int("ttl-seconds", 120, "one-time probe token lifetime from 1 to 600 seconds")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || !currentCatalogHost(*host) || *host == "mac-control-plane" ||
		(*lane != "dev" && *lane != "qas" && *lane != "prd") || !validCLIProbeOperationID(*operationID) ||
		*controllerID == "" || *ttlSeconds < 1 || *ttlSeconds > 600 {
		return errors.New("usage: runner-lane catalog-probe-prepare --host LINUX-HOST --lane dev|qas|prd --operation-id ID --controller-id ID [--ttl-seconds 1..600]")
	}
	plan, err := laneinstall.BuildCatalogPlan(*host, config.ReleaseLane(*lane))
	if err != nil {
		return err
	}
	idempotencyKey := "catalog-probe-" + *operationID + "-" + *host
	requestBody, err := laneinstall.CatalogMaintenanceProbeRequestBody(plan)
	if err != nil {
		return err
	}
	expiresAt := time.Now().UTC().Add(time.Duration(*ttlSeconds) * time.Second)
	token, err := laneinstall.PrepareCatalogMaintenanceProbe(plan, *operationID, *controllerID, idempotencyKey, requestBody, expiresAt)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(requestBody)
	return writeJSON(stdout, struct {
		HostID         string          `json:"host_id"`
		Lane           string          `json:"lane"`
		OperationID    string          `json:"operation_id"`
		ControllerID   string          `json:"controller_id"`
		IdempotencyKey string          `json:"idempotency_key"`
		RequestSHA256  string          `json:"request_body_sha256"`
		RequestBody    json.RawMessage `json:"request_body"`
		ExpiresAt      string          `json:"expires_at"`
		Token          string          `json:"one_time_token"`
	}{*host, *lane, *operationID, *controllerID, idempotencyKey, hex.EncodeToString(digest[:]), requestBody, expiresAt.Format(time.RFC3339Nano), token})
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
	if flags.NArg() != 0 || !currentCatalogHost(*host) || (*lane != "dev" && *lane != "qas" && *lane != "prd") {
		return errors.New("usage: runner-lane catalog-select|catalog-reconcile|catalog-rollback --host HOST --lane dev|qas|prd --cohort-operation-id ID [operation-specific flags]")
	}
	if action == "catalog-select" {
		if *releaseID == "" || *cohortOperationID == "" || *expectedTarget == "" || *evidenceSHA == "" || *selectorOperationID != "" {
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
			receipt, err := laneinstall.ReadCatalogCohortReceipt(plan, *cohortOperationID)
			if err != nil {
				return err
			}
			var backup laneinstall.CatalogCohortHostPreflight
			for _, hostPreflight := range receipt.Hosts {
				if hostPreflight.HostID == *host {
					backup = hostPreflight
					break
				}
			}
			if backup.HostID == "" {
				return laneinstall.ErrCatalogOperationInvalid
			}
			if err := maintenance.UseVerifiedBackup(ctx, backup.BackupDirectory, backup.BackupManifestSHA256); err != nil {
				return err
			}
			operation, err = maintenance.Select(ctx, *releaseID, *expectedTarget, *cohortOperationID, *evidenceSHA)
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
		receipt, err := laneinstall.ReadCatalogCohortReceipt(plan, *cohortOperationID)
		if err != nil {
			return err
		}
		var backup laneinstall.CatalogCohortHostPreflight
		for _, hostPreflight := range receipt.Hosts {
			if hostPreflight.HostID == *host {
				backup = hostPreflight
				break
			}
		}
		if backup.HostID == "" {
			return laneinstall.ErrCatalogOperationInvalid
		}
		if err := maintenance.UseVerifiedBackup(ctx, backup.BackupDirectory, backup.BackupManifestSHA256); err != nil {
			return err
		}
		operation, err = maintenance.Select(ctx, *releaseID, *expectedTarget, *cohortOperationID, *evidenceSHA)
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
