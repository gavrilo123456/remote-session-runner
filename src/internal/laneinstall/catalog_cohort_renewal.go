package laneinstall

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"golang.org/x/sys/unix"

	"remote-session-runner/src/internal/config"
)

const catalogCohortContinuationFormatVersion = 1

// CatalogCohortContinuationWindow is the single bounded extension for an
// expired selected-lane cohort. Host selector effects still require a new
// two-minute local preflight.
const CatalogCohortContinuationWindow = 4 * time.Hour

const catalogCohortContinuationObservationMaxAge = 2 * time.Minute

// CatalogCohortContinuationHostObservation is a fresh, read-only host proof
// captured after the original cohort evidence expired. It deliberately lives
// outside CatalogCohortReceipt so the sealed Runner runtime and historical
// receipt timestamps remain unchanged.
type CatalogCohortContinuationHostObservation struct {
	FormatVersion              int                `json:"format_version"`
	OperationID                string             `json:"operation_id"`
	HostID                     string             `json:"host_id"`
	Lane                       config.ReleaseLane `json:"lane"`
	ReleaseID                  string             `json:"release_id"`
	ReleaseSHA256              string             `json:"release_sha256"`
	BaseTransitionCount        int                `json:"base_transition_count"`
	BaseTransitionSHA256       string             `json:"base_transition_sha256"`
	SelectorKind               string             `json:"selector_kind"`
	SelectorTarget             string             `json:"selector_target,omitempty"`
	SelectorBundleSHA256       string             `json:"selector_bundle_sha256"`
	SelectorJournalOperationID string             `json:"selector_journal_operation_id,omitempty"`
	SelectorJournalSHA256      string             `json:"selector_journal_sha256,omitempty"`
	ConfigurationSHA256        string             `json:"configuration_sha256"`
	DatabaseSchema             int                `json:"database_schema"`
	WorkInventorySHA256        string             `json:"work_inventory_sha256"`
	WorkInventoryEmpty         bool               `json:"work_inventory_empty"`
	BackupDirectory            string             `json:"backup_directory"`
	BackupManifestSHA256       string             `json:"backup_manifest_sha256"`
	AvailableBytes             uint64             `json:"available_bytes"`
	RequiredBytes              uint64             `json:"required_bytes"`
	ServiceStopped             bool               `json:"service_stopped"`
	AdmissionHeld              bool               `json:"admission_held"`
	AdmissionOperationID       string             `json:"admission_operation_id"`
	ObservedAt                 string             `json:"observed_at"`
}

// CatalogCohortContinuationRenewal is an append-only, operation-specific
// evidence file stored beside (never inside) catalog-cohorts. Receipt schema,
// transition timestamps, and the selected release bytes are not modified.
type CatalogCohortContinuationRenewal struct {
	FormatVersion        int                                        `json:"format_version"`
	RenewalID            string                                     `json:"renewal_id"`
	OperationID          string                                     `json:"operation_id"`
	Lane                 config.ReleaseLane                         `json:"lane"`
	ReleaseID            string                                     `json:"release_id"`
	ReleaseSHA256        string                                     `json:"release_sha256"`
	BaseTransitionCount  int                                        `json:"base_transition_count"`
	BaseTransitionSHA256 string                                     `json:"base_transition_sha256"`
	VerifiedAt           string                                     `json:"verified_at"`
	ExpiresAt            string                                     `json:"expires_at"`
	Hosts                []CatalogCohortContinuationHostObservation `json:"hosts"`
	SHA256               string                                     `json:"sha256"`
}

// ObserveCatalogCohortContinuationHost validates the current selected-lane host without
// changing its service, selector, database, receipt, or work state.
func ObserveCatalogCohortContinuationHost(ctx context.Context, plan Plan, operationID string) (CatalogCohortContinuationHostObservation, error) {
	if validateCatalogPlan(plan) != nil || !validCatalogOperationID(operationID) {
		return CatalogCohortContinuationHostObservation{}, ErrCatalogOperationInvalid
	}
	receipt, err := ReadCatalogCohortReceipt(plan, operationID)
	if err != nil || !catalogCohortContinuationEligible(receipt) {
		return CatalogCohortContinuationHostObservation{}, ErrCatalogOperationInvalid
	}
	now := time.Now().UTC()
	expiresAt, ok := catalogCohortEvidenceExpiresAt(receipt)
	if !ok || now.Before(expiresAt) || cohortEvidenceFresh(receipt, now.Format(time.RFC3339Nano)) {
		return CatalogCohortContinuationHostObservation{}, ErrCatalogOperationInvalid
	}
	if err := CatalogCohortAdmissionHoldMatches(plan, operationID); err != nil {
		return CatalogCohortContinuationHostObservation{}, err
	}
	if err := ValidateCatalogCohortHistoryLocalState(ctx, plan, receipt); err != nil {
		return CatalogCohortContinuationHostObservation{}, err
	}
	root, err := DefaultCatalogRootForPlan(plan)
	if err != nil {
		return CatalogCohortContinuationHostObservation{}, err
	}
	layout, err := catalogSelectorLayoutForPlan(plan, root)
	if err != nil {
		return CatalogCohortContinuationHostObservation{}, err
	}
	index := cohortHostIndex(receipt, plan.HostID)
	if index < 0 {
		return CatalogCohortContinuationHostObservation{}, ErrCatalogOperationInvalid
	}
	host := receipt.Hosts[index]
	var active bool
	var work CatalogWorkInventory
	var configurationSHA string
	var schema int
	if plan.Lane == config.ReleaseLanePRD {
		prdPlan, planErr := BuildPRDUpgradePlan(plan.HostID)
		service, serviceErr := NewPRDServiceController()
		if planErr != nil || serviceErr != nil {
			return CatalogCohortContinuationHostObservation{}, errors.Join(planErr, serviceErr)
		}
		active, err = service.Status(ctx, prdPlan)
		if err == nil && !active {
			var proof prdCatalogProof
			proof, err = readPRDCatalogProof(ctx, prdPlan, layout)
			if err == nil {
				work, configurationSHA, schema = proof.work, proof.configurationSHA256, proof.schema
			}
		}
	} else {
		service, serviceErr := NewSimpleServiceController()
		if serviceErr != nil {
			return CatalogCohortContinuationHostObservation{}, serviceErr
		}
		var report CatalogWorkInventoryReport
		report, err = ReadCatalogWorkInventory(ctx, plan, service)
		if err == nil {
			active, work, configurationSHA, schema = report.ServiceActive, report.WorkInventory, report.ConfigurationSHA256, report.DatabaseSchema
		}
	}
	if err != nil || active || !work.Empty() || schema != host.SchemaVersion || configurationSHA != host.ConfigurationSHA256 ||
		work.SHA256() != host.WorkInventorySHA256 {
		return CatalogCohortContinuationHostObservation{}, ErrCatalogOperationInvalid
	}
	backupReference := catalogBackupReference{Directory: host.BackupDirectory, ManifestSHA256: host.BackupManifestSHA256,
		CohortOperationID: receipt.OperationID}
	if err := validateCatalogBackupReference(layout, backupReference, host.PreviousTarget, host.SchemaVersion); err != nil ||
		backupManifestSHA256(host.BackupDirectory) != host.BackupManifestSHA256 {
		return CatalogCohortContinuationHostObservation{}, errors.Join(ErrSimpleBackupRequired, err)
	}
	if plan.Lane == config.ReleaseLanePRD {
		prdPlan, planErr := BuildPRDUpgradePlan(plan.HostID)
		manifest, loadErr := loadPRDBackupManifest(prdPlan, host.BackupDirectory)
		if planErr != nil || loadErr != nil || manifest.BundleSHA256 != host.Target.BundleSHA256 {
			return CatalogCohortContinuationHostObservation{}, errors.Join(ErrSimpleBackupRequired, planErr, loadErr)
		}
		if err := validateCatalogCohortContinuationBackupTargets(receipt, plan.HostID, manifest); err != nil {
			return CatalogCohortContinuationHostObservation{}, errors.Join(ErrSimpleBackupRequired, err)
		}
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(layout.backupRoot, &stat); err != nil || stat.Bsize < 0 || stat.Bavail < 0 {
		return CatalogCohortContinuationHostObservation{}, errors.Join(ErrCatalogInsufficientSpace, err)
	}
	available := uint64(stat.Bavail) * uint64(stat.Bsize)
	required, requiredErr := catalogCohortContinuationRequiredBytesForLane(plan.Lane, host.RequiredBytes)
	if requiredErr != nil || available < required {
		return CatalogCohortContinuationHostObservation{}, errors.Join(ErrCatalogInsufficientSpace, requiredErr)
	}
	status, err := inspectCatalogSelector(layout)
	if err != nil {
		return CatalogCohortContinuationHostObservation{}, err
	}
	journalID, journalSHA, err := catalogCohortContinuationSelectorJournal(layout, receipt)
	if err != nil {
		return CatalogCohortContinuationHostObservation{}, err
	}
	observation := CatalogCohortContinuationHostObservation{
		FormatVersion: catalogCohortContinuationFormatVersion, OperationID: receipt.OperationID, HostID: plan.HostID,
		Lane: receipt.Lane, ReleaseID: receipt.ReleaseID, ReleaseSHA256: receipt.ReleaseSHA256,
		BaseTransitionCount: len(receipt.Transitions), BaseTransitionSHA256: lastCohortTransitionDigest(receipt.Transitions),
		SelectorKind: status.Kind, SelectorTarget: status.Target, SelectorBundleSHA256: catalogSelectorStatusBundleSHA256(status),
		SelectorJournalOperationID: journalID, SelectorJournalSHA256: journalSHA,
		ConfigurationSHA256: configurationSHA, DatabaseSchema: schema,
		WorkInventorySHA256: work.SHA256(), WorkInventoryEmpty: work.Empty(),
		BackupDirectory: host.BackupDirectory, BackupManifestSHA256: host.BackupManifestSHA256,
		AvailableBytes: available, RequiredBytes: required,
		ServiceStopped: true, AdmissionHeld: true, AdmissionOperationID: operationID,
		ObservedAt: now.Format(time.RFC3339Nano),
	}
	if err := validateCatalogCohortContinuationHostObservation(observation, receipt); err != nil {
		return CatalogCohortContinuationHostObservation{}, err
	}
	return observation, nil
}

// validateCatalogCohortContinuationBackupTargets keeps the original legacy
// installation bound to its verified backup before a host switches. After a
// recorded switch, live service artifacts are expected to differ: the caller
// has already validated the selector journal and current catalog service
// artifacts through ValidateCatalogCohortHistoryLocalState, while
// loadPRDBackupManifest has verified every retained backup copy. Requiring the
// installed legacy files to remain byte-identical would reject a valid switch.
func validateCatalogCohortContinuationBackupTargets(receipt CatalogCohortReceipt, hostID string, manifest PRDBackupManifest) error {
	if !cohortContainsHost(receipt, hostID) {
		return ErrCatalogOperationInvalid
	}
	if cohortHasSwitched(receipt, hostID) {
		return nil
	}
	return verifyPRDBackupTargets(manifest, true)
}

// NewCatalogCohortContinuationRenewal creates one bounded continuation for an
// expired held/switching/verifying receipt with a legal ordered switch prefix
// and four mutually consistent, recent host observations.
func NewCatalogCohortContinuationRenewal(receipt CatalogCohortReceipt,
	observations []CatalogCohortContinuationHostObservation, verifiedAt time.Time) (CatalogCohortContinuationRenewal, error) {
	if ValidateCatalogCohortReceipt(receipt) != nil || !catalogCohortContinuationEligible(receipt) ||
		len(observations) != len(catalogCohortSwitchOrder) || verifiedAt.IsZero() {
		return CatalogCohortContinuationRenewal{}, ErrCatalogOperationInvalid
	}
	verifiedAt = verifiedAt.UTC()
	if cohortEvidenceFresh(receipt, verifiedAt.Format(time.RFC3339Nano)) {
		return CatalogCohortContinuationRenewal{}, ErrCatalogOperationInvalid
	}
	expiresAt, ok := catalogCohortEvidenceExpiresAt(receipt)
	if !ok || verifiedAt.Before(expiresAt) {
		return CatalogCohortContinuationRenewal{}, ErrCatalogOperationInvalid
	}
	base := receipt
	baseTransitionCount := len(receipt.Transitions)
	baseTransitionSHA := lastCohortTransitionDigest(receipt.Transitions)
	if baseTransitionCount == 0 || !bundleDigestPattern.MatchString(baseTransitionSHA) {
		return CatalogCohortContinuationRenewal{}, ErrCatalogOperationInvalid
	}
	for index, hostID := range catalogCohortSwitchOrder {
		observation := observations[index]
		if observation.HostID != hostID || observation.OperationID != receipt.OperationID ||
			observation.BaseTransitionCount != baseTransitionCount || observation.BaseTransitionSHA256 != baseTransitionSHA ||
			validateCatalogCohortContinuationHostObservation(observation, base) != nil {
			return CatalogCohortContinuationRenewal{}, ErrCatalogOperationInvalid
		}
		observedAt, err := time.Parse(time.RFC3339Nano, observation.ObservedAt)
		if err != nil || observedAt.Before(expiresAt) || observedAt.After(verifiedAt) ||
			verifiedAt.Sub(observedAt) > catalogCohortContinuationObservationMaxAge {
			return CatalogCohortContinuationRenewal{}, ErrCatalogOperationInvalid
		}
	}
	renewalID, err := newCatalogOperationID()
	if err != nil {
		return CatalogCohortContinuationRenewal{}, err
	}
	renewal := CatalogCohortContinuationRenewal{
		FormatVersion: catalogCohortContinuationFormatVersion, RenewalID: renewalID, OperationID: receipt.OperationID,
		Lane: receipt.Lane, ReleaseID: receipt.ReleaseID, ReleaseSHA256: receipt.ReleaseSHA256,
		BaseTransitionCount: baseTransitionCount, BaseTransitionSHA256: baseTransitionSHA,
		VerifiedAt: verifiedAt.Format(time.RFC3339Nano), ExpiresAt: verifiedAt.Add(CatalogCohortContinuationWindow).Format(time.RFC3339Nano),
		Hosts: append([]CatalogCohortContinuationHostObservation(nil), observations...),
	}
	renewal.SHA256 = catalogCohortContinuationDigest(renewal)
	if err := ValidateCatalogCohortContinuationRenewal(renewal); err != nil {
		return CatalogCohortContinuationRenewal{}, err
	}
	return renewal, nil
}

// ValidateCatalogCohortContinuationRenewal validates the self-contained
// evidence format and its fixed four-hour lifetime.
func ValidateCatalogCohortContinuationRenewal(renewal CatalogCohortContinuationRenewal) error {
	if renewal.FormatVersion != catalogCohortContinuationFormatVersion || !validCatalogOperationID(renewal.RenewalID) ||
		!validCatalogOperationID(renewal.OperationID) || (renewal.Lane != config.ReleaseLaneDev && renewal.Lane != config.ReleaseLaneQAS && renewal.Lane != config.ReleaseLanePRD) ||
		!validCatalogReleaseID(renewal.ReleaseID) ||
		!bundleDigestPattern.MatchString(renewal.ReleaseSHA256) || renewal.BaseTransitionCount <= 0 ||
		!bundleDigestPattern.MatchString(renewal.BaseTransitionSHA256) || len(renewal.Hosts) != len(catalogCohortSwitchOrder) ||
		!validCohortTimestamp(renewal.VerifiedAt) || !validCohortTimestamp(renewal.ExpiresAt) ||
		!renewal.ExpiresAtEqualFixedWindow() || renewal.SHA256 != catalogCohortContinuationDigest(renewal) {
		return ErrCatalogOperationInvalid
	}
	verifiedAt, _ := time.Parse(time.RFC3339Nano, renewal.VerifiedAt)
	for index, hostID := range catalogCohortSwitchOrder {
		observation := renewal.Hosts[index]
		if observation.HostID != hostID || observation.OperationID != renewal.OperationID || observation.Lane != renewal.Lane ||
			observation.ReleaseID != renewal.ReleaseID || observation.ReleaseSHA256 != renewal.ReleaseSHA256 ||
			observation.BaseTransitionCount != renewal.BaseTransitionCount || observation.BaseTransitionSHA256 != renewal.BaseTransitionSHA256 ||
			validateCatalogCohortContinuationHostObservationFields(observation) != nil {
			return ErrCatalogOperationInvalid
		}
		observedAt, err := time.Parse(time.RFC3339Nano, observation.ObservedAt)
		if err != nil || observedAt.After(verifiedAt) || verifiedAt.Sub(observedAt) > catalogCohortContinuationObservationMaxAge {
			return ErrCatalogOperationInvalid
		}
	}
	return nil
}

// ValidateCatalogCohortContinuationRenewalForReceipt binds a continuation to
// the exact historical transition prefix and the still-current operation.
func ValidateCatalogCohortContinuationRenewalForReceipt(renewal CatalogCohortContinuationRenewal,
	receipt CatalogCohortReceipt, now time.Time) error {
	if ValidateCatalogCohortContinuationRenewal(renewal) != nil || ValidateCatalogCohortReceipt(receipt) != nil ||
		receipt.OperationID != renewal.OperationID || receipt.Lane != renewal.Lane || receipt.ReleaseID != renewal.ReleaseID ||
		receipt.ReleaseSHA256 != renewal.ReleaseSHA256 || len(receipt.Transitions) < renewal.BaseTransitionCount || now.IsZero() {
		return ErrCatalogOperationInvalid
	}
	base := receipt
	base.Transitions = append([]CatalogCohortTransition(nil), receipt.Transitions[:renewal.BaseTransitionCount]...)
	if lastCohortTransitionDigest(base.Transitions) != renewal.BaseTransitionSHA256 {
		return ErrCatalogOperationInvalid
	}
	base.Stage, _ = replayCatalogCohortTransitions(base)
	base.UpdatedAt = base.Transitions[len(base.Transitions)-1].At
	if ValidateCatalogCohortReceipt(base) != nil || !catalogCohortContinuationEligible(base) {
		return ErrCatalogOperationInvalid
	}
	verifiedAt, _ := time.Parse(time.RFC3339Nano, renewal.VerifiedAt)
	expiresAt, _ := time.Parse(time.RFC3339Nano, renewal.ExpiresAt)
	if now.Before(verifiedAt) || now.After(expiresAt) || cohortEvidenceFresh(base, verifiedAt.Format(time.RFC3339Nano)) {
		return ErrCatalogOperationInvalid
	}
	oldExpiry, ok := catalogCohortEvidenceExpiresAt(base)
	if !ok || verifiedAt.Before(oldExpiry) {
		return ErrCatalogOperationInvalid
	}
	for index, observation := range renewal.Hosts {
		observedAt, observedErr := time.Parse(time.RFC3339Nano, observation.ObservedAt)
		if validateCatalogCohortContinuationHostObservation(observation, base) != nil || observation.HostID != catalogCohortSwitchOrder[index] ||
			observedErr != nil || observedAt.Before(oldExpiry) || observedAt.After(verifiedAt) ||
			verifiedAt.Sub(observedAt) > catalogCohortContinuationObservationMaxAge {
			return ErrCatalogOperationInvalid
		}
	}
	return nil
}

func catalogCohortContinuationRequiredBytes(selectorReserve uint64) (uint64, error) {
	if selectorReserve != prdCatalogSelectorHeadroom || selectorReserve > ^uint64(0)-prdCatalogOperatingBufferBytes {
		return 0, ErrCatalogOperationInvalid
	}
	return selectorReserve + prdCatalogOperatingBufferBytes, nil
}

func catalogCohortContinuationRequiredBytesForLane(lane config.ReleaseLane, recorded uint64) (uint64, error) {
	minimum := prdCatalogOperatingBufferBytes
	if lane == config.ReleaseLanePRD {
		minimum += prdCatalogSelectorHeadroom
	} else if lane != config.ReleaseLaneDev && lane != config.ReleaseLaneQAS {
		return 0, ErrCatalogOperationInvalid
	}
	if recorded > minimum {
		minimum = recorded
	}
	return minimum, nil
}

func catalogCohortContinuationEligible(receipt CatalogCohortReceipt) bool {
	if ValidateCatalogCohortReceipt(receipt) != nil ||
		(receipt.Stage != CatalogCohortHeld && receipt.Stage != CatalogCohortSwitching && receipt.Stage != CatalogCohortVerifying) {
		return false
	}
	switched := cohortSwitchedHosts(receipt)
	if len(switched) > len(catalogCohortSwitchOrder) {
		return false
	}
	for index, host := range switched {
		if host != catalogCohortSwitchOrder[index] {
			return false
		}
	}
	return receipt.Stage != CatalogCohortVerifying || len(switched) == len(catalogCohortSwitchOrder)
}

// ReadCatalogCohortContinuationRenewal loads the separate owner-only renewal
// file for one PRD operation. The sealed receipt parser never reads this path.
func ReadCatalogCohortContinuationRenewal(plan Plan, operationID string) (CatalogCohortContinuationRenewal, error) {
	if validateCatalogPlan(plan) != nil || !validCatalogOperationID(operationID) {
		return CatalogCohortContinuationRenewal{}, ErrCatalogOperationInvalid
	}
	path := catalogCohortContinuationPath(plan, operationID)
	var renewal CatalogCohortContinuationRenewal
	if err := readCanonicalOwnerJSON(path, 1<<20, &renewal); err != nil || renewal.OperationID != operationID || ValidateCatalogCohortContinuationRenewal(renewal) != nil {
		return CatalogCohortContinuationRenewal{}, ErrCatalogOperationInvalid
	}
	return renewal, nil
}

// LoadCatalogCohortContinuationHostObservationFile accepts only canonical,
// owner-only observation reports exchanged over the verified host route.
func LoadCatalogCohortContinuationHostObservationFile(path string) (CatalogCohortContinuationHostObservation, error) {
	var observation CatalogCohortContinuationHostObservation
	if err := readCanonicalOwnerJSON(path, 1<<20, &observation); err != nil ||
		validateCatalogCohortContinuationHostObservationFields(observation) != nil {
		return CatalogCohortContinuationHostObservation{}, ErrCatalogOperationInvalid
	}
	return observation, nil
}

// WriteCatalogCohortContinuationHostObservationFile writes a task-owned,
// canonical observation in a private directory for transfer to the Mac
// coordinator. It never writes into the cohort receipt directory.
func WriteCatalogCohortContinuationHostObservationFile(path string, observation CatalogCohortContinuationHostObservation) error {
	if validateCatalogCohortContinuationHostObservationFields(observation) != nil {
		return ErrCatalogOperationInvalid
	}
	data, err := canonicalJSON(observation)
	if err != nil {
		return ErrCatalogOperationInvalid
	}
	return writeCanonicalOwnerJSONNoReplace(path, data)
}

// LoadCatalogCohortContinuationRenewalFile loads a canonical owner-only
// renewal received from the Mac coordinator.
func LoadCatalogCohortContinuationRenewalFile(path string) (CatalogCohortContinuationRenewal, error) {
	var renewal CatalogCohortContinuationRenewal
	if err := readCanonicalOwnerJSON(path, 1<<20, &renewal); err != nil || ValidateCatalogCohortContinuationRenewal(renewal) != nil {
		return CatalogCohortContinuationRenewal{}, ErrCatalogOperationInvalid
	}
	return renewal, nil
}

// catalogCohortContinuationRenewalForSwitch reads the separate record used
// only by guarded PRD selector mutation. Its path is outside catalog-cohorts,
// so older Runner processes continue to parse the unchanged receipt format.
func readCatalogCohortContinuationRenewalAt(layout catalogSelectorLayout, operationID string) (CatalogCohortContinuationRenewal, error) {
	if !validCatalogOperationID(operationID) {
		return CatalogCohortContinuationRenewal{}, ErrCatalogOperationInvalid
	}
	path := filepath.Join(filepath.Dir(layout.operationRoot), "catalog-cohort-continuations", operationID+".json")
	var renewal CatalogCohortContinuationRenewal
	if err := readCanonicalOwnerJSON(path, 1<<20, &renewal); err != nil || renewal.OperationID != operationID || ValidateCatalogCohortContinuationRenewal(renewal) != nil {
		return CatalogCohortContinuationRenewal{}, ErrCatalogOperationInvalid
	}
	return renewal, nil
}

// WriteCatalogCohortContinuationRenewal stores the immutable renewal beside
// the unchanged receipt tree. Repeating the exact same write is idempotent.
func WriteCatalogCohortContinuationRenewal(plan Plan, renewal CatalogCohortContinuationRenewal) (string, error) {
	if validateCatalogPlan(plan) != nil || plan.Lane != renewal.Lane || ValidateCatalogCohortContinuationRenewal(renewal) != nil {
		return "", ErrCatalogOperationInvalid
	}
	path := catalogCohortContinuationPath(plan, renewal.OperationID)
	data, err := canonicalJSON(renewal)
	if err != nil {
		return "", ErrCatalogOperationInvalid
	}
	if err := writeCanonicalOwnerJSONNoReplace(path, data); err != nil {
		return "", err
	}
	return path, nil
}

// SyncCatalogCohortContinuationRenewal installs the same immutable evidence
// on one host only after the local stopped/empty/hold/backup/journal checks
// still match that host's original observation.
func SyncCatalogCohortContinuationRenewal(ctx context.Context, plan Plan, renewal CatalogCohortContinuationRenewal) (string, error) {
	receipt, err := ReadCatalogCohortReceipt(plan, renewal.OperationID)
	if err != nil || ValidateCatalogCohortContinuationRenewalForReceipt(renewal, receipt, time.Now().UTC()) != nil {
		return "", ErrCatalogOperationInvalid
	}
	observation, err := ObserveCatalogCohortContinuationHost(ctx, plan, renewal.OperationID)
	if err != nil {
		return "", err
	}
	expected, ok := catalogCohortContinuationObservationForHost(renewal, plan.HostID)
	if !ok || !SameCatalogCohortContinuationHostState(expected, observation) {
		return "", ErrCatalogOperationInvalid
	}
	return WriteCatalogCohortContinuationRenewal(plan, renewal)
}

// VerifyCatalogCohortContinuationRenewal rechecks the stored renewal against
// current local PRD state without changing it.
func VerifyCatalogCohortContinuationRenewal(ctx context.Context, plan Plan, operationID string) (CatalogCohortContinuationRenewal, error) {
	renewal, err := ReadCatalogCohortContinuationRenewal(plan, operationID)
	if err != nil {
		return CatalogCohortContinuationRenewal{}, err
	}
	receipt, err := ReadCatalogCohortReceipt(plan, operationID)
	if err != nil || ValidateCatalogCohortContinuationRenewalForReceipt(renewal, receipt, time.Now().UTC()) != nil {
		return CatalogCohortContinuationRenewal{}, ErrCatalogOperationInvalid
	}
	observation, err := ObserveCatalogCohortContinuationHost(ctx, plan, operationID)
	if err != nil {
		return CatalogCohortContinuationRenewal{}, err
	}
	expected, ok := catalogCohortContinuationObservationForHost(renewal, plan.HostID)
	if !ok || !SameCatalogCohortContinuationHostState(expected, observation) {
		return CatalogCohortContinuationRenewal{}, ErrCatalogOperationInvalid
	}
	return renewal, nil
}

func validateCatalogCohortContinuationHostObservation(observation CatalogCohortContinuationHostObservation, receipt CatalogCohortReceipt) error {
	if validateCatalogCohortContinuationHostObservationFields(observation) != nil || ValidateCatalogCohortReceipt(receipt) != nil ||
		observation.OperationID != receipt.OperationID || observation.Lane != receipt.Lane || observation.ReleaseID != receipt.ReleaseID ||
		observation.ReleaseSHA256 != receipt.ReleaseSHA256 || observation.BaseTransitionCount != len(receipt.Transitions) ||
		observation.BaseTransitionSHA256 != lastCohortTransitionDigest(receipt.Transitions) {
		return ErrCatalogOperationInvalid
	}
	index := cohortHostIndex(receipt, observation.HostID)
	if index < 0 {
		return ErrCatalogOperationInvalid
	}
	host := receipt.Hosts[index]
	if observation.ConfigurationSHA256 != host.ConfigurationSHA256 || observation.DatabaseSchema != host.SchemaVersion ||
		observation.WorkInventorySHA256 != host.WorkInventorySHA256 || observation.BackupDirectory != host.BackupDirectory ||
		observation.BackupManifestSHA256 != host.BackupManifestSHA256 {
		return ErrCatalogOperationInvalid
	}
	if cohortHasSwitched(receipt, observation.HostID) {
		plan, err := BuildCatalogPlan(observation.HostID, receipt.Lane)
		if err != nil {
			return ErrCatalogOperationInvalid
		}
		root, err := DefaultCatalogRootForPlan(plan)
		wantTarget := filepath.Join(root, receipt.ReleaseID, observation.HostID)
		if err != nil || observation.SelectorKind != "catalog" || observation.SelectorTarget != wantTarget ||
			observation.SelectorBundleSHA256 != host.Target.BundleSHA256 || observation.SelectorJournalOperationID == "" ||
			!validCatalogOperationID(observation.SelectorJournalOperationID) || !bundleDigestPattern.MatchString(observation.SelectorJournalSHA256) ||
			!cohortHasSwitchEvidence(receipt, observation.HostID, observation.SelectorJournalSHA256) {
			return ErrCatalogOperationInvalid
		}
	} else {
		status := CatalogSelectorStatus{
			HostID: observation.HostID, Lane: observation.Lane, Kind: observation.SelectorKind, Target: observation.SelectorTarget,
			Bundle: SimpleBundleManifest{BundleSHA256: observation.SelectorBundleSHA256},
		}
		if observation.SelectorKind == "catalog" {
			status.Release = &CatalogReleaseStatus{Identity: CatalogReleaseIdentity{BundleSHA256: observation.SelectorBundleSHA256}}
		}
		if !catalogCohortPreviousSelectorMatches(receipt.Lane, host, status) || observation.SelectorJournalOperationID != "" || observation.SelectorJournalSHA256 != "" {
			return ErrCatalogOperationInvalid
		}
	}
	return nil
}

func validateCatalogCohortContinuationHostObservationFields(observation CatalogCohortContinuationHostObservation) error {
	if observation.FormatVersion != catalogCohortContinuationFormatVersion || !validCatalogOperationID(observation.OperationID) ||
		!containsString(catalogCohortSwitchOrder, observation.HostID) ||
		(observation.Lane != config.ReleaseLaneDev && observation.Lane != config.ReleaseLaneQAS && observation.Lane != config.ReleaseLanePRD) ||
		!validCatalogReleaseID(observation.ReleaseID) || !bundleDigestPattern.MatchString(observation.ReleaseSHA256) ||
		observation.BaseTransitionCount <= 0 || !bundleDigestPattern.MatchString(observation.BaseTransitionSHA256) ||
		(observation.SelectorKind != "catalog" && observation.SelectorKind != "legacy" && observation.SelectorKind != "unselected") ||
		!bundleDigestPattern.MatchString(observation.SelectorBundleSHA256) ||
		!bundleDigestPattern.MatchString(observation.ConfigurationSHA256) || observation.DatabaseSchema <= 0 ||
		!bundleDigestPattern.MatchString(observation.WorkInventorySHA256) || !observation.WorkInventoryEmpty ||
		!filepath.IsAbs(observation.BackupDirectory) || filepath.Clean(observation.BackupDirectory) != observation.BackupDirectory ||
		!bundleDigestPattern.MatchString(observation.BackupManifestSHA256) || !observation.ServiceStopped || !observation.AdmissionHeld ||
		observation.AdmissionOperationID != observation.OperationID || !validCohortTimestamp(observation.ObservedAt) {
		return ErrCatalogOperationInvalid
	}
	required, err := catalogCohortContinuationRequiredBytesForLane(observation.Lane, observation.RequiredBytes)
	if err != nil || required != observation.RequiredBytes || observation.AvailableBytes < required {
		return ErrCatalogOperationInvalid
	}
	return nil
}

func catalogCohortContinuationSelectorJournal(layout catalogSelectorLayout, receipt CatalogCohortReceipt) (string, string, error) {
	entries, err := os.ReadDir(layout.operationRoot)
	if errors.Is(err, os.ErrNotExist) {
		entries = nil
	} else if err != nil {
		return "", "", err
	}
	var matches []CatalogSelectorOperation
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := entry.Name()[:len(entry.Name())-len(filepath.Ext(entry.Name()))]
		operation, readErr := readCatalogSelectorOperation(layout.operationRoot, id)
		if readErr != nil {
			return "", "", ErrCatalogOperationInvalid
		}
		if operation.CohortOperationID == receipt.OperationID {
			matches = append(matches, operation)
		}
	}
	if cohortHasSwitched(receipt, layout.hostID) {
		if len(matches) != 1 || matches[0].Stage != catalogOperationComplete {
			return "", "", ErrCatalogOperationInvalid
		}
		digest, err := catalogSelectorOperationDigest(layout, matches[0].OperationID)
		if err != nil || !cohortHasSwitchEvidence(receipt, layout.hostID, digest) {
			return "", "", ErrCatalogOperationInvalid
		}
		return matches[0].OperationID, digest, nil
	}
	if len(matches) != 0 {
		return "", "", ErrCatalogOperationInvalid
	}
	return "", "", nil
}

func catalogCohortContinuationDigest(renewal CatalogCohortContinuationRenewal) string {
	renewal.SHA256 = ""
	data, err := json.Marshal(renewal)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func (renewal CatalogCohortContinuationRenewal) ExpiresAtEqualFixedWindow() bool {
	verifiedAt, err := time.Parse(time.RFC3339Nano, renewal.VerifiedAt)
	if err != nil {
		return false
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, renewal.ExpiresAt)
	return err == nil && expiresAt.Equal(verifiedAt.Add(CatalogCohortContinuationWindow))
}

func catalogCohortContinuationPath(plan Plan, operationID string) string {
	return filepath.Join(plan.Namespace.Staging, "catalog-cohort-continuations", operationID+".json")
}

// SameCatalogCohortContinuationHostState compares the stable host identity and
// proof fields while ignoring capture time and free-space counter movement.
// Callers obtain a fresh observation first, which independently enforces the
// current minimum-capacity requirement.
func SameCatalogCohortContinuationHostState(expected, observed CatalogCohortContinuationHostObservation) bool {
	expected.ObservedAt = ""
	observed.ObservedAt = ""
	expected.AvailableBytes = 0
	observed.AvailableBytes = 0
	return reflect.DeepEqual(expected, observed)
}

func catalogCohortContinuationObservationForHost(renewal CatalogCohortContinuationRenewal, hostID string) (CatalogCohortContinuationHostObservation, bool) {
	for _, observation := range renewal.Hosts {
		if observation.HostID == hostID {
			return observation, true
		}
	}
	return CatalogCohortContinuationHostObservation{}, false
}

func canonicalJSON(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func writeCanonicalOwnerJSONNoReplace(path string, data []byte) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || len(data) == 0 || len(data) > 1<<20 {
		return ErrCatalogOperationInvalid
	}
	root := filepath.Dir(path)
	if err := ensureCatalogPrivateDirectory(root); err != nil {
		return err
	}
	if existing, err := os.ReadFile(path); err == nil {
		if !bytes.Equal(existing, data) || validateOwnerOnlyJSONFile(path, 1<<20) != nil {
			return ErrCatalogOperationInvalid
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary, err := os.CreateTemp(root, ".continuation-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Link(temporaryPath, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			if existing, readErr := os.ReadFile(path); readErr == nil && bytes.Equal(existing, data) && validateOwnerOnlyJSONFile(path, 1<<20) == nil {
				return nil
			}
		}
		return err
	}
	return syncSimpleDirectory(root)
}

func readCanonicalOwnerJSON(path string, maxBytes int64, target any) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || target == nil ||
		validateExistingPrivateDirectory(filepath.Dir(path)) != nil || validateOwnerOnlyJSONFile(path, maxBytes) != nil {
		return ErrCatalogOperationInvalid
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return ErrCatalogOperationInvalid
	}
	canonical, err := canonicalJSON(target)
	if err != nil || !bytes.Equal(data, canonical) {
		return ErrCatalogOperationInvalid
	}
	return nil
}

func validateOwnerOnlyJSONFile(path string, maxBytes int64) error {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 ||
		info.Size() < 1 || info.Size() > maxBytes || !catalogFileHasSingleLink(info) {
		return ErrCatalogOperationInvalid
	}
	var stat unix.Stat_t
	if unix.Lstat(path, &stat) != nil || stat.Uid != uint32(os.Getuid()) {
		return ErrCatalogOperationInvalid
	}
	return nil
}
