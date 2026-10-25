package releaseflow

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
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Test-only seams are package variables, never CLI flags or environment
// switches. Production build tools always use the validated native backend.
var buildBackendForTests BuildBackend
var promotionBackendForTests Backend

type buildCLIEnvironment struct {
	Repository         string
	ControllerRevision string
	Resolver           GitResolver
}

// buildCLIEnvironmentForTests is assigned only by a test binary. It avoids
// changing the canonical checkout or contacting real Git remotes in public
// command subprocess fixtures; production has no flag or environment switch.
var buildCLIEnvironmentForTests *buildCLIEnvironment

func RunBuildCLI(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help") {
		fmt.Fprintln(stdout, "Usage: build-release (--branch NAME | --commit SHA | --branch NAME --commit SHA) [--source-test-receipt FILE]... [--release-id ID] [--resume BUILD-ID] [--plan] [--json]")
		fmt.Fprintln(stdout, "Freezes one published source revision, builds and seals four native host bundles, then publishes without selecting or restarting a service.")
		fmt.Fprintln(stdout, "Source and mirror refs must agree. With --branch and --commit, SHA must be reachable from the captured branch tip.")
		fmt.Fprintln(stdout, "A build requires four owner-only native-full-v1 receipts for the frozen commit; each must match that host's actual bundle platform and toolchain.")
		fmt.Fprintln(stdout, "--resume reuses the frozen build identity and completed artifact bytes; it does not re-resolve source or rebuild completed hosts.")
		return 0
	}
	flags := flag.NewFlagSet("build-release", flag.ContinueOnError)
	flags.SetOutput(stderr)
	branch := flags.String("branch", "", "published primary branch to capture")
	commit := flags.String("commit", "", "full published commit SHA")
	var qualificationReceiptPaths stringListFlag
	flags.Var(&qualificationReceiptPaths, "source-test-receipt", "owner-only native full-suite receipt; repeat once per host")
	releaseID := flags.String("release-id", "", "optional immutable label")
	resume := flags.String("resume", "", "frozen build ID to resume")
	plan := flags.Bool("plan", false, "resolve source and show planned effects without building")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	hostsFile := flags.String("hosts-file", "", "owner-only host control configuration")
	storeRoot := flags.String("store-root", "", "owner-only workflow record root")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || (*resume != "" && (*branch != "" || *commit != "")) || (*resume == "" && *branch == "" && *commit == "") {
		fmt.Fprintln(stderr, "usage: build-release (--branch NAME | --commit SHA | --branch NAME --commit SHA) [--source-test-receipt FILE]... [--release-id ID] [--resume BUILD-ID] [--plan] [--json]")
		return 2
	}
	repo, err := gitOutput("rev-parse", "--show-toplevel")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	repo = strings.TrimSpace(repo)
	head, err := gitOutput("-C", repo, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	controller := strings.TrimSpace(head)
	resolver := GitResolver{Repository: repo, Primary: "origin", Mirror: "github", TempRoot: os.TempDir()}
	if buildCLIEnvironmentForTests != nil {
		if !absoluteClean(buildCLIEnvironmentForTests.Repository) || !shaPattern.MatchString(buildCLIEnvironmentForTests.ControllerRevision) {
			fmt.Fprintln(stderr, ErrInvalidInput)
			return 1
		}
		repo = buildCLIEnvironmentForTests.Repository
		controller = buildCLIEnvironmentForTests.ControllerRevision
		resolver = buildCLIEnvironmentForTests.Resolver
		resolver.Repository = repo
	}
	if *hostsFile == "" {
		*hostsFile = defaultHostConfigPath()
	}
	cfg := HostConfiguration{}
	if !*plan {
		cfg, err = LoadHostConfiguration(*hostsFile)
		if err != nil {
			fmt.Fprintf(stderr, "load host configuration: %v\n", err)
			return 1
		}
	}
	storePath := *storeRoot
	if storePath == "" {
		storePath = defaultStorePath()
	}
	store := &Store{Root: storePath}
	if !*plan {
		store, err = NewStore(storePath)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	sshRoot := filepath.Join(mustHome(), ".ssh")
	if buildCLIEnvironmentForTests == nil {
		resolver.PrimarySSHCommand = fmt.Sprintf("ssh -i %s -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes", filepath.Join(sshRoot, "local-gitea-key"))
		resolver.MirrorSSHCommand = fmt.Sprintf("ssh -i %s -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes", filepath.Join(sshRoot, "gavrilo123456-github"))
	}
	backend := buildBackendForTests
	if backend == nil {
		backend = &NativeBuildBackend{Config: cfg, Repository: repo, StoreRoot: storePath, ControllerRevision: controller, GitSSHCommand: resolver.PrimarySSHCommand}
	}
	receipts := make([]SourceTestReceipt, 0, len(qualificationReceiptPaths))
	if !*plan {
		for _, path := range qualificationReceiptPaths {
			receipt, _, loadErr := LoadSourceTestReceiptFile(path)
			if loadErr != nil {
				fmt.Fprintf(stderr, "load source-test receipt %s: %v\n", path, loadErr)
				return 1
			}
			receipts = append(receipts, receipt)
		}
	}
	result, err := Build(context.Background(), store, resolver, backend, BuildRequest{Branch: *branch, Commit: *commit, ReleaseID: *releaseID, ResumeID: *resume, PlanOnly: *plan, QualificationReceipts: receipts}, controller, time.Now)
	if err != nil {
		if result.Record.BuildID != "" && *jsonOutput {
			_ = json.NewEncoder(stdout).Encode(result)
		} else {
			fmt.Fprintf(stderr, "build-release failed: %v\n", err)
		}
		return 1
	}
	if *jsonOutput {
		if err := json.NewEncoder(stdout).Encode(result); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	} else if *plan {
		fmt.Fprintf(stdout, "source plan: branch=%s captured_tip=%s commit=%s; no bundles built or published\n", result.Record.Source.Branch, result.Record.Source.CapturedTip, result.Record.Source.Commit)
	} else {
		fmt.Fprintf(stdout, "release ready without activation: build=%s release=%s sha256=%s source=%s\n", result.Record.BuildID, result.Release.ID, result.Release.Digest, result.Release.Source.Commit)
		for _, host := range HostOrder {
			b := result.Release.Bundles[host]
			fmt.Fprintf(stdout, "  %s %s/%s bundle=%s bytes=%d\n", host, b.GOOS, b.GOARCH, b.SHA256, b.Bytes)
		}
	}
	return 0
}

func RunPromoteCLI(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help") {
		fmt.Fprintln(stdout, "Usage: promote-to dev|qas|prd HOST|all --release ID [--plan | --prepare-only] [--resume OP-ID] [--approval-file FILE] [--json]")
		fmt.Fprintln(stdout, "A named host advances only that next selector step; initial common preparation holds, drains, observes, and backs up all four hosts in that lane.")
		fmt.Fprintln(stdout, "Switch order: linux-host → sandbox-host → factory-sandbox → mac-control-plane. Promotion never rebuilds a sealed release.")
		fmt.Fprintln(stdout, "QAS requires Dev acceptance for the same release digest and bundle hashes. PRD requires QAS acceptance and a current owner approval bound to the plan.")
		fmt.Fprintln(stdout, "--plan is read-only. Execution requires explicit publisher owner/control and paused/no-new-accepted confirmations.")
		fmt.Fprintln(stdout, "--prepare-only performs common preparation and leaves the synchronized cohort hold active without switching selectors; resume the same operation after supported receipt reconciliation.")
		return 0
	}
	if len(args) < 2 {
		fmt.Fprintln(stderr, "usage: promote-to dev|qas|prd HOST|all --release ID [--plan | --prepare-only] [--resume OP-ID] [--approval-file FILE] [--json]")
		return 2
	}
	lane, target := args[0], args[1]
	flags := flag.NewFlagSet("promote-to", flag.ContinueOnError)
	flags.SetOutput(stderr)
	releaseID := flags.String("release", "", "sealed release ID")
	resume := flags.String("resume", "", "existing operation ID")
	approvalFile := flags.String("approval-file", "", "release-bound PRD approval JSON")
	hostsFile := flags.String("hosts-file", "", "owner-only host control configuration")
	publisherOwner := flags.String("publisher-owner", "", "owner of the selected lane's publisher pause")
	publisherControl := flags.String("publisher-control", "", "specific control keeping selected-lane publishers paused")
	publishersPaused := flags.Bool("publishers-paused", false, "confirm selected-lane publishers are actually paused")
	noNewAccepted := flags.Bool("no-new-accepted", false, "confirm no new work is accepted by the selected lane")
	operatingBuffer := flags.Uint64("operating-buffer-bytes", defaultPromotionOperatingBuffer, "required post-backup free-space buffer; PRD also preserves the 64 MiB selector reserve")
	planOnly := flags.Bool("plan", false, "read-only promotion plan")
	prepareOnly := flags.Bool("prepare-only", false, "prepare and hold the full cohort, then stop before selector changes")
	profile := flags.String("profile", "catalog-functional-v1", "release-bound acceptance profile")
	opID := flags.String("operation-id", "", "optional new operation ID")
	storeRoot := flags.String("store-root", "", "workflow record root")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args[2:]); err != nil {
		return 2
	}
	if flags.NArg() != 0 || !contains(LaneOrder, lane) || (target != "all" && !contains(HostOrder, target)) {
		fmt.Fprintln(stderr, "invalid lane or host; valid lanes dev|qas|prd and hosts linux-host|sandbox-host|factory-sandbox|mac-control-plane|all")
		return 2
	}
	if *planOnly && *prepareOnly {
		fmt.Fprintln(stderr, "--plan and --prepare-only are mutually exclusive")
		return 2
	}
	if *storeRoot == "" {
		*storeRoot = defaultStorePath()
	}
	if *hostsFile == "" {
		*hostsFile = defaultHostConfigPath()
	}
	var err error
	store := &Store{Root: *storeRoot}
	if !*planOnly {
		store, err = NewStore(*storeRoot)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	if *resume != "" {
		op, err := store.LoadOperation(lane, *resume)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		releaseIDValue := op.Release.ID
		*releaseID = releaseIDValue
	}
	if !releasePattern.MatchString(*releaseID) {
		fmt.Fprintln(stderr, "--release is required for a new operation")
		return 2
	}
	release, err := store.LoadRelease(*releaseID)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	var approval Approval
	approvalSHA := ""
	if lane == "prd" && *approvalFile != "" {
		approval, approvalSHA, err = readApprovalFile(*approvalFile)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := ValidateApproval(approval, release, time.Now()); err != nil {
			fmt.Fprintln(stderr, err)
			return 3
		}
	}
	plan, err := BuildPlan(lane, target, release)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if lane == "qas" || lane == "prd" {
		plan.RequiredProof = append(plan.RequiredProof, "digest-bound acceptance receipt for the preceding lane and this exact release")
	}
	backend := promotionBackendForTests
	if backend == nil {
		cfg, loadErr := LoadHostConfiguration(*hostsFile)
		if loadErr != nil {
			fmt.Fprintf(stderr, "load host configuration: %v\n", loadErr)
			return 1
		}
		repo, repoErr := gitOutput("rev-parse", "--show-toplevel")
		if repoErr != nil {
			fmt.Fprintln(stderr, repoErr)
			return 1
		}
		repo = strings.TrimSpace(repo)
		head, headErr := gitOutput("-C", repo, "rev-parse", "--verify", "HEAD^{commit}")
		if headErr != nil {
			fmt.Fprintln(stderr, headErr)
			return 1
		}
		sshRoot := filepath.Join(mustHome(), ".ssh")
		gitSSH := fmt.Sprintf("ssh -i %s -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes", filepath.Join(sshRoot, "local-gitea-key"))
		mirrorSSH := fmt.Sprintf("ssh -i %s -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes", filepath.Join(sshRoot, "gavrilo123456-github"))
		native := &NativePromotionBackend{Config: cfg, Repository: repo, ControllerRevision: strings.TrimSpace(head), StoreRoot: *storeRoot,
			GitSSHCommand: gitSSH, MirrorSSHCommand: mirrorSSH, PublisherOwner: *publisherOwner, PublisherControl: *publisherControl,
			PublishersPaused: *publishersPaused, NoNewAccepted: *noNewAccepted, OperatingBuffer: *operatingBuffer}
		if lane == "prd" && *approvalFile != "" {
			native.Approval = &approval
		}
		backend = native
	}
	if native, ok := backend.(*NativePromotionBackend); ok && lane == "prd" && approvalSHA != "" {
		native.Approval = &approval
	}
	plan, err = backend.Plan(context.Background(), lane, release, approvalSHA)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	plan.Target = target
	if lane == "prd" && approvalSHA != "" {
		if !sameStringMap(approval.PredecessorBundleSHA256, plan.PredecessorBundleSHA256) || approval.BackupRecoveryPackageSHA256 != plan.BackupRecoveryPackageSHA {
			fmt.Fprintln(stderr, "PRD approval predecessor or backup/recovery package does not match the read-only host plan")
			return 3
		}
	}
	if *planOnly {
		result := PromoteResult{Plan: plan, Outcome: "planned_no_effects", Next: "confirm the listed publisher controls and approval, then run promote-to without --plan"}
		return writeCLIResult(stdout, stderr, result, *jsonOutput)
	}
	if lane == "prd" && approvalSHA == "" {
		fmt.Fprintln(stderr, ErrApprovalRequired)
		return 3
	}
	if *publisherOwner == "" || *publisherControl == "" || !*publishersPaused || !*noNewAccepted {
		fmt.Fprintln(stderr, "promotion execution requires explicit --publisher-owner, --publisher-control, --publishers-paused, and --no-new-accepted")
		return 3
	}
	if *resume == "" && *opID == "" {
		id, err := newID()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		*opID = id
	}
	result, err := Promote(context.Background(), store, backend, PromoteRequest{Lane: lane, Target: target, ReleaseID: *releaseID, ResumeID: *resume, Approval: approvalSHA, PrepareOnly: *prepareOnly, Profile: *profile, OperationID: *opID}, time.Now)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return writeCLIResult(stdout, stderr, result, *jsonOutput)
}

func sameStringMap(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range right {
		if left[key] != value {
			return false
		}
	}
	return true
}

func RunAdminCLI(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: runner-release status --build ID | --lane LANE --operation ID | rollback --lane LANE --operation ID")
		return 2
	}
	switch args[0] {
	case "status":
		flags := flag.NewFlagSet("runner-release status", flag.ContinueOnError)
		flags.SetOutput(stderr)
		buildID := flags.String("build", "", "build ID")
		releaseID := flags.String("release", "", "release ID")
		acceptanceLane := flags.String("acceptance-lane", "", "dev, qas, or prd")
		lane := flags.String("lane", "", "lane name")
		operation := flags.String("operation", "", "operation ID")
		root := flags.String("store-root", "", "workflow store")
		jsonOutput := flags.Bool("json", false, "JSON output")
		if err := flags.Parse(args[1:]); err != nil {
			return 2
		}
		if *root == "" {
			*root = defaultStorePath()
		}
		s := &Store{Root: *root}
		if *buildID != "" && *lane == "" && *operation == "" {
			record, err := s.LoadBuild(*buildID)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			return writeCLIResult(stdout, stderr, record, *jsonOutput)
		}
		if *releaseID != "" && *acceptanceLane == "" && *lane == "" && *operation == "" {
			release, err := s.LoadRelease(*releaseID)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			provenance, err := s.LoadReleaseProvenance(*releaseID)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			return writeCLIResult(stdout, stderr, struct {
				Release    Release    `json:"release"`
				Provenance Provenance `json:"provenance"`
			}{Release: release, Provenance: provenance}, *jsonOutput)
		}
		if *releaseID != "" && contains(LaneOrder, *acceptanceLane) && *lane == "" && *operation == "" {
			acceptance, err := s.LoadAcceptance(*acceptanceLane, *releaseID)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			return writeCLIResult(stdout, stderr, acceptance, *jsonOutput)
		}
		if *operation != "" && contains(LaneOrder, *lane) {
			op, err := s.LoadOperation(*lane, *operation)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			return writeCLIResult(stdout, stderr, op, *jsonOutput)
		}
		fmt.Fprintln(stderr, "status requires --build BUILD-ID, --release ID, --acceptance-lane LANE --release ID, or --lane LANE --operation OP-ID")
		return 2
	case "branch-sync":
		return runBranchSyncCLI(args[1:], stdout, stderr)
	case "rollback":
		return runRollbackCLI(args[1:], stdout, stderr)
	case "help", "--help", "-h":
		fmt.Fprintln(stdout, "Usage:\n  runner-release status --build ID | --release ID | --acceptance-lane LANE --release ID | --lane LANE --operation ID [--json]\n  runner-release branch-sync --lane qas|prd --operation ID [--json]\n  runner-release rollback --lane LANE --operation ID [--approval-file FILE] --publisher-owner OWNER --publisher-control CONTROL --publishers-paused --no-new-accepted [--json]")
		fmt.Fprintln(stdout, "Branch-sync retries only the qas/prd source marker after runtime acceptance; it never redeploys. Rollback uses guarded reverse selector journals and does not restore databases by default.")
		return 0
	default:
		fmt.Fprintf(stderr, "unknown runner-release command %q\n", args[0])
		return 2
	}
}

func runBranchSyncCLI(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("runner-release branch-sync", flag.ContinueOnError)
	flags.SetOutput(stderr)
	lane := flags.String("lane", "", "qas or prd")
	operationID := flags.String("operation", "", "accepted operation ID")
	root := flags.String("store-root", "", "workflow store")
	hostsFile := flags.String("hosts-file", "", "owner-only host control configuration")
	jsonOutput := flags.Bool("json", false, "JSON output")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || (*lane != "qas" && *lane != "prd") || !operationPattern.MatchString(*operationID) {
		fmt.Fprintln(stderr, "usage: runner-release branch-sync --lane qas|prd --operation OP-ID")
		return 2
	}
	if *root == "" {
		*root = defaultStorePath()
	}
	store, err := NewStore(*root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	op, err := store.LoadOperation(*lane, *operationID)
	if err != nil || op.Stage != StageBranchSyncPending || op.BranchSync != "pending" {
		fmt.Fprintln(stderr, errors.Join(errors.New("operation has no pending branch-only synchronization"), err))
		return 1
	}
	backend := promotionBackendForTests
	if backend == nil {
		native, err := loadNativePromotionBackend(*hostsFile, *root, "", "", false, false, defaultPromotionOperatingBuffer)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		backend = native
	}
	var result PromoteResult
	err = store.WithLaneLock(*lane, func() error {
		fresh, err := store.LoadOperation(*lane, *operationID)
		if err != nil {
			return err
		}
		if fresh.Stage != StageBranchSyncPending || fresh.BranchSync != "pending" {
			return ErrOperationCorrupted
		}
		plan := BuildPlanPlaceholder(fresh)
		return finishBranchSync(context.Background(), store, backend, &fresh, plan, &result, time.Now)
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return writeCLIResult(stdout, stderr, result, *jsonOutput)
}

func runRollbackCLI(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("runner-release rollback", flag.ContinueOnError)
	flags.SetOutput(stderr)
	lane := flags.String("lane", "", "dev, qas, or prd")
	operationID := flags.String("operation", "", "promotion operation ID")
	approvalFile := flags.String("approval-file", "", "same release-bound PRD approval package")
	root := flags.String("store-root", "", "workflow store")
	hostsFile := flags.String("hosts-file", "", "owner-only host control configuration")
	owner := flags.String("publisher-owner", "", "owner of the selected lane publisher pause")
	control := flags.String("publisher-control", "", "specific pause and admission/dispatch control")
	paused := flags.Bool("publishers-paused", false, "confirm affected publishers are paused")
	noNew := flags.Bool("no-new-accepted", false, "confirm no new work is accepted")
	jsonOutput := flags.Bool("json", false, "JSON output")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || !contains(LaneOrder, *lane) || !operationPattern.MatchString(*operationID) {
		fmt.Fprintln(stderr, "usage: runner-release rollback --lane LANE --operation OP-ID [--approval-file FILE] --publisher-owner OWNER --publisher-control CONTROL --publishers-paused --no-new-accepted")
		return 2
	}
	if strings.TrimSpace(*owner) == "" || strings.TrimSpace(*control) == "" || !*paused || !*noNew {
		fmt.Fprintln(stderr, "rollback requires explicit publisher owner/control, paused confirmation, and no-new-accepted confirmation")
		return 3
	}
	if *root == "" {
		*root = defaultStorePath()
	}
	store, err := NewStore(*root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	op, err := store.LoadOperation(*lane, *operationID)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	approvalSHA := ""
	var approval Approval
	if *lane == "prd" {
		if *approvalFile == "" {
			fmt.Fprintln(stderr, ErrApprovalRequired)
			return 3
		}
		approval, approvalSHA, err = readApprovalFile(*approvalFile)
		if err != nil || ValidateApproval(approval, op.Release, time.Now()) != nil || approvalSHA != op.ApprovalSHA {
			fmt.Fprintln(stderr, errors.Join(ErrApprovalRequired, err))
			return 3
		}
		if op.PlanSnapshot == nil || !sameStringMap(approval.PredecessorBundleSHA256, op.PlanSnapshot.PredecessorBundleSHA256) || approval.BackupRecoveryPackageSHA256 != op.PlanSnapshot.BackupRecoveryPackageSHA {
			fmt.Fprintln(stderr, ErrApprovalRequired)
			return 3
		}
	}
	backend := promotionBackendForTests
	if backend == nil {
		native, err := loadNativePromotionBackend(*hostsFile, *root, *owner, *control, *paused, *noNew, defaultPromotionOperatingBuffer)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		backend = native
	}
	updated, err := Rollback(context.Background(), store, backend, *lane, *operationID, approvalSHA, time.Now)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return writeCLIResult(stdout, stderr, updated, *jsonOutput)
}

func BuildPlanPlaceholder(op Operation) PromotionPlan {
	if op.PlanSnapshot != nil {
		return *op.PlanSnapshot
	}
	plan, _ := BuildPlan(op.Lane, op.Target, op.Release)
	return plan
}

func loadNativePromotionBackend(hostsFile, storeRoot, owner, control string, paused, noNew bool, buffer uint64) (*NativePromotionBackend, error) {
	if hostsFile == "" {
		hostsFile = defaultHostConfigPath()
	}
	cfg, err := LoadHostConfiguration(hostsFile)
	if err != nil {
		return nil, err
	}
	repo, err := gitOutput("rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	repo = strings.TrimSpace(repo)
	head, err := gitOutput("-C", repo, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return nil, err
	}
	sshRoot := filepath.Join(mustHome(), ".ssh")
	gitSSH := fmt.Sprintf("ssh -i %s -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes", filepath.Join(sshRoot, "local-gitea-key"))
	mirrorSSH := fmt.Sprintf("ssh -i %s -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes", filepath.Join(sshRoot, "gavrilo123456-github"))
	return &NativePromotionBackend{Config: cfg, Repository: repo, ControllerRevision: strings.TrimSpace(head), StoreRoot: storeRoot,
		GitSSHCommand: gitSSH, MirrorSSHCommand: mirrorSSH, PublisherOwner: owner, PublisherControl: control,
		PublishersPaused: paused, NoNewAccepted: noNew, OperatingBuffer: buffer}, nil
}

func RunHostCLI(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "host helper command required")
		return 2
	}
	switch args[0] {
	case "host-capacity":
		f := flag.NewFlagSet("runner-release host-capacity", flag.ContinueOnError)
		f.SetOutput(stderr)
		host := f.String("host", "", "canonical host being measured")
		lane := f.String("lane", "", "dev, qas, or prd")
		repo := f.String("repository", "", "clean dev controller checkout")
		controller := f.String("controller-revision", "", "pinned tool revision")
		if err := f.Parse(args[1:]); err != nil {
			return 2
		}
		if f.NArg() != 0 {
			fmt.Fprintln(stderr, "host-capacity accepts flags only")
			return 2
		}
		report, err := RunHostCapacity(context.Background(), *host, *lane, *repo, *controller)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return writeCLIResult(stdout, stderr, report, true)
	case "host-lane-rpc":
		if len(args) != 1 {
			fmt.Fprintln(stderr, "host-lane-rpc accepts one JSON request on stdin")
			return 2
		}
		req, err := DecodeHostLaneRPCRequest(os.Stdin)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		out, err := RunHostLaneRPC(context.Background(), req)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if _, err := stdout.Write(out); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	case "host-build":
		f := flag.NewFlagSet("runner-release host-build", flag.ContinueOnError)
		f.SetOutput(stderr)
		host := f.String("host", "", "canonical host")
		repo := f.String("repository", "", "clean dev controller checkout")
		goBin := f.String("go", "", "Go 1.27.1")
		runtimeRoot := f.String("runtime-root", "", "owner-only operation root")
		branch := f.String("branch", "", "published source branch")
		tip := f.String("branch-tip", "", "captured branch tip")
		commit := f.String("commit", "", "frozen source SHA")
		buildID := f.String("build-id", "", "build ID")
		releaseID := f.String("release-id", "", "release label")
		controller := f.String("controller-revision", "", "pinned tool revision")
		gitID := f.String("git-identity-file", "", "explicit Git identity")
		known := f.String("git-known-hosts-file", "", "verified Git known-hosts file")
		if err := f.Parse(args[1:]); err != nil {
			return 2
		}
		req := HostBuildRequest{HostID: *host, Repository: *repo, GoBinary: *goBin, RuntimeRoot: *runtimeRoot, Branch: *branch, BranchTip: *tip, Commit: *commit, BuildID: *buildID, ReleaseID: *releaseID, ControllerRevision: *controller, GitIdentityFile: *gitID, GitKnownHostsFile: *known}
		bundle, err := RunHostBuild(context.Background(), req)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return writeCLIResult(stdout, stderr, bundle, true)
	case "host-verify-bundle":
		f := flag.NewFlagSet("host-verify-bundle", flag.ContinueOnError)
		f.SetOutput(stderr)
		host := f.String("host", "", "")
		dir := f.String("bundle-dir", "", "")
		sha := f.String("bundle-sha256", "", "")
		if err := f.Parse(args[1:]); err != nil {
			return 2
		}
		bundle, err := RunHostVerifyBundle(*host, *dir, *sha)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return writeCLIResult(stdout, stderr, bundle, true)
	case "host-publish":
		f := flag.NewFlagSet("host-publish", flag.ContinueOnError)
		f.SetOutput(stderr)
		host := f.String("host", "", "")
		dir := f.String("bundle-dir", "", "")
		controller := f.String("controller-revision", "", "")
		repo := f.String("repository", "", "")
		if err := f.Parse(args[1:]); err != nil {
			return 2
		}
		identity, err := RunHostPublish(*host, *dir, *controller, os.Stdin, *repo)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return writeCLIResult(stdout, stderr, identity, true)
	case "host-inspect-release":
		f := flag.NewFlagSet("host-inspect-release", flag.ContinueOnError)
		f.SetOutput(stderr)
		host := f.String("host", "", "")
		id := f.String("release-id", "", "")
		sha := f.String("release-sha256", "", "")
		controller := f.String("controller-revision", "", "")
		repo := f.String("repository", "", "")
		if err := f.Parse(args[1:]); err != nil {
			return 2
		}
		if err := verifyControllerCheckout(context.Background(), *repo, *controller); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		status, err := RunHostInspect(*host, *id, *sha)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return writeCLIResult(stdout, stderr, status, true)
	default:
		fmt.Fprintf(stderr, "unknown host helper %q\n", args[0])
		return 2
	}
}

func writeCLIResult(stdout, stderr io.Writer, value any, jsonOutput bool) int {
	if jsonOutput {
		if err := json.NewEncoder(stdout).Encode(value); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	_, err = fmt.Fprintln(stdout, string(encoded))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
func readApprovalFile(path string) (Approval, string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return Approval{}, "", ErrApprovalRequired
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return Approval{}, "", ErrApprovalRequired
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Approval{}, "", err
	}
	var approval Approval
	d := json.NewDecoder(strings.NewReader(string(data)))
	d.DisallowUnknownFields()
	if err := d.Decode(&approval); err != nil {
		return Approval{}, "", ErrApprovalRequired
	}
	sum := sha256.Sum256(data)
	return approval, hex.EncodeToString(sum[:]), nil
}
func gitOutput(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
func mustHome() string { h, _ := os.UserHomeDir(); return h }
func defaultStorePath() string {
	return filepath.Join(mustHome(), "projects-runtime", "runner-release-operations")
}
func defaultHostConfigPath() string {
	return filepath.Join(mustHome(), "Library", "Application Support", "RemoteSessionRunner", "runner-release-hosts.json")
}
