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
	reconciledStage, _, _, err := catalogCohortReconciledBranchState(branches)
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
			event.ReconciledStage != CatalogCohortSwitching && event.ReconciledStage != CatalogCohortVerifying) ||
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
	reconciledStage, _, _, err := catalogCohortReconciledBranchState(branches)
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
		receipt.Stage != CatalogCohortVerifying {
		return false
	}
	for _, transition := range receipt.Transitions {
		switch transition.Action {
		case "hold", "preflight", "switch", "switch-reconciled", "block":
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
func catalogCohortReconciledBranchState(branches []CatalogCohortHistoryBranch) (CatalogCohortStage, []string, []string, error) {
	if len(branches) == 0 {
		return "", nil, nil, ErrCatalogOperationInvalid
	}
	switchesByHost := make(map[string]CatalogCohortTransition)
	verificationsByHost := make(map[string]CatalogCohortTransition)
	preparedOnly := true
	verifyingOnly := true
	anyVerifying := false
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
			case "switch", "switch-reconciled":
				if !containsString(catalogCohortSwitchOrder, transition.HostID) {
					return "", nil, nil, ErrCatalogOperationInvalid
				}
				if existing, ok := switchesByHost[transition.HostID]; ok && !reflect.DeepEqual(existing, transition) {
					return "", nil, nil, ErrCatalogOperationInvalid
				}
				switchesByHost[transition.HostID] = transition
			case "verify":
				if !containsString(catalogCohortSwitchOrder, transition.HostID) {
					return "", nil, nil, ErrCatalogOperationInvalid
				}
				if existing, ok := verificationsByHost[transition.HostID]; ok && !reflect.DeepEqual(existing, transition) {
					return "", nil, nil, ErrCatalogOperationInvalid
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
		return "", nil, nil, ErrCatalogOperationInvalid
	}
	if anyVerifying {
		if !verifyingOnly || len(ordered) != len(catalogCohortSwitchOrder) {
			return "", nil, nil, ErrCatalogOperationInvalid
		}
		verified := make([]string, 0, len(verificationsByHost))
		for _, hostID := range catalogCohortSwitchOrder {
			if _, ok := verificationsByHost[hostID]; ok {
				verified = append(verified, hostID)
			}
		}
		if len(verified) != len(verificationsByHost) {
			return "", nil, nil, ErrCatalogOperationInvalid
		}
		return CatalogCohortVerifying, ordered, verified, nil
	}
	if len(ordered) > 0 {
		return CatalogCohortSwitching, ordered, nil, nil
	}
	if preparedOnly {
		return CatalogCohortPrepared, ordered, nil, nil
	}
	return CatalogCohortHeld, ordered, nil, nil
}

// ValidateCatalogCohortHistoryLocalState verifies that this host's stopped
// service, empty work inventory, selected target, and selector journal agree
// with the switch history in a reconciled or synchronized receipt.
func ValidateCatalogCohortHistoryLocalState(ctx context.Context, plan Plan, receipt CatalogCohortReceipt) error {
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
	entries, err := os.ReadDir(layout.operationRoot)
	if errors.Is(err, os.ErrNotExist) {
		entries = nil
	} else if err != nil {
		return err
	}
	var matching []CatalogSelectorOperation
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		operationID := entry.Name()[:len(entry.Name())-len(filepath.Ext(entry.Name()))]
		operation, readErr := readCatalogSelectorOperation(layout.operationRoot, operationID)
		if readErr != nil {
			return ErrCatalogOperationInvalid
		}
		if operation.CohortOperationID == receipt.OperationID {
			matching = append(matching, operation)
		}
	}
	return validateCatalogCohortReconciledSelectorEvidence(layout, receipt, status, databaseSchema, configurationSHA256, matching)
}

func validateCatalogCohortReconciledSelectorEvidence(layout catalogSelectorLayout, receipt CatalogCohortReceipt,
	status CatalogSelectorStatus, databaseSchema int, configurationSHA256 string, matching []CatalogSelectorOperation) error {
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
		if validateCatalogOperation(layout, operation) != nil || operation.Stage != catalogOperationRolledBack ||
			operation.HostID != layout.hostID || operation.Lane != layout.lane || operation.Release != host.Target ||
			operation.PreviousTarget != host.PreviousTarget || operation.ConfigurationSHA256 != host.ConfigurationSHA256 ||
			operation.DatabaseSchemaAtSwitch != host.SchemaVersion || status.Target != operation.PreviousTarget ||
			catalogSelectorStatusBundleSHA256(status) != host.PreviousBundleSHA256 {
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
		if validateCatalogOperation(layout, operation) != nil || operation.Stage != catalogOperationComplete ||
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
	if len(matching) != 0 || status.Target != host.PreviousTarget || catalogSelectorStatusBundleSHA256(status) != host.PreviousBundleSHA256 {
		return ErrCatalogOperationInvalid
	}
	return nil
}

func cohortHasRolledBackHost(receipt CatalogCohortReceipt, hostID string) bool {
	for _, transition := range receipt.Transitions {
		if transition.Action == "rollback-host" && transition.HostID == hostID {
			return true
		}
	}
	return false
}

func cohortHasSwitchEvidence(receipt CatalogCohortReceipt, hostID, digest string) bool {
	for _, transition := range receipt.Transitions {
		if transition.Action == "reconcile-history" {
			for _, branch := range transition.HistoricalBranches {
				for _, event := range branch.Transitions {
					if (event.Action == "switch" || event.Action == "switch-reconciled") && event.HostID == hostID && event.EvidenceSHA256 == digest {
						return true
					}
				}
			}
		}
		if (transition.Action == "switch" || transition.Action == "switch-reconciled") && transition.HostID == hostID && transition.EvidenceSHA256 == digest {
			return true
		}
	}
	return false
}

func cohortHasRollbackEvidence(receipt CatalogCohortReceipt, hostID, digest string) bool {
	for _, transition := range receipt.Transitions {
		if transition.Action == "reconcile-history" {
			for _, branch := range transition.HistoricalBranches {
				for _, event := range branch.Transitions {
					if event.Action == "rollback-host" && event.HostID == hostID && event.EvidenceSHA256 == digest {
						return true
					}
				}
			}
		}
		if transition.Action == "rollback-host" && transition.HostID == hostID && transition.EvidenceSHA256 == digest {
			return true
		}
	}
	return false
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
// one never-switched or fully rolled-back attempt. It does not mutate service,
// selector, or database state. Ambiguous selector journals, an active service,
// or any retained/resumable work refuse the observation. The successor must
// already be held with fresh four-host preflight and backup evidence.
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
	evidence := CatalogCohortSupersessionEvidence{
		HostID: plan.HostID, SuccessorOperationID: successor.OperationID,
		ExpectedSelector: successor.Hosts[index].PreviousTarget, ObservedSelector: status.Target,
		ServiceActive: active, WorkInventorySHA256: workSHA256, WorkInventoryEmpty: empty,
		ConfigurationSHA256: configurationSHA256, DatabaseSchema: schemaVersion,
		SelectorOperations: selectorOperations,
	}
	if !observationMatchesSuccessor(receipt, successor, evidence) {
		return CatalogCohortSupersessionEvidence{}, ErrCatalogOperationInvalid
	}
	return evidence, nil
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
		encoded, err := json.Marshal(report.WorkInventory)
		if err != nil {
			return "", 0, false, false, err
		}
		digest := sha256.Sum256(encoded)
		return hex.EncodeToString(digest[:]), report.DatabaseSchema, report.WorkInventoryEmpty, report.ServiceActive, nil
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
	var schema int
	if err := database.QueryRowContext(ctx, "PRAGMA user_version").Scan(&schema); err != nil || schema <= 0 {
		return "", 0, false, active, ErrPRDUpgradeGuard
	}
	work, err := readPRDWorkInventory(ctx, database)
	if err != nil {
		return "", 0, false, active, err
	}
	encoded, err := json.Marshal(work)
	if err != nil {
		return "", 0, false, active, err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), schema, work.Empty(), active, nil
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
		observation.ServiceActive || !observation.WorkInventoryEmpty || observation.SelectorOperations != "none" && observation.SelectorOperations != "rolled_back" ||
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
	if switched {
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
		(event.Supersession.SelectorOperations != "none" && event.Supersession.SelectorOperations != "rolled_back") ||
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
	for _, transition := range transitions {
		if transition.Action == "rollback-complete" {
			return true
		}
	}
	return false
}

func cohortHasAnySwitch(receipt CatalogCohortReceipt) bool {
	return cohortHasAnySwitchTransitions(receipt.Transitions)
}

func cohortHasAnySwitchTransitions(transitions []CatalogCohortTransition) bool {
	return len(cohortSwitchedHostsFrom(transitions)) > 0
}

func cohortSwitchedHostsFrom(transitions []CatalogCohortTransition) []string {
	switched := make(map[string]struct{})
	for _, transition := range transitions {
		if transition.Action == "switch" || transition.Action == "switch-reconciled" {
			switched[transition.HostID] = struct{}{}
		}
		if transition.Action == "reconcile-history" {
			for _, branch := range transition.HistoricalBranches {
				for _, event := range branch.Transitions {
					if event.Action == "switch" || event.Action == "switch-reconciled" {
						switched[event.HostID] = struct{}{}
					}
				}
			}
		}
	}
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
