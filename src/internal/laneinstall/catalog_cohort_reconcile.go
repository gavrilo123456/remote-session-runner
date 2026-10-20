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
// receipt from divergent valid pre-switch copies. Every original transition
// chain is retained as a branch. Any copy that records a switch or rollback is
// refused and must use the normal deployment recovery path instead.
func ReconcileCatalogCohortReceiptHistories(receipts []CatalogCohortReceipt, at time.Time) (CatalogCohortReceipt, error) {
	if len(receipts) < 2 || len(receipts) > maxCatalogCohortHistoryBranches || at.IsZero() {
		return CatalogCohortReceipt{}, ErrCatalogOperationInvalid
	}
	base := receipts[0]
	branchesByDigest := make(map[string]CatalogCohortHistoryBranch, len(receipts))
	for _, receipt := range receipts {
		if ValidateCatalogCohortReceipt(receipt) != nil || !catalogCohortStaticIdentityEqual(base, receipt) ||
			(receipt.Stage != CatalogCohortPrepared && receipt.Stage != CatalogCohortHeld && receipt.Stage != CatalogCohortBlocked) ||
			cohortHasAnySwitchTransitions(receipt.Transitions) {
			return CatalogCohortReceipt{}, ErrCatalogOperationInvalid
		}
		for _, transition := range receipt.Transitions {
			if transition.Action == "reconcile-history" {
				return CatalogCohortReceipt{}, ErrCatalogOperationInvalid
			}
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
	reconciledStage := CatalogCohortPrepared
	for _, branch := range branches {
		if branch.Stage != CatalogCohortPrepared {
			reconciledStage = CatalogCohortHeld
			break
		}
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
		(event.ReconciledStage != CatalogCohortPrepared && event.ReconciledStage != CatalogCohortHeld) ||
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
	conservativeStage := CatalogCohortPrepared
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
		if ValidateCatalogCohortReceipt(branchReceipt) != nil ||
			(branch.Stage != CatalogCohortPrepared && branch.Stage != CatalogCohortHeld && branch.Stage != CatalogCohortBlocked) ||
			cohortHasAnySwitchTransitions(branch.Transitions) || !catalogCohortTransitionPrefix(prior, branch.Transitions) {
			return false
		}
		for _, transition := range branch.Transitions {
			if transition.Action == "reconcile-history" {
				return false
			}
		}
		if branch.Stage != CatalogCohortPrepared {
			conservativeStage = CatalogCohortHeld
		}
		if lastDigest != "" && branchDigestOrderKey(branch) <= lastDigest {
			return false
		}
		lastDigest = branchDigestOrderKey(branch)
	}
	return conservativeStage == event.ReconciledStage
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
	if index < 0 || index != len(supersededHosts(receipt.Transitions)) {
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
		observation.ServiceActive || !observation.WorkInventoryEmpty || observation.SelectorOperations != "none" && observation.SelectorOperations != "rolled_back" ||
		!bundleDigestPattern.MatchString(observation.WorkInventorySHA256) || !bundleDigestPattern.MatchString(observation.ConfigurationSHA256) ||
		observation.DatabaseSchema <= 0 {
		return false
	}
	index := cohortHostIndex(successor, observation.HostID)
	if index < 0 || index != len(supersededHosts(receipt.Transitions)) {
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
		event.ResumeStage != "" ||
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
	for _, transition := range transitions {
		if transition.Action == "switch" || transition.Action == "switch-reconciled" {
			return true
		}
	}
	return false
}

func cohortSwitchedHostsFrom(transitions []CatalogCohortTransition) []string {
	var hosts []string
	for _, transition := range transitions {
		if transition.Action == "switch" || transition.Action == "switch-reconciled" {
			hosts = append(hosts, transition.HostID)
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

func supersessionSuccessor(transitions []CatalogCohortTransition) string {
	for _, transition := range transitions {
		if transition.Action == "supersede-host" && transition.Supersession != nil {
			return transition.Supersession.SuccessorOperationID
		}
	}
	return ""
}

func cohortTransitionsHaveSupersession(transitions []CatalogCohortTransition, successorID string) bool {
	return len(supersededHosts(transitions)) == len(catalogCohortSwitchOrder) && supersessionSuccessor(transitions) == successorID &&
		cohortHasAllSupersessionEvidenceFrom(transitions, successorID)
}

func cohortHasAllSupersessionEvidence(receipt CatalogCohortReceipt, successorID string) bool {
	return cohortHasAllSupersessionEvidenceFrom(receipt.Transitions, successorID)
}

func cohortHasAllSupersessionEvidenceFrom(transitions []CatalogCohortTransition, successorID string) bool {
	var evidence []CatalogCohortTransition
	for _, transition := range transitions {
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
