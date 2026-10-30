package laneinstall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/store"
)

const maxCatalogCohortHistoryBranches = 16

// ReconcileCatalogCohortReceiptHistories creates one conservative, hash-linked
// receipt from divergent valid copies. Every original transition chain is
// retained as a branch. A history containing a selector switch is reconciled
// to a blocked switching stage; host-local selector journals and work state
// must then be verified before the merged receipt is written or synchronized.
func ReconcileCatalogCohortReceiptHistories(receipts []CatalogCohortReceipt, at time.Time) (CatalogCohortReceipt, error) {
	if len(receipts) < 2 || len(receipts) > maxCatalogCohortHistoryBranches || at.IsZero() {
		return CatalogCohortReceipt{}, ErrCatalogOperationInvalid
	}
	base := receipts[0]
	branchesByDigest := make(map[string]CatalogCohortHistoryBranch, len(receipts))
	for _, receipt := range receipts {
		if ValidateCatalogCohortReceipt(receipt) != nil || !catalogCohortStaticIdentityEqual(base, receipt) ||
			!catalogCohortHistoryBranchReconcileSafe(receipt) {
			return CatalogCohortReceipt{}, ErrCatalogOperationInvalid
		}
		branch := CatalogCohortHistoryBranch{Stage: receipt.Stage, Transitions: append([]CatalogCohortTransition(nil), receipt.Transitions...)}
		encoded, err := json.Marshal(branch)
		if err != nil {
			return CatalogCohortReceipt{}, err
		}
		digest := sha256.Sum256(encoded)
		branchesByDigest[hex.EncodeToString(digest[:])] = branch
	}
	if len(branchesByDigest) < 2 {
		return CatalogCohortReceipt{}, ErrCatalogOperationInvalid
	}
	branches := make([]CatalogCohortHistoryBranch, 0, len(branchesByDigest))
	branchDigests := make([]string, 0, len(branchesByDigest))
	for digest := range branchesByDigest {
		branchDigests = append(branchDigests, digest)
	}
	sort.Strings(branchDigests)
	for _, digest := range branchDigests {
		branches = append(branches, branchesByDigest[digest])
	}

	commonLength := len(branches[0].Transitions)
	for _, branch := range branches[1:] {
		if len(branch.Transitions) < commonLength {
			commonLength = len(branch.Transitions)
		}
		for index := 0; index < commonLength; index++ {
			if !reflect.DeepEqual(branches[0].Transitions[index], branch.Transitions[index]) {
				commonLength = index
				break
			}
		}
	}
	common := append([]CatalogCohortTransition(nil), branches[0].Transitions[:commonLength]...)
	base.Transitions = common
	commonStage, err := replayCatalogCohortTransitions(base)
	if err != nil {
		return CatalogCohortReceipt{}, ErrCatalogOperationInvalid
	}
	base.Stage = commonStage
	base.UpdatedAt = base.CreatedAt
	if len(common) > 0 {
		base.UpdatedAt = common[len(common)-1].At
	}
	reconciledStage, _, _, _, err := catalogCohortReconciledBranchState(branches)
	if err != nil {
		return CatalogCohortReceipt{}, ErrCatalogOperationInvalid
	}
	if reconciledStage == CatalogCohortVerifying && commonStage != CatalogCohortVerifying {
		return CatalogCohortReceipt{}, ErrCatalogOperationInvalid
	}
	branchBytes, err := json.Marshal(branches)
	if err != nil {
		return CatalogCohortReceipt{}, err
	}
	evidence := sha256.Sum256(branchBytes)
	transition := CatalogCohortTransition{
		Sequence: uint64(len(common) + 1), Action: "reconcile-history",
		EvidenceSHA256: hex.EncodeToString(evidence[:]), At: at.UTC().Format(time.RFC3339Nano),
		ReconciledStage: reconciledStage, HistoricalBranches: branches,
		PreviousSHA256: lastCohortTransitionDigest(common),
	}
	transition.SHA256 = catalogCohortTransitionDigest(transition)
	base.Transitions = append(base.Transitions, transition)
	base.Stage = CatalogCohortBlocked
	base.UpdatedAt = transition.At
	if ValidateCatalogCohortReceipt(base) != nil {
		return CatalogCohortReceipt{}, ErrCatalogOperationInvalid
	}
	return base, nil
}

func validCatalogCohortHistoryReconciliation(receipt CatalogCohortReceipt, event CatalogCohortTransition, prior []CatalogCohortTransition) bool {
	if event.Action != "reconcile-history" || !eventHostEmpty(event.HostID) || event.ResumeStage != "" ||
		event.Supersession != nil || event.SuccessorID != "" ||
		(event.ReconciledStage != CatalogCohortPrepared && event.ReconciledStage != CatalogCohortHeld &&
			event.ReconciledStage != CatalogCohortSwitching && event.ReconciledStage != CatalogCohortVerifying &&
			event.ReconciledStage != CatalogCohortRollingBack && event.ReconciledStage != CatalogCohortRolledBack) ||
		len(event.HistoricalBranches) < 2 || len(event.HistoricalBranches) > maxCatalogCohortHistoryBranches {
		return false
	}
	branchBytes, err := json.Marshal(event.HistoricalBranches)
	if err != nil {
		return false
	}
	digest := sha256.Sum256(branchBytes)
	if event.EvidenceSHA256 != hex.EncodeToString(digest[:]) {
		return false
	}
	seen := make(map[string]struct{}, len(event.HistoricalBranches))
	branches := make([]CatalogCohortHistoryBranch, 0, len(event.HistoricalBranches))
	lastDigest := ""
	for _, branch := range event.HistoricalBranches {
		branchBytes, err := json.Marshal(branch)
		if err != nil {
			return false
		}
		branchDigest := sha256.Sum256(branchBytes)
		branchKey := hex.EncodeToString(branchDigest[:])
		if _, exists := seen[branchKey]; exists {
			return false
		}
		seen[branchKey] = struct{}{}
		branchReceipt := receipt
		branchReceipt.Transitions = append([]CatalogCohortTransition(nil), branch.Transitions...)
		branchReceipt.Stage = branch.Stage
		branchReceipt.UpdatedAt = branchReceipt.CreatedAt
		if len(branch.Transitions) > 0 {
			branchReceipt.UpdatedAt = branch.Transitions[len(branch.Transitions)-1].At
		}
		if ValidateCatalogCohortReceipt(branchReceipt) != nil || !catalogCohortHistoryBranchReconcileSafe(branchReceipt) ||
			!catalogCohortTransitionPrefix(prior, branch.Transitions) {
			return false
		}
		branches = append(branches, branch)
		if lastDigest != "" && branchDigestOrderKey(branch) <= lastDigest {
			return false
		}
		lastDigest = branchDigestOrderKey(branch)
	}
	reconciledStage, _, _, _, err := catalogCohortReconciledBranchState(branches)
	if err != nil || reconciledStage != event.ReconciledStage {
		return false
	}
	if event.ReconciledStage == CatalogCohortVerifying {
		prefix := receipt
		prefix.Transitions = append([]CatalogCohortTransition(nil), prior...)
		commonStage, replayErr := replayCatalogCohortTransitions(prefix)
		return replayErr == nil && commonStage == CatalogCohortVerifying
	}
	return true
}

// catalogCohortHistoryBranchReconcileSafe limits branch merging to deployment
// histories that have not entered rollback, supersession, or a prior
// reconciliation. Verification branches may be merged only when their shared
// prefix already contains verify-start; the branch-state validator rejects
// conflicting host evidence. An interrupted switch remains blocked and
// requires host-local journal verification followed by the normal rollback
// path.
func catalogCohortHistoryBranchReconcileSafe(receipt CatalogCohortReceipt) bool {
	if receipt.Stage != CatalogCohortPrepared && receipt.Stage != CatalogCohortHeld &&
		receipt.Stage != CatalogCohortBlocked && receipt.Stage != CatalogCohortSwitching &&
		receipt.Stage != CatalogCohortVerifying && receipt.Stage != CatalogCohortRollingBack &&
		receipt.Stage != CatalogCohortRolledBack {
		return false
	}
	for _, transition := range receipt.Transitions {
		switch transition.Action {
		case "hold", "fence-host", "preflight", "switch", "switch-reconciled", "block",
			"rollback-start", "rollback-host", "rollback-complete":
		case "verify-start", "verify":
			if receipt.Stage != CatalogCohortVerifying {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// catalogCohortReconciledBranchState derives the safe stage and ordered set of
// switches represented by divergent histories. Duplicate records for a host
// must be byte-for-byte identical; switch order must still match the cohort.
func catalogCohortReconciledBranchState(branches []CatalogCohortHistoryBranch) (CatalogCohortStage, []string, []string, []string, error) {
	if len(branches) == 0 {
		return "", nil, nil, nil, ErrCatalogOperationInvalid
	}
	switchesByHost := make(map[string]CatalogCohortTransition)
	verificationsByHost := make(map[string]CatalogCohortTransition)
	fencesByHost := make(map[string]CatalogCohortTransition)
	rollbacksByHost := make(map[string]CatalogCohortTransition)
	preparedOnly := true
	verifyingOnly := true
	anyVerifying := false
	anyRollbackStarted := false
	anyRollbackCompleted := false
	for _, branch := range branches {
		if branch.Stage != CatalogCohortPrepared {
			preparedOnly = false
		}
		if branch.Stage == CatalogCohortVerifying {
			anyVerifying = true
		} else {
			verifyingOnly = false
		}
		for _, transition := range branch.Transitions {
			switch transition.Action {
			case "rollback-start":
				anyRollbackStarted = true
			case "rollback-complete":
				anyRollbackStarted = true
				anyRollbackCompleted = true
			case "rollback-host":
				anyRollbackStarted = true
				if !containsString(catalogCohortSwitchOrder, transition.HostID) {
					return "", nil, nil, nil, ErrCatalogOperationInvalid
				}
				if existing, ok := rollbacksByHost[transition.HostID]; ok && !reflect.DeepEqual(existing, transition) {
					return "", nil, nil, nil, ErrCatalogOperationInvalid
				}
				rollbacksByHost[transition.HostID] = transition
			case "fence-host":
				if !containsString(catalogCohortSwitchOrder, transition.HostID) || transition.HostFence == nil {
					return "", nil, nil, nil, ErrCatalogOperationInvalid
				}
				if existing, ok := fencesByHost[transition.HostID]; ok && !reflect.DeepEqual(existing, transition) {
					return "", nil, nil, nil, ErrCatalogOperationInvalid
				}
				fencesByHost[transition.HostID] = transition
			case "switch", "switch-reconciled":
				if !containsString(catalogCohortSwitchOrder, transition.HostID) {
					return "", nil, nil, nil, ErrCatalogOperationInvalid
				}
				if existing, ok := switchesByHost[transition.HostID]; ok && !reflect.DeepEqual(existing, transition) {
					return "", nil, nil, nil, ErrCatalogOperationInvalid
				}
				switchesByHost[transition.HostID] = transition
			case "verify":
				if !containsString(catalogCohortSwitchOrder, transition.HostID) {
					return "", nil, nil, nil, ErrCatalogOperationInvalid
				}
				if existing, ok := verificationsByHost[transition.HostID]; ok && !reflect.DeepEqual(existing, transition) {
					return "", nil, nil, nil, ErrCatalogOperationInvalid
				}
				verificationsByHost[transition.HostID] = transition
			}
		}
	}
	ordered := make([]string, 0, len(switchesByHost))
	for _, hostID := range catalogCohortSwitchOrder {
		if _, ok := switchesByHost[hostID]; ok {
			ordered = append(ordered, hostID)
		}
	}
	if len(ordered) != len(switchesByHost) {
		return "", nil, nil, nil, ErrCatalogOperationInvalid
	}
	if anyVerifying {
		if !verifyingOnly || len(ordered) != len(catalogCohortSwitchOrder) {
			return "", nil, nil, nil, ErrCatalogOperationInvalid
		}
		verified := make([]string, 0, len(verificationsByHost))
		for _, hostID := range catalogCohortSwitchOrder {
			if _, ok := verificationsByHost[hostID]; ok {
				verified = append(verified, hostID)
			}
		}
		if len(verified) != len(verificationsByHost) {
			return "", nil, nil, nil, ErrCatalogOperationInvalid
		}
		if anyRollbackStarted {
			return "", nil, nil, nil, ErrCatalogOperationInvalid
		}
		return CatalogCohortVerifying, ordered, verified, nil, nil
	}
	rolledBack := make([]string, 0, len(rollbacksByHost))
	for index := len(ordered) - 1; index >= 0; index-- {
		hostID := ordered[index]
		if _, ok := rollbacksByHost[hostID]; !ok {
			break
		}
		rolledBack = append(rolledBack, hostID)
	}
	if len(rolledBack) != len(rollbacksByHost) {
		return "", nil, nil, nil, ErrCatalogOperationInvalid
	}
	if anyRollbackStarted {
		if len(ordered) == 0 || anyVerifying || anyRollbackCompleted && len(rolledBack) != len(ordered) {
			return "", nil, nil, nil, ErrCatalogOperationInvalid
		}
		if anyRollbackCompleted {
			return CatalogCohortRolledBack, ordered, nil, rolledBack, nil
		}
		return CatalogCohortRollingBack, ordered, nil, rolledBack, nil
	}
	if len(ordered) > 0 {
		return CatalogCohortSwitching, ordered, nil, nil, nil
	}
	if preparedOnly {
		return CatalogCohortPrepared, ordered, nil, nil, nil
	}
	return CatalogCohortHeld, ordered, nil, nil, nil
}

// ValidateCatalogCohortHistoryLocalState verifies that this host's stopped
// service, empty work inventory, selected target, and selector journal agree
// with the switch history in a reconciled or synchronized receipt.
func ValidateCatalogCohortHistoryLocalState(ctx context.Context, plan Plan, receipt CatalogCohortReceipt) error {
	if plan.Lane == config.ReleaseLanePRD {
		return validatePRDCatalogCohortHistoryLocalState(ctx, plan, receipt)
	}
	if validateCatalogPlan(plan) != nil || ValidateCatalogCohortReceipt(receipt) != nil || receipt.Lane != plan.Lane ||
		!cohortHasAnySwitch(receipt) {
		if validateCatalogPlan(plan) == nil && ValidateCatalogCohortReceipt(receipt) == nil && !cohortHasAnySwitch(receipt) {
			return nil
		}
		return ErrCatalogOperationInvalid
	}
	controller, err := NewSimpleServiceController()
	if err != nil {
		return err
	}
	active, err := controller.Status(ctx, plan)
	if err != nil || active {
		return ErrSimpleLaneNotQuiescent
	}
	current, err := simpleCurrentBundleForPlan(plan)
	if err != nil {
		return err
	}
	proof, err := readSimpleLaneQuiescence(ctx, plan, current)
	if err != nil || !simpleMetricsEmpty(proof.metrics) {
		return ErrSimpleLaneNotQuiescent
	}
	index := cohortHostIndex(receipt, plan.HostID)
	if index < 0 {
		return ErrCatalogOperationInvalid
	}
	host := receipt.Hosts[index]
	if proof.database != host.SchemaVersion {
		return ErrCatalogOperationInvalid
	}
	layout, err := catalogSelectorLayoutForPlan(plan, mustDefaultCatalogRoot(plan))
	if err != nil {
		return err
	}
	status, err := inspectCatalogSelector(layout)
	if err != nil {
		return err
	}
	configurationSHA256, err := hashCatalogConfiguration(layout.configFile)
	if err != nil {
		return ErrCatalogOperationInvalid
	}
	return validateCatalogCohortHistoryLocalSelectorState(layout, receipt, status, proof.database, configurationSHA256)
}

func validatePRDCatalogCohortHistoryLocalState(ctx context.Context, plan Plan, receipt CatalogCohortReceipt) error {
	if validateCatalogPlan(plan) != nil || plan.Lane != config.ReleaseLanePRD || ValidateCatalogCohortReceipt(receipt) != nil ||
		receipt.Lane != config.ReleaseLanePRD || !cohortContainsHost(receipt, plan.HostID) {
		return ErrCatalogOperationInvalid
	}
	prdPlan, err := BuildPRDUpgradePlan(plan.HostID)
	if err != nil || prdPlan.Namespace != plan.Namespace || validatePRDConfig(prdPlan) != nil {
		return ErrPRDUpgradeGuard
	}
	service, err := NewPRDServiceController()
	if err != nil {
		return err
	}
	active, err := service.Status(ctx, prdPlan)
	if err != nil {
		return err
	}
	root, err := DefaultCatalogRootForPlan(plan)
	if err != nil {
		return err
	}
	layout, err := catalogSelectorLayoutForPlan(plan, root)
	if err != nil {
		return err
	}
	proof, err := readPRDCatalogProof(ctx, prdPlan, layout)
	if err != nil || !proof.work.Empty() {
		return errors.Join(ErrPRDUpgradeGuard, err)
	}
	index := cohortHostIndex(receipt, plan.HostID)
	if index < 0 {
		return ErrCatalogOperationInvalid
	}
	host := receipt.Hosts[index]
	if err := validatePRDCatalogCohortHostObservation(receipt, host, proof); err != nil {
		return ErrCatalogOperationInvalid
	}
	_, fenced := catalogCohortHostFenceFor(receipt, plan.HostID)
	preflighted := cohortHasHostPreflight(receipt, plan.HostID)
	wantActive := host.ServiceActiveAtPreflight && !fenced && !preflighted
	if active != wantActive {
		return ErrSimpleLaneNotQuiescent
	}
	manifest, err := loadPRDBackupManifest(prdPlan, host.BackupDirectory)
	if err != nil || manifest.SchemaVersion != host.SchemaVersion || manifest.BundleSHA256 != host.Target.BundleSHA256 ||
		backupManifestSHA256(host.BackupDirectory) != host.BackupManifestSHA256 {
		return ErrSimpleBackupRequired
	}
	status, err := inspectCatalogSelector(layout)
	if err != nil {
		return err
	}
	if cohortHasSwitched(receipt, plan.HostID) || cohortHasRolledBackHost(receipt, plan.HostID) {
		if active {
			return ErrSimpleLaneNotQuiescent
		}
		if err := validateCatalogCohortHistoryLocalSelectorState(layout, receipt, status, proof.schema, proof.configurationSHA256); err != nil {
			return err
		}
		if cohortHasRolledBackHost(receipt, plan.HostID) {
			return verifyPRDCatalogCohortRollbackArtifacts(prdPlan, host, proof, status, manifest)
		}
		return verifyPRDCatalogServiceArtifacts(plan.HostID)
	}
	if !catalogCohortPreviousSelectorMatches(receipt.Lane, host, status) {
		return ErrCatalogOperationInvalid
	}
	return verifyPRDBackupTargets(manifest, true)
}

// validatePRDFreshInstallRollbackEvidence proves rollback to the recorded
// unselected replacement baseline. There are no predecessor service artifacts
// to restore or compare for a fresh install; readPRDCatalogProof has already
// revalidated the baseline against the current configuration, schema, and
// empty-work inventory, while the caller validates the selector journal and
// the consistent backup.
func validatePRDFreshInstallRollbackEvidence(host CatalogCohortHostPreflight, proof prdCatalogProof,
	status CatalogSelectorStatus) error {
	if host.HostID != "linux-host" || host.FreshInstall == nil || proof.freshInstall == nil ||
		host.FreshInstall.EvidenceSHA256 == "" || proof.freshInstall.EvidenceSHA256 != host.FreshInstall.EvidenceSHA256 ||
		status.Kind != "unselected" || status.Target != "" {
		return ErrPRDUpgradeGuard
	}
	return nil
}

func verifyPRDCatalogCohortRollbackArtifacts(plan PRDUpgradePlan, host CatalogCohortHostPreflight,
	proof prdCatalogProof, status CatalogSelectorStatus, manifest PRDBackupManifest) error {
	if plan.HostID != host.HostID {
		return ErrPRDUpgradeGuard
	}
	if host.FreshInstall != nil {
		return validatePRDFreshInstallRollbackEvidence(host, proof, status)
	}
	return verifyPRDLegacyServiceArtifactsFromBackup(plan, manifest)
}

// validatePRDCatalogCohortHostObservation compares stable local state with the
// cohort preflight. Before any selector operation, the current target and
// bundle must still be the recorded previous release. After a recorded switch
// or rollback, selector-journal validation below establishes the current
// target; comparing it to PreviousTarget here would reject the legitimate
// switched state before that journal can be checked.
func validatePRDCatalogCohortHostObservation(receipt CatalogCohortReceipt, host CatalogCohortHostPreflight, proof prdCatalogProof) error {
	if proof.schema != host.SchemaVersion || proof.configurationSHA256 != host.ConfigurationSHA256 ||
		proof.workSHA256 != host.WorkInventorySHA256 {
		return ErrCatalogOperationInvalid
	}
	selectorChanged := cohortHasSwitched(receipt, host.HostID) || cohortHasRolledBackHost(receipt, host.HostID)
	if !selectorChanged && (proof.selectorTarget != host.PreviousTarget ||
		proof.previousBundleSHA256() != host.PreviousBundleSHA256) {
		return ErrCatalogOperationInvalid
	}
	return nil
}

func catalogCohortPreviousSelectorMatches(lane config.ReleaseLane, host CatalogCohortHostPreflight,
	status CatalogSelectorStatus) bool {
	if lane == config.ReleaseLanePRD && host.PreviousTarget == "" {
		return status.Kind == "unselected" && status.Target == "" &&
			validPreviousCatalogIdentity(host, lane)
	}
	return status.Target == host.PreviousTarget && catalogSelectorStatusBundleSHA256(status) == host.PreviousBundleSHA256
}

func verifyPRDCatalogServiceArtifacts(hostID string) error {
	artifacts, err := BuildPRDCatalogServiceArtifacts(hostID)
	if err != nil {
		return err
	}
	for _, artifact := range artifacts {
		if err := verifyPRDCatalogArtifact(artifact); err != nil {
			return err
		}
	}
	return nil
}

func verifyPRDLegacyServiceArtifactsFromBackup(plan PRDUpgradePlan, manifest PRDBackupManifest) error {
	artifacts, err := BuildPRDCatalogServiceArtifacts(plan.HostID)
	if err != nil {
		return err
	}
	for _, artifact := range artifacts {
		file, ok := findPRDBackupFile(manifest, artifact.InstallPath)
		if !ok {
			return ErrPRDUpgradeGuard
		}
		path := filepath.Join(filepath.Dir(manifest.DatabaseBackupPath), file.BackupPath)
		if _, err := os.Stat(path); err != nil {
			return ErrPRDUpgradeGuard
		}
		legacy := artifact
		legacy.Mode = file.Mode
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		legacy.Contents = string(contents)
		if err := verifyPRDArtifactContents(legacy); err != nil {
			return err
		}
	}
	return nil
}

func catalogCohortHostFenceFor(receipt CatalogCohortReceipt, hostID string) (CatalogCohortHostFence, bool) {
	var found CatalogCohortHostFence
	var ok bool
	var collect func([]CatalogCohortTransition)
	collect = func(events []CatalogCohortTransition) {
		for _, event := range events {
			if event.Action == "fence-host" && event.HostID == hostID && event.HostFence != nil && !ok {
				found, ok = *event.HostFence, true
			}
			if event.Action == "reconcile-history" {
				for _, branch := range event.HistoricalBranches {
					collect(branch.Transitions)
				}
			}
		}
	}
	collect(receipt.Transitions)
	return found, ok
}

func cohortHasHostPreflight(receipt CatalogCohortReceipt, hostID string) bool {
	var found bool
	walkCatalogCohortTransitions(receipt.Transitions, func(event CatalogCohortTransition) {
		if event.Action == "preflight" && event.HostID == hostID {
			found = true
		}
	})
	return found
}

// validateCatalogCohortHistoryLocalSelectorState verifies the durable selector
// and operation journals after the caller has established stopped-service,
// empty-work, schema, and configuration evidence. It is shared by the live
// reconciliation path and fixture tests so rollback recovery exercises the
// same on-disk validation logic as synchronization.
func validateCatalogCohortHistoryLocalSelectorState(layout catalogSelectorLayout, receipt CatalogCohortReceipt,
	status CatalogSelectorStatus, databaseSchema int, configurationSHA256 string) error {
	if ValidateCatalogCohortReceipt(receipt) != nil || receipt.Lane != layout.lane || cohortHostIndex(receipt, layout.hostID) < 0 {
		return ErrCatalogOperationInvalid
	}
	matching, err := catalogCohortLocalSelectorOperations(layout, receipt.OperationID)
	if err != nil {
		return err
	}
	return validateCatalogCohortReconciledSelectorEvidence(layout, receipt, status, databaseSchema, configurationSHA256, matching)
}

func catalogCohortLocalSelectorOperations(layout catalogSelectorLayout, cohortOperationID string) ([]CatalogSelectorOperation, error) {
	entries, err := os.ReadDir(layout.operationRoot)
	if errors.Is(err, os.ErrNotExist) {
		entries = nil
	} else if err != nil {
		return nil, err
	}
	var matching []CatalogSelectorOperation
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		operationID := entry.Name()[:len(entry.Name())-len(filepath.Ext(entry.Name()))]
		operation, readErr := readCatalogSelectorOperation(layout.operationRoot, operationID)
		if readErr != nil {
			return nil, ErrCatalogOperationInvalid
		}
		if operation.CohortOperationID == cohortOperationID {
			matching = append(matching, operation)
		}
	}
	return matching, nil
}

func validateCatalogCohortReconciledSelectorEvidence(layout catalogSelectorLayout, receipt CatalogCohortReceipt,
	status CatalogSelectorStatus, databaseSchema int, configurationSHA256 string, matching []CatalogSelectorOperation) error {
	return validateCatalogCohortReconciledSelectorEvidenceUsing(layout, receipt, status, databaseSchema,
		configurationSHA256, matching, validateCatalogOperation)
}

func validatePRDCatalogRollbackRetryState(layout catalogSelectorLayout, receipt CatalogCohortReceipt,
	operation CatalogSelectorOperation, status CatalogSelectorStatus, databaseSchema int, configurationSHA256 string) error {
	return validatePRDCatalogRollbackRetryStateUsing(layout, receipt, operation, status, databaseSchema,
		configurationSHA256, validateCatalogOperation)
}

func validatePRDCatalogRollbackRetryStateUsing(layout catalogSelectorLayout, receipt CatalogCohortReceipt,
	operation CatalogSelectorOperation, status CatalogSelectorStatus, databaseSchema int, configurationSHA256 string,
	validateOperation func(catalogSelectorLayout, CatalogSelectorOperation) error) error {
	if layout.lane != config.ReleaseLanePRD || operation.Stage != catalogOperationRolledBack ||
		status.Target != operation.PreviousTarget || (receipt.Stage != CatalogCohortRollingBack && receipt.Stage != CatalogCohortRolledBack) {
		return ErrCatalogRollbackUnsafe
	}
	if err := validateCatalogCohortReconciledSelectorEvidenceUsing(layout, receipt, status, databaseSchema,
		configurationSHA256, []CatalogSelectorOperation{operation}, validateOperation); err != nil {
		return errors.Join(ErrCatalogRollbackUnsafe, err)
	}
	return nil
}

// validateCatalogCohortReconciledSelectorEvidenceUsing shares the receipt,
// selector, and rollback-journal state machine with disposable PRD fixtures.
// Production always supplies validateCatalogOperation, including its backup
// and compatibility checks.
func validateCatalogCohortReconciledSelectorEvidenceUsing(layout catalogSelectorLayout, receipt CatalogCohortReceipt,
	status CatalogSelectorStatus, databaseSchema int, configurationSHA256 string, matching []CatalogSelectorOperation,
	validateOperation func(catalogSelectorLayout, CatalogSelectorOperation) error) error {
	if validateOperation == nil {
		return ErrCatalogOperationInvalid
	}
	index := cohortHostIndex(receipt, layout.hostID)
	if index < 0 {
		return ErrCatalogOperationInvalid
	}
	host := receipt.Hosts[index]
	if databaseSchema != host.SchemaVersion || configurationSHA256 != host.ConfigurationSHA256 {
		return ErrCatalogOperationInvalid
	}
	if cohortHasRolledBackHost(receipt, layout.hostID) {
		if len(matching) != 1 {
			return ErrCatalogOperationInvalid
		}
		operation := matching[0]
		if validateOperation(layout, operation) != nil || operation.Stage != catalogOperationRolledBack ||
			operation.HostID != layout.hostID || operation.Lane != layout.lane || operation.Release != host.Target ||
			operation.PreviousTarget != host.PreviousTarget || operation.ConfigurationSHA256 != host.ConfigurationSHA256 ||
			operation.DatabaseSchemaAtSwitch != host.SchemaVersion || status.Target != operation.PreviousTarget ||
			!catalogCohortPreviousSelectorMatches(receipt.Lane, host, status) {
			return ErrCatalogOperationInvalid
		}
		digest, digestErr := catalogSelectorOperationDigest(layout, operation.OperationID)
		if digestErr != nil || !cohortHasRollbackEvidence(receipt, layout.hostID, digest) {
			return ErrCatalogOperationInvalid
		}
		return nil
	}
	if cohortHasSwitched(receipt, layout.hostID) {
		if len(matching) != 1 {
			return ErrCatalogOperationInvalid
		}
		operation := matching[0]
		if validateOperation(layout, operation) != nil || operation.Stage != catalogOperationComplete ||
			operation.HostID != layout.hostID || operation.Lane != layout.lane || operation.Release != host.Target ||
			operation.PreviousTarget != host.PreviousTarget || operation.ConfigurationSHA256 != host.ConfigurationSHA256 ||
			operation.DatabaseSchemaAtSwitch != host.SchemaVersion || status.Target != operation.NextTarget {
			return ErrCatalogOperationInvalid
		}
		digest, digestErr := catalogSelectorOperationDigest(layout, operation.OperationID)
		if digestErr != nil || !cohortHasSwitchEvidence(receipt, layout.hostID, digest) {
			return ErrCatalogOperationInvalid
		}
		return nil
	}
	if len(matching) != 0 || !catalogCohortPreviousSelectorMatches(receipt.Lane, host, status) {
		return ErrCatalogOperationInvalid
	}
	return nil
}

func cohortHasRolledBackHost(receipt CatalogCohortReceipt, hostID string) bool {
	found := false
	walkCatalogCohortTransitions(receipt.Transitions, func(transition CatalogCohortTransition) {
		if transition.Action == "rollback-host" && transition.HostID == hostID {
			found = true
		}
	})
	return found
}

func cohortHasSwitchEvidence(receipt CatalogCohortReceipt, hostID, digest string) bool {
	found := false
	walkCatalogCohortTransitions(receipt.Transitions, func(transition CatalogCohortTransition) {
		if (transition.Action == "switch" || transition.Action == "switch-reconciled") && transition.HostID == hostID && transition.EvidenceSHA256 == digest {
			found = true
		}
	})
	return found
}

func cohortHasRollbackEvidence(receipt CatalogCohortReceipt, hostID, digest string) bool {
	found := false
	walkCatalogCohortTransitions(receipt.Transitions, func(transition CatalogCohortTransition) {
		if transition.Action == "rollback-host" && transition.HostID == hostID && transition.EvidenceSHA256 == digest {
			found = true
		}
	})
	return found
}

func branchDigestOrderKey(branch CatalogCohortHistoryBranch) string {
	encoded, err := json.Marshal(branch)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func catalogCohortReceiptHasHistoricalBranch(receipt CatalogCohortReceipt, transitions []CatalogCohortTransition) bool {
	for _, event := range receipt.Transitions {
		if event.Action != "reconcile-history" {
			continue
		}
		for _, branch := range event.HistoricalBranches {
			if reflect.DeepEqual(branch.Transitions, transitions) {
				return true
			}
		}
	}
	return false
}

// ObserveCatalogCohortSupersession captures the local state needed to retire
// one never-switched, fully rolled-back, completed, or fully switched verifying attempt. It does not
// mutate service, selector, or database state. Ambiguous selector journals,
// an active service, or any retained/resumable work refuse the observation.
// The successor must already be held with fresh four-host preflight and backup
// evidence.
func ObserveCatalogCohortSupersession(ctx context.Context, plan Plan, receipt, successor CatalogCohortReceipt) (CatalogCohortSupersessionEvidence, error) {
	if validateCatalogPlan(plan) != nil || ValidateCatalogCohortReceipt(receipt) != nil || ValidateCatalogCohortReceipt(successor) != nil ||
		receipt.Lane != plan.Lane || successor.Lane != plan.Lane || receipt.OperationID == successor.OperationID ||
		!catalogCohortReceiptSupersessionEligible(receipt) || successor.Stage != CatalogCohortHeld ||
		!cohortEvidenceFresh(successor, time.Now().UTC().Format(time.RFC3339Nano)) {
		return CatalogCohortSupersessionEvidence{}, ErrCatalogOperationInvalid
	}
	index := cohortHostIndex(successor, plan.HostID)
	if index < 0 || index != len(currentSupersededHosts(receipt.Transitions)) {
		return CatalogCohortSupersessionEvidence{}, ErrCatalogOperationInvalid
	}
	if plan.Lane == config.ReleaseLanePRD {
		if err := CatalogCohortAdmissionHoldMatches(plan, successor.OperationID); err != nil {
			return CatalogCohortSupersessionEvidence{}, err
		}
		prdPlan, planErr := BuildPRDUpgradePlan(plan.HostID)
		if planErr != nil || validatePRDPreflightBackup(ctx, prdPlan, successor.Hosts[index]) != nil {
			return CatalogCohortSupersessionEvidence{}, ErrSimpleBackupRequired
		}
	}
	layout, err := catalogSelectorLayoutForPlan(plan, mustDefaultCatalogRoot(plan))
	if err != nil {
		return CatalogCohortSupersessionEvidence{}, err
	}
	catalogFD, err := acquireCatalogRootPublicationLock(layout.catalogRoot)
	if err != nil {
		return CatalogCohortSupersessionEvidence{}, err
	}
	defer releaseCatalogRootPublicationLock(catalogFD)
	laneFD, err := acquireSimpleLaneLock(plan)
	if err != nil {
		return CatalogCohortSupersessionEvidence{}, err
	}
	defer releasePRDUpgradeLock(laneFD)
	status, err := inspectCatalogSelector(layout)
	if err != nil {
		return CatalogCohortSupersessionEvidence{}, err
	}
	configurationSHA256, err := hashCatalogConfiguration(layout.configFile)
	if err != nil {
		return CatalogCohortSupersessionEvidence{}, err
	}
	workSHA256, schemaVersion, empty, active, err := readCatalogCohortWorkInventory(ctx, plan)
	if err != nil {
		return CatalogCohortSupersessionEvidence{}, err
	}
	selectorOperations, err := catalogCohortSelectorOperationDisposition(layout, receipt)
	if err != nil {
		return CatalogCohortSupersessionEvidence{}, err
	}
	oldHostIndex := cohortHostIndex(receipt, plan.HostID)
	if oldHostIndex < 0 || !catalogCohortSupersessionSelectorMatches(layout, receipt, receipt.Hosts[oldHostIndex], status) {
		return CatalogCohortSupersessionEvidence{}, ErrCatalogOperationInvalid
	}
	selectorBundleSHA256 := catalogSelectorStatusBundleSHA256(status)
	if plan.Lane == config.ReleaseLanePRD && status.Kind == "unselected" {
		prdPlan, planErr := BuildPRDUpgradePlan(plan.HostID)
		if planErr != nil {
			return CatalogCohortSupersessionEvidence{}, ErrPRDUpgradeGuard
		}
		legacy, legacyErr := readPRDLegacyInventory(prdPlan)
		if legacyErr != nil {
			return CatalogCohortSupersessionEvidence{}, legacyErr
		}
		selectorBundleSHA256, err = PRDLegacyBundleDigest(legacy)
		if err != nil {
			return CatalogCohortSupersessionEvidence{}, err
		}
	}
	evidence := CatalogCohortSupersessionEvidence{
		HostID: plan.HostID, SuccessorOperationID: successor.OperationID,
		ExpectedSelector: successor.Hosts[index].PreviousTarget, ObservedSelector: status.Target,
		ServiceActive: active, WorkInventorySHA256: workSHA256, WorkInventoryEmpty: empty,
		ConfigurationSHA256: configurationSHA256, DatabaseSchema: schemaVersion,
		SelectorBundleSHA256: selectorBundleSHA256,
		SelectorOperations:   selectorOperations,
	}
	if !observationMatchesSuccessor(receipt, successor, evidence) {
		return CatalogCohortSupersessionEvidence{}, ErrCatalogOperationInvalid
	}
	return evidence, nil
}

func catalogCohortSupersessionSelectorMatches(layout catalogSelectorLayout, receipt CatalogCohortReceipt,
	host CatalogCohortHostPreflight, status CatalogSelectorStatus) bool {
	if receipt.Stage == CatalogCohortComplete || receipt.Stage == CatalogCohortVerifying {
		expected := filepath.Join(layout.catalogRoot, receipt.ReleaseID, layout.hostID)
		return status.Kind == "catalog" && status.Target == expected && status.Release != nil &&
			status.Release.Identity == host.Target
	}
	return catalogCohortPreviousSelectorMatches(receipt.Lane, host, status)
}

func readCatalogCohortWorkInventory(ctx context.Context, plan Plan) (string, int, bool, bool, error) {
	if plan.Lane != config.ReleaseLanePRD {
		service, err := NewSimpleServiceController()
		if err != nil {
			return "", 0, false, false, err
		}
		report, err := ReadCatalogWorkInventory(ctx, plan, service)
		if err != nil {
			return "", 0, false, false, err
		}
		return report.WorkInventory.SHA256(), report.DatabaseSchema, report.WorkInventoryEmpty, report.ServiceActive, nil
	}
	prdPlan, err := BuildPRDUpgradePlan(plan.HostID)
	if err != nil || validatePRDConfig(prdPlan) != nil {
		return "", 0, false, false, ErrPRDUpgradeGuard
	}
	service, err := NewPRDServiceController()
	if err != nil {
		return "", 0, false, false, err
	}
	active, err := service.Status(ctx, prdPlan)
	if err != nil {
		return "", 0, false, false, err
	}
	database, err := store.OpenExistingRestartPreflightReadOnly(ctx, plan.Namespace.Database)
	if err != nil {
		return "", 0, false, active, err
	}
	defer database.Close()
	schema, work, err := readPRDWorkSnapshot(ctx, database)
	if err != nil {
		return "", 0, false, active, err
	}
	if schema <= 0 {
		return "", 0, false, active, ErrPRDUpgradeGuard
	}
	return work.SHA256(), schema, work.Empty(), active, nil
}

func catalogCohortSelectorOperationDisposition(layout catalogSelectorLayout, receipt CatalogCohortReceipt) (string, error) {
	root := layout.operationRoot
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		if cohortHasSwitched(receipt, layout.hostID) {
			return "", ErrCatalogOperationInvalid
		}
		return "none", nil
	} else if err != nil || validateExistingPrivateDirectory(root) != nil {
		return "", ErrCatalogOperationInvalid
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	var matching []CatalogSelectorOperation
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		operationID := entry.Name()[:len(entry.Name())-len(filepath.Ext(entry.Name()))]
		operation, err := readCatalogSelectorOperation(root, operationID)
		if err != nil {
			return "", err
		}
		if operation.CohortOperationID == receipt.OperationID {
			matching = append(matching, operation)
		}
	}
	if !cohortHasSwitched(receipt, layout.hostID) {
		if len(matching) != 0 {
			return "", ErrCatalogOperationInvalid
		}
		return "none", nil
	}
	if receipt.Stage == CatalogCohortComplete || receipt.Stage == CatalogCohortVerifying {
		index := cohortHostIndex(receipt, layout.hostID)
		if index < 0 || len(matching) != 1 {
			return "", ErrCatalogOperationInvalid
		}
		host := receipt.Hosts[index]
		operation := matching[0]
		expectedTarget := filepath.Join(layout.catalogRoot, receipt.ReleaseID, layout.hostID)
		status, statusErr := inspectCatalogSelector(layout)
		if statusErr != nil || validateCatalogOperation(layout, operation) != nil ||
			operation.HostID != layout.hostID || operation.Lane != layout.lane || operation.Stage != catalogOperationComplete ||
			operation.CohortOperationID != receipt.OperationID ||
			operation.Release != host.Target || operation.PreviousTarget != host.PreviousTarget || operation.NextTarget != expectedTarget ||
			operation.ConfigurationSHA256 != host.ConfigurationSHA256 || operation.DatabaseSchemaAtSwitch != host.SchemaVersion ||
			operation.BackupDirectory != host.BackupDirectory || operation.BackupManifestSHA256 != host.BackupManifestSHA256 ||
			status.Kind != "catalog" || status.Target != expectedTarget || status.Release == nil || status.Release.Identity != host.Target {
			return "", ErrCatalogOperationInvalid
		}
		digest, digestErr := catalogSelectorOperationDigest(layout, operation.OperationID)
		if digestErr != nil || !cohortHasSwitchEvidence(receipt, layout.hostID, digest) {
			return "", ErrCatalogOperationInvalid
		}
		return "completed", nil
	}
	if receipt.Stage != CatalogCohortRolledBack || !containsString(cohortRolledBackHosts(receipt), layout.hostID) || len(matching) != 1 ||
		matching[0].HostID != layout.hostID || matching[0].Stage != catalogOperationRolledBack {
		return "", ErrCatalogOperationInvalid
	}
	return "rolled_back", nil
}

func observationMatchesSuccessor(receipt, successor CatalogCohortReceipt, observation CatalogCohortSupersessionEvidence) bool {
	if !catalogCohortReceiptSupersessionEligible(receipt) || successor.Stage != CatalogCohortHeld ||
		receipt.Lane != successor.Lane || observation.HostID == "" || observation.SuccessorOperationID != successor.OperationID ||
		currentSupersessionSuccessor(receipt.Transitions) != "" && currentSupersessionSuccessor(receipt.Transitions) != successor.OperationID ||
		observation.ServiceActive || !observation.WorkInventoryEmpty ||
		!bundleDigestPattern.MatchString(observation.WorkInventorySHA256) || !bundleDigestPattern.MatchString(observation.ConfigurationSHA256) ||
		observation.DatabaseSchema <= 0 {
		return false
	}
	index := cohortHostIndex(successor, observation.HostID)
	if index < 0 || index != len(currentSupersededHosts(receipt.Transitions)) {
		return false
	}
	newHost := successor.Hosts[index]
	if observation.ExpectedSelector != newHost.PreviousTarget || observation.ObservedSelector != newHost.PreviousTarget ||
		observation.WorkInventorySHA256 != newHost.WorkInventorySHA256 || observation.ConfigurationSHA256 != newHost.ConfigurationSHA256 ||
		observation.DatabaseSchema != newHost.SchemaVersion {
		return false
	}
	switched := cohortHasSwitched(receipt, observation.HostID)
	oldHostIndex := cohortHostIndex(receipt, observation.HostID)
	if oldHostIndex < 0 {
		return false
	}
	oldHost := receipt.Hosts[oldHostIndex]
	if receipt.Stage == CatalogCohortComplete || receipt.Stage == CatalogCohortVerifying {
		if observation.SelectorOperations != "completed" || observation.SelectorBundleSHA256 != oldHost.Target.BundleSHA256 ||
			newHost.PreviousBundleSHA256 != oldHost.Target.BundleSHA256 {
			return false
		}
		return true
	}
	if (receipt.Lane == config.ReleaseLanePRD && observation.SelectorBundleSHA256 != oldHost.PreviousBundleSHA256) ||
		(observation.SelectorBundleSHA256 != "" && observation.SelectorBundleSHA256 != oldHost.PreviousBundleSHA256) ||
		newHost.PreviousBundleSHA256 != oldHost.PreviousBundleSHA256 {
		return false
	}
	if switched {
		if newHost.PreviousTarget != oldHost.PreviousTarget {
			return false
		}
		return receipt.Stage == CatalogCohortRolledBack && containsString(cohortRolledBackHosts(receipt), observation.HostID) && observation.SelectorOperations == "rolled_back"
	}
	return observation.SelectorOperations == "none"
}

func validSupersessionEvidence(event CatalogCohortTransition) bool {
	if event.Supersession == nil || event.SuccessorID != "" || event.HostID != event.Supersession.HostID ||
		!validCatalogOperationID(event.Supersession.SuccessorOperationID) || event.Supersession.ServiceActive ||
		event.ResumeStage != "" || event.ReconciledStage != "" || len(event.HistoricalBranches) != 0 || event.SupersessionRestart != nil ||
		!event.Supersession.WorkInventoryEmpty || !bundleDigestPattern.MatchString(event.Supersession.WorkInventorySHA256) ||
		!bundleDigestPattern.MatchString(event.Supersession.ConfigurationSHA256) || event.Supersession.DatabaseSchema <= 0 ||
		(event.Supersession.SelectorOperations != "none" && event.Supersession.SelectorOperations != "rolled_back" && event.Supersession.SelectorOperations != "completed") ||
		(event.Supersession.SelectorBundleSHA256 != "" && !bundleDigestPattern.MatchString(event.Supersession.SelectorBundleSHA256)) ||
		(event.Supersession.SelectorOperations == "completed" && !bundleDigestPattern.MatchString(event.Supersession.SelectorBundleSHA256)) ||
		event.Supersession.ExpectedSelector != event.Supersession.ObservedSelector {
		return false
	}
	encoded, err := json.Marshal(*event.Supersession)
	if err != nil {
		return false
	}
	digest := sha256.Sum256(encoded)
	return event.EvidenceSHA256 == hex.EncodeToString(digest[:])
}

func validSupersessionEvidenceForReceipt(event CatalogCohortTransition, receipt CatalogCohortReceipt,
	stage, blockedFrom CatalogCohortStage) bool {
	if !validSupersessionEvidence(event) {
		return false
	}
	index := cohortHostIndex(receipt, event.HostID)
	if index < 0 {
		return false
	}
	effectiveStage := stage
	if stage == CatalogCohortBlocked {
		effectiveStage = blockedFrom
	}
	supersession := event.Supersession
	switched := cohortHasSwitched(receipt, event.HostID)
	switch effectiveStage {
	case CatalogCohortComplete, CatalogCohortVerifying:
		return switched && supersession.SelectorOperations == "completed" &&
			supersession.SelectorBundleSHA256 == receipt.Hosts[index].Target.BundleSHA256
	case CatalogCohortRolledBack:
		if switched {
			return containsString(cohortRolledBackHosts(receipt), event.HostID) &&
				supersession.SelectorOperations == "rolled_back" &&
				(supersession.SelectorBundleSHA256 == receipt.Hosts[index].PreviousBundleSHA256 ||
					receipt.Lane != config.ReleaseLanePRD && supersession.SelectorBundleSHA256 == "")
		}
		return supersession.SelectorOperations == "none" &&
			(supersession.SelectorBundleSHA256 == receipt.Hosts[index].PreviousBundleSHA256 ||
				receipt.Lane != config.ReleaseLanePRD && supersession.SelectorBundleSHA256 == "")
	case CatalogCohortHeld, CatalogCohortPrepared:
		return !switched && supersession.SelectorOperations == "none" &&
			(supersession.SelectorBundleSHA256 == receipt.Hosts[index].PreviousBundleSHA256 ||
				receipt.Lane != config.ReleaseLanePRD && supersession.SelectorBundleSHA256 == "")
	default:
		return false
	}
}

func validSupersessionRestart(event CatalogCohortTransition) bool {
	restart := event.SupersessionRestart
	if restart == nil || event.Action != "supersession-restart" || !eventHostEmpty(event.HostID) ||
		event.ResumeStage != "" || event.ReconciledStage != "" || len(event.HistoricalBranches) != 0 || event.Supersession != nil ||
		!validCatalogOperationID(event.SuccessorID) || restart.NextSuccessorID != event.SuccessorID ||
		!validCatalogOperationID(restart.PreviousSuccessorID) || restart.PreviousSuccessorID == restart.NextSuccessorID ||
		!validCohortTimestamp(restart.PreviousEvidenceExpiresAt) || event.At < restart.PreviousEvidenceExpiresAt ||
		len(restart.PriorObservedHosts) == 0 || len(restart.PriorObservedHosts) >= len(catalogCohortSwitchOrder) {
		return false
	}
	for index, hostID := range restart.PriorObservedHosts {
		if index >= len(catalogCohortSwitchOrder) || hostID != catalogCohortSwitchOrder[index] {
			return false
		}
	}
	encoded, err := json.Marshal(*restart)
	if err != nil {
		return false
	}
	digest := sha256.Sum256(encoded)
	return event.EvidenceSHA256 == hex.EncodeToString(digest[:])
}

func catalogCohortReceiptSupersessionEligible(receipt CatalogCohortReceipt) bool {
	if ValidateCatalogCohortReceipt(receipt) != nil {
		return false
	}
	stage, err := replayCatalogCohortTransitions(receipt)
	if err != nil {
		return false
	}
	switch stage {
	case CatalogCohortPrepared:
		return !cohortHasAnySwitchTransitions(receipt.Transitions)
	case CatalogCohortComplete:
		return len(cohortSwitchedHostsFrom(receipt.Transitions)) == len(catalogCohortSwitchOrder)
	case CatalogCohortVerifying:
		return len(cohortSwitchedHostsFrom(receipt.Transitions)) == len(catalogCohortSwitchOrder)
	case CatalogCohortHeld:
		return !cohortHasAnySwitchTransitions(receipt.Transitions)
	case CatalogCohortBlocked:
		resume := blockedCohortResumeStage(receipt.Transitions)
		if resume == CatalogCohortPrepared {
			return !cohortHasAnySwitchTransitions(receipt.Transitions)
		}
		if resume == CatalogCohortHeld {
			return !cohortHasAnySwitchTransitions(receipt.Transitions)
		}
		return resume == CatalogCohortRolledBack && cohortRollbackComplete(receipt.Transitions)
	case CatalogCohortRolledBack:
		return cohortRollbackComplete(receipt.Transitions)
	default:
		return false
	}
}

func catalogCohortSupersessionStageEligible(stage, blockedFrom CatalogCohortStage, transitions []CatalogCohortTransition) bool {
	switch stage {
	case CatalogCohortPrepared:
		return !cohortHasAnySwitchTransitions(transitions)
	case CatalogCohortComplete:
		return len(cohortSwitchedHostsFrom(transitions)) == len(catalogCohortSwitchOrder)
	case CatalogCohortVerifying:
		return len(cohortSwitchedHostsFrom(transitions)) == len(catalogCohortSwitchOrder)
	case CatalogCohortHeld:
		return !cohortHasAnySwitchTransitions(transitions)
	case CatalogCohortBlocked:
		if blockedFrom == CatalogCohortPrepared {
			return !cohortHasAnySwitchTransitions(transitions)
		}
		if blockedFrom == CatalogCohortHeld {
			return !cohortHasAnySwitchTransitions(transitions)
		}
		return blockedFrom == CatalogCohortRolledBack && cohortRollbackComplete(transitions)
	case CatalogCohortRolledBack:
		return cohortRollbackComplete(transitions)
	default:
		return false
	}
}

func cohortRollbackComplete(transitions []CatalogCohortTransition) bool {
	switched := cohortSwitchedHostsFrom(transitions)
	rolledBack := cohortRolledBackHosts(CatalogCohortReceipt{Transitions: transitions})
	if len(switched) == 0 || len(rolledBack) != len(switched) {
		return false
	}
	complete := false
	walkCatalogCohortTransitions(transitions, func(transition CatalogCohortTransition) {
		if transition.Action == "rollback-complete" {
			complete = true
		}
	})
	return complete
}

func cohortHasAnySwitch(receipt CatalogCohortReceipt) bool {
	return cohortHasAnySwitchTransitions(receipt.Transitions)
}

func cohortHasAnySwitchTransitions(transitions []CatalogCohortTransition) bool {
	return len(cohortSwitchedHostsFrom(transitions)) > 0
}

func cohortSwitchedHostsFrom(transitions []CatalogCohortTransition) []string {
	switched := make(map[string]struct{})
	walkCatalogCohortTransitions(transitions, func(transition CatalogCohortTransition) {
		if transition.Action == "switch" || transition.Action == "switch-reconciled" {
			switched[transition.HostID] = struct{}{}
		}
	})
	hosts := make([]string, 0, len(switched))
	for _, hostID := range catalogCohortSwitchOrder {
		if _, ok := switched[hostID]; ok {
			hosts = append(hosts, hostID)
		}
	}
	return hosts
}

func supersededHosts(transitions []CatalogCohortTransition) []string {
	var hosts []string
	for _, transition := range transitions {
		if transition.Action == "supersede-host" {
			hosts = append(hosts, transition.HostID)
		}
	}
	return hosts
}

func currentSupersededHosts(transitions []CatalogCohortTransition) []string {
	start := 0
	for index, transition := range transitions {
		if transition.Action == "supersession-restart" {
			start = index + 1
		}
	}
	var hosts []string
	for _, transition := range transitions[start:] {
		if transition.Action == "supersede-host" {
			hosts = append(hosts, transition.HostID)
		}
	}
	return hosts
}

func currentSupersessionSuccessor(transitions []CatalogCohortTransition) string {
	start := 0
	for index, transition := range transitions {
		if transition.Action == "supersession-restart" {
			start = index
		}
	}
	for _, transition := range transitions[start:] {
		if transition.Action == "supersession-restart" {
			return transition.SuccessorID
		}
		if transition.Action == "supersede-host" && transition.Supersession != nil {
			return transition.Supersession.SuccessorOperationID
		}
	}
	return ""
}

func supersessionSuccessor(transitions []CatalogCohortTransition) string {
	return currentSupersessionSuccessor(transitions)
}

func cohortTransitionsHaveSupersession(transitions []CatalogCohortTransition, successorID string) bool {
	return len(currentSupersededHosts(transitions)) == len(catalogCohortSwitchOrder) && supersessionSuccessor(transitions) == successorID &&
		cohortHasAllSupersessionEvidenceFrom(transitions, successorID)
}

func cohortHasAllSupersessionEvidence(receipt CatalogCohortReceipt, successorID string) bool {
	return cohortHasAllSupersessionEvidenceFrom(receipt.Transitions, successorID)
}

func cohortHasAllSupersessionEvidenceFrom(transitions []CatalogCohortTransition, successorID string) bool {
	var evidence []CatalogCohortTransition
	start := 0
	for index, transition := range transitions {
		if transition.Action == "supersession-restart" {
			start = index + 1
		}
	}
	for _, transition := range transitions[start:] {
		if transition.Action == "supersede-host" {
			evidence = append(evidence, transition)
		}
	}
	if len(evidence) != len(catalogCohortSwitchOrder) {
		return false
	}
	for index, transition := range evidence {
		if transition.HostID != catalogCohortSwitchOrder[index] || !validSupersessionEvidence(transition) ||
			transition.Supersession.SuccessorOperationID != successorID {
			return false
		}
	}
	return true
}
