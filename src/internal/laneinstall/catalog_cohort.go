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
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/store"
)

const catalogCohortFormatVersion = 1
const catalogCohortEvidenceMaxAge = 10 * time.Minute

var catalogCohortSwitchOrder = []string{"linux-host", "sandbox-host", "factory-sandbox", "mac-control-plane"}

type CatalogCohortStage string

const (
	CatalogCohortPrepared    CatalogCohortStage = "prepared"
	CatalogCohortHeld        CatalogCohortStage = "held"
	CatalogCohortSwitching   CatalogCohortStage = "switching"
	CatalogCohortVerifying   CatalogCohortStage = "verifying"
	CatalogCohortComplete    CatalogCohortStage = "complete"
	CatalogCohortBlocked     CatalogCohortStage = "blocked"
	CatalogCohortRollingBack CatalogCohortStage = "rolling_back"
	CatalogCohortRolledBack  CatalogCohortStage = "rolled_back"
	CatalogCohortReleased    CatalogCohortStage = "released"
	CatalogCohortSuperseded  CatalogCohortStage = "superseded"
)

type CatalogCohortFenceProof struct {
	Scope          string `json:"scope"`
	Control        string `json:"control"`
	Owner          string `json:"owner"`
	Paused         bool   `json:"paused"`
	NoNewAccepted  bool   `json:"no_new_accepted"`
	EvidenceSHA256 string `json:"evidence_sha256"`
	VerifiedAt     string `json:"verified_at"`
}

type CatalogCohortHostPreflight struct {
	HostID                   string                              `json:"host_id"`
	ServiceActiveAtPreflight bool                                `json:"service_active_at_preflight"`
	PreviousTarget           string                              `json:"previous_target,omitempty"`
	PreviousBundleSHA256     string                              `json:"previous_bundle_sha256,omitempty"`
	PreviousReadSchemaMax    int                                 `json:"previous_read_schema_max,omitempty"`
	LegacyRollback           *CatalogLegacyRollbackCompatibility `json:"legacy_rollback_compatibility,omitempty"`
	Target                   CatalogReleaseIdentity              `json:"target_release_identity"`
	ConfigurationSHA256      string                              `json:"configuration_sha256"`
	SchemaVersion            int                                 `json:"schema_version"`
	WorkInventorySHA256      string                              `json:"work_inventory_sha256"`
	WorkInventoryEmpty       bool                                `json:"work_inventory_empty"`
	PreflightAt              string                              `json:"preflight_at"`
	BackupDirectory          string                              `json:"backup_directory"`
	BackupManifestSHA256     string                              `json:"backup_manifest_sha256"`
	AvailableBytes           uint64                              `json:"available_bytes"`
	RequiredBytes            uint64                              `json:"required_bytes"`
}

// CatalogLegacyRollbackCompatibility binds rollback claims for an unselected
// legacy PRD installation to its exact binaries and configuration, plus a
// reviewed semantic-test record. A schema number alone is never sufficient.
type CatalogLegacyRollbackCompatibility struct {
	FormatVersion       int      `json:"format_version"`
	LegacyBundleSHA256  string   `json:"legacy_bundle_sha256"`
	ConfigurationSHA256 string   `json:"configuration_sha256"`
	DatabaseSchema      int      `json:"database_schema"`
	ReadSchemaMin       int      `json:"read_schema_min"`
	ReadSchemaMax       int      `json:"read_schema_max"`
	TestSuite           string   `json:"test_suite"`
	TestEvidenceSHA256  string   `json:"test_evidence_sha256"`
	SemanticChecks      []string `json:"semantic_checks"`
	TestedAt            string   `json:"tested_at"`
}

const catalogLegacyRollbackTestSuite = "catalog-prd-legacy-semantics-v1"

var catalogLegacyRollbackSemanticChecks = []string{
	"current-database-read-without-migration",
	"retained-history-unchanged",
	"launcher-resolves-preserved-legacy-binaries",
	"queued-wrapper-resolves-preserved-legacy-binaries",
	"rollback-preserves-current-database",
}

func ValidateCatalogLegacyRollbackCompatibility(evidence CatalogLegacyRollbackCompatibility,
	legacyBundleSHA256, configurationSHA256 string, databaseSchema int) error {
	if evidence.FormatVersion != 1 || !bundleDigestPattern.MatchString(legacyBundleSHA256) ||
		evidence.LegacyBundleSHA256 != legacyBundleSHA256 || !bundleDigestPattern.MatchString(configurationSHA256) ||
		evidence.ConfigurationSHA256 != configurationSHA256 || evidence.DatabaseSchema != databaseSchema || databaseSchema <= 0 ||
		evidence.ReadSchemaMin <= 0 || evidence.ReadSchemaMin > databaseSchema || evidence.ReadSchemaMax < databaseSchema ||
		evidence.TestSuite != catalogLegacyRollbackTestSuite || !bundleDigestPattern.MatchString(evidence.TestEvidenceSHA256) ||
		!reflect.DeepEqual(evidence.SemanticChecks, catalogLegacyRollbackSemanticChecks) || !validCohortTimestamp(evidence.TestedAt) {
		return ErrCatalogRollbackUnsafe
	}
	return nil
}

type CatalogCohortHostFence struct {
	HostID               string `json:"host_id"`
	ServiceWasActive     bool   `json:"service_was_active"`
	ServiceStopped       bool   `json:"service_stopped"`
	WorkInventorySHA256  string `json:"work_inventory_sha256"`
	ConfigurationSHA256  string `json:"configuration_sha256"`
	SelectorTarget       string `json:"selector_target,omitempty"`
	PreviousBundleSHA256 string `json:"previous_bundle_sha256"`
	DatabaseSchema       int    `json:"database_schema"`
	ObservedAt           string `json:"observed_at"`
}

// CatalogCohortPublisherPause records the owner-provided pause that cannot be
// inferred from the four host-local service observations. Host ingress fence
// evidence is derived separately from each supported host preflight report.
type CatalogCohortPublisherPause struct {
	Owner         string
	Control       string
	Paused        bool
	NoNewAccepted bool
}

type CatalogCohortTransition struct {
	Sequence            uint64                             `json:"sequence"`
	Action              string                             `json:"action"`
	HostID              string                             `json:"host_id,omitempty"`
	EvidenceSHA256      string                             `json:"evidence_sha256"`
	At                  string                             `json:"at"`
	ResumeStage         CatalogCohortStage                 `json:"resume_stage,omitempty"`
	ReconciledStage     CatalogCohortStage                 `json:"reconciled_stage,omitempty"`
	HistoricalBranches  []CatalogCohortHistoryBranch       `json:"historical_branches,omitempty"`
	Supersession        *CatalogCohortSupersessionEvidence `json:"supersession,omitempty"`
	SupersessionRestart *CatalogCohortSupersessionRestart  `json:"supersession_restart,omitempty"`
	HostFence           *CatalogCohortHostFence            `json:"host_fence,omitempty"`
	SuccessorID         string                             `json:"successor_operation_id,omitempty"`
	PreviousSHA256      string                             `json:"previous_sha256"`
	SHA256              string                             `json:"sha256"`
}

// CatalogCohortHistoryBranch retains one previously valid, divergent copy of
// a receipt. The reconciliation transition embeds each branch so the
// supported sync path can replace local copies without discarding events.
type CatalogCohortHistoryBranch struct {
	Stage       CatalogCohortStage        `json:"stage"`
	Transitions []CatalogCohortTransition `json:"transitions"`
}

// CatalogCohortSupersessionEvidence is the local, read-only observation that
// permits a never-switched attempt, a fully rolled-back attempt, or a
// completed release to be superseded by a fresh held successor. Every host
// records its own evidence before the old receipt stops holding admissions.
// A completed predecessor must still match its completed selector journal;
// an interrupted selector operation must first use the existing recovery
// procedure.
type CatalogCohortSupersessionEvidence struct {
	HostID               string `json:"host_id"`
	SuccessorOperationID string `json:"successor_operation_id"`
	ExpectedSelector     string `json:"expected_selector"`
	ObservedSelector     string `json:"observed_selector"`
	SelectorBundleSHA256 string `json:"selector_bundle_sha256,omitempty"` // Exact selected bundle, or recorded legacy PRD binary-set identity.
	ServiceActive        bool   `json:"service_active"`
	WorkInventorySHA256  string `json:"work_inventory_sha256"`
	WorkInventoryEmpty   bool   `json:"work_inventory_empty"`
	ConfigurationSHA256  string `json:"configuration_sha256"`
	DatabaseSchema       int    `json:"database_schema"`
	SelectorOperations   string `json:"selector_operations"`
}

// CatalogCohortSupersessionRestart records that a partially observed,
// read-only supersession epoch has expired and will be repeated against a
// fresh held successor. The preceding host observations remain in the
// transition history; this record only starts a new observation epoch.
type CatalogCohortSupersessionRestart struct {
	PreviousSuccessorID       string   `json:"previous_successor_operation_id"`
	NextSuccessorID           string   `json:"next_successor_operation_id"`
	PreviousEvidenceExpiresAt string   `json:"previous_evidence_expires_at"`
	PriorObservedHosts        []string `json:"prior_observed_hosts"`
}

// CatalogCohortReceipt is a durable operator record shared across the four
// selected-host copies. It binds preflight and backup evidence to one sealed
// release and records the required worker-then-Mac progression. It is not a
// distributed lock or a substitute for the host service/admission controls.
type CatalogCohortReceipt struct {
	FormatVersion  int                          `json:"format_version"`
	OperationID    string                       `json:"operation_id"`
	Lane           config.ReleaseLane           `json:"lane"`
	ReleaseID      string                       `json:"release_id"`
	ReleaseSHA256  string                       `json:"release_sha256"`
	SourceRevision string                       `json:"source_revision"`
	Database       CatalogDatabaseCompatibility `json:"database_compatibility"`
	CreatedAt      string                       `json:"created_at"`
	UpdatedAt      string                       `json:"updated_at"`
	Stage          CatalogCohortStage           `json:"stage"`
	FenceProofs    []CatalogCohortFenceProof    `json:"fence_proofs"`
	Hosts          []CatalogCohortHostPreflight `json:"hosts"`
	Transitions    []CatalogCohortTransition    `json:"transitions"`
}

// CatalogCohortRequiredFences lists the minimum scoped closures before a
// whole-lane switch. Project publishers are named by their real owner in the
// receipt; each worker direct route and Mac dispatch route has independent
// evidence.
func CatalogCohortRequiredFences() []string {
	return []string{
		"mac-project-publishers", "mac-ingress", "mac-queued-dispatch",
		"linux-host-direct-ingress", "sandbox-host-direct-ingress", "factory-sandbox-direct-ingress",
	}
}

func catalogCohortInitialFenceScopes(lane config.ReleaseLane) []string {
	if lane == config.ReleaseLanePRD {
		// PRD's legacy service cannot observe a catalog receipt before adoption.
		// Its four service stops are recorded as ordered host-fence transitions.
		return []string{"mac-project-publishers"}
	}
	return CatalogCohortRequiredFences()
}

// NewCatalogCohortReceipt builds a prepared receipt only from a validated
// sealed release, four host-local preflight reports, and an explicit owner
// attestation that external project publishers remain paused. It derives all
// fence digests and host identities; callers never assemble receipt JSON.
func NewCatalogCohortReceipt(lane config.ReleaseLane, manifest CatalogReleaseManifest,
	reports []CatalogCohortHostPreflightReport, publisherPause CatalogCohortPublisherPause, now time.Time) (CatalogCohortReceipt, error) {
	if (lane != config.ReleaseLaneDev && lane != config.ReleaseLaneQAS && lane != config.ReleaseLanePRD) || ValidateCatalogReleaseManifest(manifest) != nil ||
		len(reports) != len(catalogCohortSwitchOrder) || strings.TrimSpace(publisherPause.Owner) == "" ||
		strings.TrimSpace(publisherPause.Control) == "" || len(publisherPause.Owner) > 128 || len(publisherPause.Control) > 1024 ||
		!publisherPause.Paused || !publisherPause.NoNewAccepted || now.IsZero() {
		return CatalogCohortReceipt{}, ErrCatalogOperationInvalid
	}
	manifestBytes, err := CatalogReleaseManifestBytes(manifest)
	if err != nil {
		return CatalogCohortReceipt{}, ErrInvalidCatalogRelease
	}
	manifestDigest := sha256.Sum256(manifestBytes)
	releaseSHA256 := hex.EncodeToString(manifestDigest[:])
	byHost := make(map[string]CatalogCohortHostPreflightReport, len(reports))
	for _, report := range reports {
		preflight := report.Preflight
		if _, exists := byHost[preflight.HostID]; exists || !containsString(catalogCohortSwitchOrder, preflight.HostID) ||
			!preflight.WorkInventoryEmpty || !report.WorkInventory.Empty() || preflight.RequiredBytes == 0 ||
			preflight.AvailableBytes < preflight.RequiredBytes || preflight.Target.ReleaseID != manifest.ReleaseID ||
			preflight.Target.ReleaseSHA256 != releaseSHA256 || preflight.Target.BundleSHA256 != catalogManifestBundleSHA256(manifest, preflight.HostID) ||
			preflight.Target.HostID != preflight.HostID || preflight.SchemaVersion < manifest.Database.ReadSchemaMin ||
			preflight.SchemaVersion > manifest.Database.ReadSchemaMax {
			return CatalogCohortReceipt{}, ErrCatalogOperationInvalid
		}
		if preflight.WorkInventorySHA256 != report.WorkInventory.SHA256() {
			return CatalogCohortReceipt{}, ErrCatalogOperationInvalid
		}
		if lane == config.ReleaseLanePRD {
			if preflight.PreviousTarget == "" || strings.HasPrefix(preflight.PreviousTarget, "legacy-bin:") {
				if preflight.LegacyRollback == nil || ValidateCatalogLegacyRollbackCompatibility(*preflight.LegacyRollback,
					preflight.PreviousBundleSHA256, preflight.ConfigurationSHA256, preflight.SchemaVersion) != nil ||
					preflight.PreviousReadSchemaMax != preflight.LegacyRollback.ReadSchemaMax {
					return CatalogCohortReceipt{}, ErrCatalogRollbackUnsafe
				}
			} else if preflight.LegacyRollback != nil {
				return CatalogCohortReceipt{}, ErrCatalogOperationInvalid
			}
		} else if preflight.ServiceActiveAtPreflight || preflight.LegacyRollback != nil {
			return CatalogCohortReceipt{}, ErrCatalogOperationInvalid
		}
		byHost[preflight.HostID] = report
	}
	if len(byHost) != len(catalogCohortSwitchOrder) {
		return CatalogCohortReceipt{}, ErrCatalogOperationInvalid
	}
	operationID, err := newCatalogOperationID()
	if err != nil {
		return CatalogCohortReceipt{}, err
	}
	stamp := now.UTC().Format(time.RFC3339Nano)
	receipt := CatalogCohortReceipt{
		FormatVersion: catalogCohortFormatVersion, OperationID: operationID, Lane: lane,
		ReleaseID: manifest.ReleaseID, ReleaseSHA256: releaseSHA256, SourceRevision: manifest.SourceRevision,
		Database: manifest.Database, CreatedAt: stamp, UpdatedAt: stamp, Stage: CatalogCohortPrepared,
	}
	for _, hostID := range catalogCohortSwitchOrder {
		report := byHost[hostID]
		preflight := report.Preflight
		if err := validateCohortPreflightFreshness(preflight.PreflightAt, now); err != nil {
			return CatalogCohortReceipt{}, err
		}
		receipt.Hosts = append(receipt.Hosts, preflight)
		encoded, marshalErr := json.Marshal(report)
		if marshalErr != nil {
			return CatalogCohortReceipt{}, ErrCatalogOperationInvalid
		}
		evidence := sha256.Sum256(encoded)
		if lane != config.ReleaseLanePRD {
			for _, scope := range cohortIngressFencesForHost(hostID) {
				receipt.FenceProofs = append(receipt.FenceProofs, CatalogCohortFenceProof{
					Scope: scope, Control: "Fresh supported catalog-host-preflight observed the selected service stopped and complete work inventory empty on " + hostID + ".",
					Owner: "Runner maintenance operator", Paused: true, NoNewAccepted: true,
					EvidenceSHA256: hex.EncodeToString(evidence[:]), VerifiedAt: preflight.PreflightAt,
				})
			}
		}
	}
	publisherEvidence, err := json.Marshal(struct {
		Scope         string `json:"scope"`
		Owner         string `json:"owner"`
		Control       string `json:"control"`
		Paused        bool   `json:"paused"`
		NoNewAccepted bool   `json:"no_new_accepted"`
		VerifiedAt    string `json:"verified_at"`
	}{"mac-project-publishers", strings.TrimSpace(publisherPause.Owner), strings.TrimSpace(publisherPause.Control), true, true, stamp})
	if err != nil {
		return CatalogCohortReceipt{}, ErrCatalogOperationInvalid
	}
	publisherDigest := sha256.Sum256(publisherEvidence)
	receipt.FenceProofs = append(receipt.FenceProofs, CatalogCohortFenceProof{
		Scope: "mac-project-publishers", Control: strings.TrimSpace(publisherPause.Control), Owner: strings.TrimSpace(publisherPause.Owner),
		Paused: true, NoNewAccepted: true, EvidenceSHA256: hex.EncodeToString(publisherDigest[:]), VerifiedAt: stamp,
	})
	if ValidateCatalogCohortReceipt(receipt) != nil {
		return CatalogCohortReceipt{}, ErrCatalogOperationInvalid
	}
	return receipt, nil
}

func catalogManifestBundleSHA256(manifest CatalogReleaseManifest, hostID string) string {
	for _, host := range manifest.Hosts {
		if host.HostID == hostID {
			return host.BundleSHA256
		}
	}
	return ""
}

func cohortIngressFencesForHost(hostID string) []string {
	switch hostID {
	case "linux-host", "sandbox-host", "factory-sandbox":
		return []string{hostID + "-direct-ingress"}
	case "mac-control-plane":
		return []string{"mac-ingress", "mac-queued-dispatch"}
	default:
		return nil
	}
}

func validateCohortPreflightFreshness(atValue string, now time.Time) error {
	at, err := time.Parse(time.RFC3339Nano, atValue)
	if err != nil || now.Before(at.Add(-5*time.Second)) || now.Sub(at) > catalogCohortEvidenceMaxAge {
		return ErrCatalogOperationInvalid
	}
	return nil
}

// ValidateCatalogCohortReceipt verifies the exact four-host scope, sealed
// release binding, fresh quiescence/backup inputs, and hash-linked transition
// history. It validates claims and digests; it does not observe a live host.
func ValidateCatalogCohortReceipt(receipt CatalogCohortReceipt) error {
	if receipt.FormatVersion != catalogCohortFormatVersion || !validCatalogOperationID(receipt.OperationID) ||
		(receipt.Lane != config.ReleaseLaneDev && receipt.Lane != config.ReleaseLaneQAS && receipt.Lane != config.ReleaseLanePRD) ||
		!validCatalogReleaseID(receipt.ReleaseID) || !bundleDigestPattern.MatchString(receipt.ReleaseSHA256) ||
		!sourceRevisionPattern.MatchString(receipt.SourceRevision) || !validCohortTimestamp(receipt.CreatedAt) || !validCohortTimestamp(receipt.UpdatedAt) ||
		receipt.Database.ReadSchemaMin <= 0 || receipt.Database.ReadSchemaMin > receipt.Database.ReadSchemaMax ||
		receipt.Database.WriteSchemaMin <= 0 || receipt.Database.WriteSchemaMin > receipt.Database.WriteSchemaMax ||
		receipt.Database.ReadSchemaMax != store.CurrentSchemaVersion || receipt.Database.WriteSchemaMin != store.CurrentSchemaVersion ||
		receipt.Database.WriteSchemaMax != store.CurrentSchemaVersion || receipt.Database.Migration != catalogMigrationPolicy || receipt.Database.Rollback != catalogRollbackPolicy ||
		len(receipt.Hosts) != len(catalogCohortSwitchOrder) || len(receipt.FenceProofs) != len(catalogCohortInitialFenceScopes(receipt.Lane)) {
		return ErrCatalogOperationInvalid
	}
	if len(receipt.Transitions) == 0 {
		if receipt.Stage != CatalogCohortPrepared || receipt.UpdatedAt != receipt.CreatedAt {
			return ErrCatalogOperationInvalid
		}
	} else if receipt.UpdatedAt != receipt.Transitions[len(receipt.Transitions)-1].At {
		return ErrCatalogOperationInvalid
	}
	for index, hostID := range catalogCohortSwitchOrder {
		host := receipt.Hosts[index]
		if host.HostID != hostID || host.Target.HostID != hostID || host.Target.ReleaseID != receipt.ReleaseID ||
			host.Target.ReleaseSHA256 != receipt.ReleaseSHA256 || !bundleDigestPattern.MatchString(host.Target.BundleSHA256) ||
			!bundleDigestPattern.MatchString(host.ConfigurationSHA256) || !bundleDigestPattern.MatchString(host.WorkInventorySHA256) ||
			!host.WorkInventoryEmpty || !validCohortTimestamp(host.PreflightAt) || !filepath.IsAbs(host.BackupDirectory) ||
			filepath.Clean(host.BackupDirectory) != host.BackupDirectory || !bundleDigestPattern.MatchString(host.BackupManifestSHA256) ||
			host.SchemaVersion < receipt.Database.ReadSchemaMin || host.SchemaVersion > receipt.Database.ReadSchemaMax ||
			host.AvailableBytes < host.RequiredBytes || !validPreviousCatalogIdentity(host, receipt.Lane) {
			return ErrCatalogOperationInvalid
		}
		if host.Target.BundleSHA256 == "" {
			return ErrCatalogOperationInvalid
		}
	}
	required := catalogCohortInitialFenceScopes(receipt.Lane)
	seenFences := make(map[string]struct{}, len(receipt.FenceProofs))
	for _, proof := range receipt.FenceProofs {
		if !containsString(required, proof.Scope) || proof.Control == "" || proof.Owner == "" || !proof.Paused || !proof.NoNewAccepted ||
			!bundleDigestPattern.MatchString(proof.EvidenceSHA256) || !validCohortTimestamp(proof.VerifiedAt) {
			return ErrCatalogOperationInvalid
		}
		if _, exists := seenFences[proof.Scope]; exists {
			return ErrCatalogOperationInvalid
		}
		seenFences[proof.Scope] = struct{}{}
	}
	for _, scope := range required {
		if _, exists := seenFences[scope]; !exists {
			return ErrCatalogOperationInvalid
		}
	}
	stage, err := replayCatalogCohortTransitions(receipt)
	if err != nil || stage != receipt.Stage {
		return ErrCatalogOperationInvalid
	}
	return nil
}

// AdvanceCatalogCohortReceipt appends one hash-linked operator observation.
// Switch records are accepted only in the fixed worker-then-Mac order, and
// final verification cannot begin until the whole cohort has switched.
func AdvanceCatalogCohortReceipt(receipt CatalogCohortReceipt, action, hostID, evidenceSHA256 string, at time.Time) (CatalogCohortReceipt, error) {
	return advanceCatalogCohortReceipt(receipt, CatalogCohortTransition{
		Action: action, HostID: hostID, EvidenceSHA256: evidenceSHA256,
	}, at)
}

func AdvanceCatalogCohortHostFence(receipt CatalogCohortReceipt, evidence CatalogCohortHostFence, at time.Time) (CatalogCohortReceipt, error) {
	if ValidateCatalogCohortReceipt(receipt) != nil || receipt.Lane != config.ReleaseLanePRD || at.IsZero() ||
		!containsString(catalogCohortSwitchOrder, evidence.HostID) {
		return CatalogCohortReceipt{}, ErrCatalogOperationInvalid
	}
	encoded, err := json.Marshal(evidence)
	if err != nil {
		return CatalogCohortReceipt{}, ErrCatalogOperationInvalid
	}
	digest := sha256.Sum256(encoded)
	return advanceCatalogCohortReceipt(receipt, CatalogCohortTransition{
		Action: "fence-host", HostID: evidence.HostID, EvidenceSHA256: hex.EncodeToString(digest[:]), HostFence: &evidence,
	}, at)
}

// AdvanceCatalogCohortSupersedeHost records one host's fresh local
// observations. Supersession is limited to attempts that never switched,
// were fully rolled back, or completed on every host; any interrupted switch
// remains under recovery.
func AdvanceCatalogCohortSupersedeHost(receipt, successor CatalogCohortReceipt, observation CatalogCohortSupersessionEvidence, at time.Time) (CatalogCohortReceipt, error) {
	if ValidateCatalogCohortReceipt(receipt) != nil || ValidateCatalogCohortReceipt(successor) != nil ||
		receipt.OperationID == successor.OperationID || receipt.Lane != successor.Lane ||
		successor.Stage != CatalogCohortHeld || !cohortEvidenceFresh(successor, at.UTC().Format(time.RFC3339Nano)) ||
		!catalogCohortReceiptSupersessionEligible(receipt) || !observationMatchesSuccessor(receipt, successor, observation) {
		return CatalogCohortReceipt{}, ErrCatalogOperationInvalid
	}
	encoded, err := json.Marshal(observation)
	if err != nil {
		return CatalogCohortReceipt{}, err
	}
	digest := sha256.Sum256(encoded)
	return advanceCatalogCohortReceipt(receipt, CatalogCohortTransition{
		Action: "supersede-host", HostID: observation.HostID,
		EvidenceSHA256: hex.EncodeToString(digest[:]), Supersession: &observation,
	}, at)
}

// AdvanceCatalogCohortSupersessionRestart preserves a partial read-only
// supersession history while allowing it to be retried against fresh evidence.
// The previous successor must still be held, but its preflight must have
// expired; the new successor must be held and fresh. No host state is changed.
func AdvanceCatalogCohortSupersessionRestart(receipt, previousSuccessor, nextSuccessor CatalogCohortReceipt, at time.Time) (CatalogCohortReceipt, error) {
	if ValidateCatalogCohortReceipt(receipt) != nil || ValidateCatalogCohortReceipt(previousSuccessor) != nil ||
		ValidateCatalogCohortReceipt(nextSuccessor) != nil || !catalogCohortReceiptSupersessionEligible(receipt) || at.IsZero() ||
		receipt.Lane != previousSuccessor.Lane || receipt.Lane != nextSuccessor.Lane || previousSuccessor.Stage != CatalogCohortHeld ||
		nextSuccessor.Stage != CatalogCohortHeld || previousSuccessor.OperationID == nextSuccessor.OperationID ||
		currentSupersessionSuccessor(receipt.Transitions) != previousSuccessor.OperationID ||
		cohortEvidenceFresh(previousSuccessor, at.UTC().Format(time.RFC3339Nano)) ||
		!cohortEvidenceFresh(nextSuccessor, at.UTC().Format(time.RFC3339Nano)) {
		return CatalogCohortReceipt{}, ErrCatalogOperationInvalid
	}
	priorHosts := currentSupersededHosts(receipt.Transitions)
	if len(priorHosts) == 0 || len(priorHosts) >= len(catalogCohortSwitchOrder) {
		return CatalogCohortReceipt{}, ErrCatalogOperationInvalid
	}
	expiresAt, ok := catalogCohortEvidenceExpiresAt(previousSuccessor)
	if !ok || at.UTC().Before(expiresAt) {
		return CatalogCohortReceipt{}, ErrCatalogOperationInvalid
	}
	restart := CatalogCohortSupersessionRestart{
		PreviousSuccessorID:       previousSuccessor.OperationID,
		NextSuccessorID:           nextSuccessor.OperationID,
		PreviousEvidenceExpiresAt: expiresAt.UTC().Format(time.RFC3339Nano),
		PriorObservedHosts:        append([]string(nil), priorHosts...),
	}
	encoded, err := json.Marshal(restart)
	if err != nil {
		return CatalogCohortReceipt{}, err
	}
	digest := sha256.Sum256(encoded)
	return advanceCatalogCohortReceipt(receipt, CatalogCohortTransition{
		Action: "supersession-restart", EvidenceSHA256: hex.EncodeToString(digest[:]),
		SuccessorID: nextSuccessor.OperationID, SupersessionRestart: &restart,
	}, at)
}

// AdvanceCatalogCohortSupersede finalizes only after every host has recorded
// its own observation against the same held successor. Until this event the
// old receipt continues to hold admissions, including across process restart.
func AdvanceCatalogCohortSupersede(receipt CatalogCohortReceipt, successorOperationID string, at time.Time) (CatalogCohortReceipt, error) {
	if ValidateCatalogCohortReceipt(receipt) != nil || !validCatalogOperationID(successorOperationID) || !catalogCohortReceiptSupersessionEligible(receipt) ||
		!cohortHasAllSupersessionEvidence(receipt, successorOperationID) {
		return CatalogCohortReceipt{}, ErrCatalogOperationInvalid
	}
	digest := sha256.Sum256([]byte("catalog-cohort-superseded\x00" + receipt.OperationID + "\x00" + successorOperationID))
	return advanceCatalogCohortReceipt(receipt, CatalogCohortTransition{
		Action: "supersede", EvidenceSHA256: hex.EncodeToString(digest[:]), SuccessorID: successorOperationID,
	}, at)
}

func advanceCatalogCohortReceipt(receipt CatalogCohortReceipt, transition CatalogCohortTransition, at time.Time) (CatalogCohortReceipt, error) {
	if ValidateCatalogCohortReceipt(receipt) != nil || !bundleDigestPattern.MatchString(transition.EvidenceSHA256) || at.IsZero() {
		return CatalogCohortReceipt{}, ErrCatalogOperationInvalid
	}
	previousStage, err := replayCatalogCohortTransitions(receipt)
	if err != nil {
		return CatalogCohortReceipt{}, ErrCatalogOperationInvalid
	}
	transition.Sequence = uint64(len(receipt.Transitions) + 1)
	transition.At = at.UTC().Format(time.RFC3339Nano)
	transition.PreviousSHA256 = lastCohortTransitionDigest(receipt.Transitions)
	if transition.Action == "resume" && previousStage == CatalogCohortBlocked {
		transition.ResumeStage = blockedCohortResumeStage(receipt.Transitions)
	}
	transition.SHA256 = catalogCohortTransitionDigest(transition)
	receipt.Transitions = append(append([]CatalogCohortTransition(nil), receipt.Transitions...), transition)
	receipt.Stage, err = replayCatalogCohortTransitions(receipt)
	if err != nil {
		return CatalogCohortReceipt{}, ErrCatalogOperationInvalid
	}
	receipt.UpdatedAt = transition.At
	if ValidateCatalogCohortReceipt(receipt) != nil {
		return CatalogCohortReceipt{}, ErrCatalogOperationInvalid
	}
	return receipt, nil
}

func replayCatalogCohortTransitions(receipt CatalogCohortReceipt) (CatalogCohortStage, error) {
	stage := CatalogCohortPrepared
	lastDigest := ""
	var switched, verified, rolledBack []string
	lastPreflight := make(map[string]time.Time, len(catalogCohortSwitchOrder))
	var previousAt time.Time
	var blockedFrom CatalogCohortStage
	for index, event := range receipt.Transitions {
		eventAt, timeErr := time.Parse(time.RFC3339Nano, event.At)
		if event.Sequence != uint64(index+1) || event.PreviousSHA256 != lastDigest || !bundleDigestPattern.MatchString(event.EvidenceSHA256) || timeErr != nil ||
			!previousAt.IsZero() && eventAt.Before(previousAt) ||
			!validCohortTimestamp(event.At) || event.SHA256 != catalogCohortTransitionDigest(event) {
			return "", ErrCatalogOperationInvalid
		}
		switch event.Action {
		case "hold":
			if stage != CatalogCohortPrepared || !cohortEvidenceFresh(receipt, event.At) || !eventHostEmpty(event.HostID) ||
				receipt.Lane == config.ReleaseLanePRD && !cohortHasAllHostFences(receipt, receipt.Transitions[:index]) {
				return "", ErrCatalogOperationInvalid
			}
			stage = CatalogCohortHeld
		case "fence-host":
			if receipt.Lane != config.ReleaseLanePRD || stage != CatalogCohortPrepared || event.HostFence == nil ||
				len(currentCohortHostFences(receipt.Transitions[:index])) >= len(catalogCohortSwitchOrder) ||
				event.HostID != catalogCohortSwitchOrder[len(currentCohortHostFences(receipt.Transitions[:index]))] ||
				!validCatalogCohortHostFence(receipt, event, eventAt) {
				return "", ErrCatalogOperationInvalid
			}
		case "preflight":
			if stage != CatalogCohortHeld && stage != CatalogCohortSwitching || len(switched) >= len(catalogCohortSwitchOrder) || event.HostID != catalogCohortSwitchOrder[len(switched)] {
				return "", ErrCatalogOperationInvalid
			}
			lastPreflight[event.HostID] = eventAt
		case "switch":
			if stage != CatalogCohortHeld && stage != CatalogCohortSwitching || len(switched) >= len(catalogCohortSwitchOrder) ||
				event.HostID != catalogCohortSwitchOrder[len(switched)] {
				return "", ErrCatalogOperationInvalid
			}
			preflightAt, ok := lastPreflight[event.HostID]
			if !ok || eventAt.Before(preflightAt) || eventAt.Sub(preflightAt) > 2*time.Minute {
				return "", ErrCatalogOperationInvalid
			}
			delete(lastPreflight, event.HostID)
			switched = append(switched, event.HostID)
			stage = CatalogCohortSwitching
		case "switch-reconciled":
			if stage != CatalogCohortHeld && stage != CatalogCohortSwitching || len(switched) >= len(catalogCohortSwitchOrder) ||
				event.HostID != catalogCohortSwitchOrder[len(switched)] {
				return "", ErrCatalogOperationInvalid
			}
			preflightAt, ok := lastPreflight[event.HostID]
			if !ok || eventAt.Before(preflightAt) {
				return "", ErrCatalogOperationInvalid
			}
			delete(lastPreflight, event.HostID)
			switched = append(switched, event.HostID)
			stage = CatalogCohortSwitching
		case "verify-start":
			if stage != CatalogCohortSwitching || len(switched) != len(catalogCohortSwitchOrder) || !eventHostEmpty(event.HostID) {
				return "", ErrCatalogOperationInvalid
			}
			stage = CatalogCohortVerifying
		case "verify":
			if stage != CatalogCohortVerifying || !containsString(catalogCohortSwitchOrder, event.HostID) || containsString(verified, event.HostID) {
				return "", ErrCatalogOperationInvalid
			}
			verified = append(verified, event.HostID)
		case "complete":
			if stage != CatalogCohortVerifying || len(verified) != len(catalogCohortSwitchOrder) || !eventHostEmpty(event.HostID) {
				return "", ErrCatalogOperationInvalid
			}
			stage = CatalogCohortComplete
		case "block":
			if stage == CatalogCohortComplete || stage == CatalogCohortRolledBack || stage == CatalogCohortReleased || stage == CatalogCohortBlocked || !eventHostEmpty(event.HostID) {
				return "", ErrCatalogOperationInvalid
			}
			blockedFrom = stage
			stage = CatalogCohortBlocked
		case "resume":
			if stage != CatalogCohortBlocked || event.ResumeStage != blockedFrom || !eventHostEmpty(event.HostID) {
				return "", ErrCatalogOperationInvalid
			}
			stage = blockedFrom
		case "rollback-start":
			if stage != CatalogCohortBlocked || len(switched) == 0 || !eventHostEmpty(event.HostID) {
				return "", ErrCatalogOperationInvalid
			}
			stage = CatalogCohortRollingBack
		case "rollback-host":
			if stage != CatalogCohortRollingBack || len(rolledBack) >= len(switched) || event.HostID != switched[len(switched)-1-len(rolledBack)] {
				return "", ErrCatalogOperationInvalid
			}
			rolledBack = append(rolledBack, event.HostID)
		case "rollback-complete":
			if stage != CatalogCohortRollingBack || len(rolledBack) != len(switched) || !eventHostEmpty(event.HostID) {
				return "", ErrCatalogOperationInvalid
			}
			stage = CatalogCohortRolledBack
		case "open":
			if (stage != CatalogCohortComplete && stage != CatalogCohortRolledBack) || !eventHostEmpty(event.HostID) {
				return "", ErrCatalogOperationInvalid
			}
			stage = CatalogCohortReleased
		case "reconcile-history":
			if (stage != CatalogCohortPrepared && stage != CatalogCohortHeld && stage != CatalogCohortSwitching &&
				stage != CatalogCohortVerifying && stage != CatalogCohortBlocked && stage != CatalogCohortRollingBack &&
				stage != CatalogCohortRolledBack) || !eventHostEmpty(event.HostID) ||
				!validCatalogCohortHistoryReconciliation(receipt, event, receipt.Transitions[:index]) {
				return "", ErrCatalogOperationInvalid
			}
			blockedFrom = event.ReconciledStage
			if event.ReconciledStage == CatalogCohortSwitching || event.ReconciledStage == CatalogCohortVerifying ||
				event.ReconciledStage == CatalogCohortRollingBack || event.ReconciledStage == CatalogCohortRolledBack {
				var err error
				var branchVerified, branchRolledBack []string
				_, switched, branchVerified, branchRolledBack, err = catalogCohortReconciledBranchState(event.HistoricalBranches)
				if err != nil || len(switched) == 0 {
					return "", ErrCatalogOperationInvalid
				}
				if event.ReconciledStage == CatalogCohortVerifying {
					if len(switched) != len(catalogCohortSwitchOrder) {
						return "", ErrCatalogOperationInvalid
					}
					verified = branchVerified
				}
				rolledBack = branchRolledBack
				lastPreflight = make(map[string]time.Time, len(catalogCohortSwitchOrder))
			}
			stage = CatalogCohortBlocked
		case "supersede-host":
			if !catalogCohortSupersessionStageEligible(stage, blockedFrom, receipt.Transitions[:index]) ||
				len(currentSupersededHosts(receipt.Transitions[:index])) >= len(catalogCohortSwitchOrder) ||
				event.HostID != catalogCohortSwitchOrder[len(currentSupersededHosts(receipt.Transitions[:index]))] ||
				!validSupersessionEvidenceForReceipt(event, receipt, stage, blockedFrom) {
				return "", ErrCatalogOperationInvalid
			}
			if previous := currentSupersessionSuccessor(receipt.Transitions[:index]); previous != "" && previous != event.Supersession.SuccessorOperationID {
				return "", ErrCatalogOperationInvalid
			}
		case "supersession-restart":
			if !catalogCohortSupersessionStageEligible(stage, blockedFrom, receipt.Transitions[:index]) || !eventHostEmpty(event.HostID) ||
				!validSupersessionRestart(event) {
				return "", ErrCatalogOperationInvalid
			}
			priorHosts := currentSupersededHosts(receipt.Transitions[:index])
			previous := currentSupersessionSuccessor(receipt.Transitions[:index])
			if previous == "" || event.SupersessionRestart.PreviousSuccessorID != previous ||
				event.SupersessionRestart.NextSuccessorID != event.SuccessorID || event.SuccessorID == previous ||
				len(priorHosts) == 0 || len(priorHosts) >= len(catalogCohortSwitchOrder) ||
				!reflect.DeepEqual(priorHosts, event.SupersessionRestart.PriorObservedHosts) {
				return "", ErrCatalogOperationInvalid
			}
		case "supersede":
			if !catalogCohortSupersessionStageEligible(stage, blockedFrom, receipt.Transitions[:index]) || !eventHostEmpty(event.HostID) ||
				!validCatalogOperationID(event.SuccessorID) || !cohortTransitionsHaveSupersession(receipt.Transitions[:index], event.SuccessorID) {
				return "", ErrCatalogOperationInvalid
			}
			digest := sha256.Sum256([]byte("catalog-cohort-superseded\x00" + receipt.OperationID + "\x00" + event.SuccessorID))
			if event.EvidenceSHA256 != hex.EncodeToString(digest[:]) || event.Supersession != nil || event.SupersessionRestart != nil ||
				event.ResumeStage != "" || event.ReconciledStage != "" || len(event.HistoricalBranches) != 0 {
				return "", ErrCatalogOperationInvalid
			}
			stage = CatalogCohortSuperseded
		default:
			return "", ErrCatalogOperationInvalid
		}
		if event.Action != "fence-host" && event.HostFence != nil {
			return "", ErrCatalogOperationInvalid
		}
		lastDigest = event.SHA256
		previousAt = eventAt
	}
	return stage, nil
}

func currentCohortHostFences(transitions []CatalogCohortTransition) []string {
	fenced := make(map[string]struct{})
	walkCatalogCohortTransitions(transitions, func(event CatalogCohortTransition) {
		if event.Action == "fence-host" {
			fenced[event.HostID] = struct{}{}
		}
	})
	var hosts []string
	for _, hostID := range catalogCohortSwitchOrder {
		if _, ok := fenced[hostID]; ok {
			hosts = append(hosts, hostID)
		}
	}
	return hosts
}

func cohortHasAllHostFences(receipt CatalogCohortReceipt, transitions []CatalogCohortTransition) bool {
	if receipt.Lane != config.ReleaseLanePRD {
		return true
	}
	return reflect.DeepEqual(currentCohortHostFences(transitions), catalogCohortSwitchOrder)
}

func validCatalogCohortHostFence(receipt CatalogCohortReceipt, event CatalogCohortTransition, eventAt time.Time) bool {
	if event.HostFence == nil || !validCohortTimestamp(eventAt.UTC().Format(time.RFC3339Nano)) ||
		event.HostFence.HostID != event.HostID || !event.HostFence.ServiceStopped ||
		!bundleDigestPattern.MatchString(event.HostFence.WorkInventorySHA256) ||
		!bundleDigestPattern.MatchString(event.HostFence.ConfigurationSHA256) ||
		!bundleDigestPattern.MatchString(event.HostFence.PreviousBundleSHA256) || event.HostFence.DatabaseSchema <= 0 ||
		!validCohortTimestamp(event.HostFence.ObservedAt) {
		return false
	}
	index := cohortHostIndex(receipt, event.HostID)
	if index < 0 {
		return false
	}
	host := receipt.Hosts[index]
	observedAt, err := time.Parse(time.RFC3339Nano, event.HostFence.ObservedAt)
	encoded, encodeErr := json.Marshal(*event.HostFence)
	digest := sha256.Sum256(encoded)
	return err == nil && encodeErr == nil && event.EvidenceSHA256 == hex.EncodeToString(digest[:]) &&
		!observedAt.After(eventAt) && eventAt.Sub(observedAt) <= 2*time.Minute &&
		event.HostFence.ServiceWasActive == host.ServiceActiveAtPreflight &&
		event.HostFence.WorkInventorySHA256 == host.WorkInventorySHA256 && host.WorkInventoryEmpty &&
		event.HostFence.ConfigurationSHA256 == host.ConfigurationSHA256 &&
		event.HostFence.SelectorTarget == host.PreviousTarget &&
		event.HostFence.PreviousBundleSHA256 == host.PreviousBundleSHA256 &&
		event.HostFence.DatabaseSchema == host.SchemaVersion
}

func validPreviousCatalogIdentity(host CatalogCohortHostPreflight, lane config.ReleaseLane) bool {
	if host.PreviousTarget == "" {
		if lane == config.ReleaseLanePRD {
			return bundleDigestPattern.MatchString(host.PreviousBundleSHA256) && host.PreviousReadSchemaMax > 0 &&
				host.LegacyRollback != nil && ValidateCatalogLegacyRollbackCompatibility(*host.LegacyRollback, host.PreviousBundleSHA256,
				host.ConfigurationSHA256, host.SchemaVersion) == nil && host.PreviousReadSchemaMax == host.LegacyRollback.ReadSchemaMax
		}
		return host.PreviousBundleSHA256 == "" && host.PreviousReadSchemaMax == 0
	}
	if strings.HasPrefix(host.PreviousTarget, "legacy-bin:") {
		return bundleDigestPattern.MatchString(strings.TrimPrefix(host.PreviousTarget, "legacy-bin:")) &&
			host.PreviousBundleSHA256 == strings.TrimPrefix(host.PreviousTarget, "legacy-bin:") &&
			(lane != config.ReleaseLanePRD || host.PreviousReadSchemaMax > 0 && host.LegacyRollback != nil &&
				ValidateCatalogLegacyRollbackCompatibility(*host.LegacyRollback, host.PreviousBundleSHA256, host.ConfigurationSHA256, host.SchemaVersion) == nil &&
				host.PreviousReadSchemaMax == host.LegacyRollback.ReadSchemaMax)
	}
	if strings.HasPrefix(host.PreviousTarget, "releases/") {
		return bundleDigestPattern.MatchString(strings.TrimPrefix(host.PreviousTarget, "releases/")) &&
			host.PreviousBundleSHA256 == strings.TrimPrefix(host.PreviousTarget, "releases/") &&
			(lane != config.ReleaseLanePRD || host.PreviousReadSchemaMax > 0 && host.LegacyRollback == nil)
	}
	if filepath.IsAbs(host.PreviousTarget) {
		plan, err := BuildCatalogPlan(host.HostID, lane)
		if err != nil {
			return false
		}
		root, err := DefaultCatalogRootForPlan(plan)
		return err == nil && filepath.Clean(host.PreviousTarget) == host.PreviousTarget &&
			host.PreviousTarget == filepath.Join(root, filepath.Base(filepath.Dir(host.PreviousTarget)), host.HostID) &&
			validCatalogReleaseID(filepath.Base(filepath.Dir(host.PreviousTarget))) && bundleDigestPattern.MatchString(host.PreviousBundleSHA256) &&
			(lane != config.ReleaseLanePRD || host.PreviousReadSchemaMax > 0 && host.LegacyRollback == nil)
	}
	return false
}

func cohortEvidenceFresh(receipt CatalogCohortReceipt, nowValue string) bool {
	now, err := time.Parse(time.RFC3339Nano, nowValue)
	if err != nil {
		return false
	}
	maxAge := catalogCohortEvidenceMaxAge
	for _, host := range receipt.Hosts {
		preflightAt, err := time.Parse(time.RFC3339Nano, host.PreflightAt)
		if err != nil || now.Before(preflightAt.Add(-5*time.Second)) || now.Sub(preflightAt) > maxAge {
			return false
		}
	}
	for _, proof := range receipt.FenceProofs {
		verifiedAt, err := time.Parse(time.RFC3339Nano, proof.VerifiedAt)
		if err != nil || now.Before(verifiedAt.Add(-5*time.Second)) || now.Sub(verifiedAt) > maxAge {
			return false
		}
	}
	return true
}

func catalogCohortEvidenceExpiresAt(receipt CatalogCohortReceipt) (time.Time, bool) {
	var earliest time.Time
	for _, host := range receipt.Hosts {
		at, err := time.Parse(time.RFC3339Nano, host.PreflightAt)
		if err != nil {
			return time.Time{}, false
		}
		if earliest.IsZero() || at.Before(earliest) {
			earliest = at
		}
	}
	for _, proof := range receipt.FenceProofs {
		at, err := time.Parse(time.RFC3339Nano, proof.VerifiedAt)
		if err != nil {
			return time.Time{}, false
		}
		if earliest.IsZero() || at.Before(earliest) {
			earliest = at
		}
	}
	if earliest.IsZero() {
		return time.Time{}, false
	}
	return earliest.Add(catalogCohortEvidenceMaxAge), true
}

func catalogCohortTransitionDigest(transition CatalogCohortTransition) string {
	transition.SHA256 = ""
	data, err := json.Marshal(transition)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func lastCohortTransitionDigest(transitions []CatalogCohortTransition) string {
	if len(transitions) == 0 {
		return ""
	}
	return transitions[len(transitions)-1].SHA256
}

func blockedCohortResumeStage(transitions []CatalogCohortTransition) CatalogCohortStage {
	for index := len(transitions) - 1; index >= 0; index-- {
		if transitions[index].Action == "block" {
			stage, _ := replayCatalogCohortTransitions(CatalogCohortReceipt{Transitions: transitions[:index]})
			return stage
		}
		if transitions[index].Action == "reconcile-history" {
			return transitions[index].ReconciledStage
		}
	}
	return ""
}

func eventHostEmpty(hostID string) bool { return hostID == "" }

func validCohortTimestamp(value string) bool {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && parsed.UTC().Format(time.RFC3339Nano) == value
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// WriteCatalogCohortReceipt stores one protected copy in this selected lane's
// staging area. Replacing a copy is allowed only when it is an exact replay or
// a hash-linked extension of the prior transition history.
func WriteCatalogCohortReceipt(plan Plan, receipt CatalogCohortReceipt) (string, error) {
	if validateCatalogPlan(plan) != nil || ValidateCatalogCohortReceipt(receipt) != nil || receipt.Lane != plan.Lane || !cohortContainsHost(receipt, plan.HostID) {
		return "", ErrCatalogOperationInvalid
	}
	root := filepath.Join(plan.Namespace.Staging, "catalog-cohorts")
	return writeCatalogCohortReceiptAt(root, receipt)
}

// SyncCatalogCohortReceipt validates this host's fresh stopped/empty local
// state and durable selector history before atomically updating its receipt
// copy. The same operation is used by the supported cohort CLI sync and
// reconcile-history actions.
func SyncCatalogCohortReceipt(ctx context.Context, plan Plan, receipt CatalogCohortReceipt) (string, error) {
	if validateCatalogPlan(plan) != nil || ValidateCatalogCohortReceipt(receipt) != nil || receipt.Lane != plan.Lane || !cohortContainsHost(receipt, plan.HostID) {
		return "", ErrCatalogOperationInvalid
	}
	root := filepath.Join(plan.Namespace.Staging, "catalog-cohorts")
	return syncCatalogCohortReceiptAt(root, receipt, func() error {
		return ValidateCatalogCohortHistoryLocalState(ctx, plan, receipt)
	})
}

func syncCatalogCohortReceiptAt(root string, receipt CatalogCohortReceipt, validateLocal func() error) (string, error) {
	if validateLocal == nil || ValidateCatalogCohortReceipt(receipt) != nil {
		return "", ErrCatalogOperationInvalid
	}
	if err := validateLocal(); err != nil {
		return "", err
	}
	return writeCatalogCohortReceiptAt(root, receipt)
}

func writeCatalogCohortReceiptAt(root string, receipt CatalogCohortReceipt) (string, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || ValidateCatalogCohortReceipt(receipt) != nil {
		return "", ErrCatalogOperationInvalid
	}
	if err := ensureCatalogPrivateDirectory(root); err != nil {
		return "", err
	}
	lock, err := acquireCohortReceiptLock(root)
	if err != nil {
		return "", err
	}
	defer releaseCatalogRootPublicationLock(lock)
	path := filepath.Join(root, receipt.OperationID+".json")
	if existing, readErr := readCatalogCohortReceipt(path); readErr == nil {
		if !catalogCohortReceiptUpdateAllowed(existing, receipt) {
			return "", ErrCatalogOperationInvalid
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return "", readErr
	}
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return "", err
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(root, ".cohort-*.tmp")
	if err != nil {
		return "", err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return "", err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return "", err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return "", err
	}
	if err := syncSimpleDirectory(root); err != nil {
		return "", err
	}
	return path, nil
}

func catalogCohortReceiptUpdateAllowed(existing, next CatalogCohortReceipt) bool {
	if !catalogCohortStaticIdentityEqual(existing, next) {
		return false
	}
	return catalogCohortTransitionPrefix(existing.Transitions, next.Transitions) ||
		catalogCohortReceiptHasHistoricalBranch(next, existing.Transitions)
}

// LoadCatalogCohortReceiptFile loads an owner-only canonical receipt supplied
// for local installation into the selected host's staging area.
func LoadCatalogCohortReceiptFile(path string) (CatalogCohortReceipt, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return CatalogCohortReceipt{}, ErrCatalogOperationInvalid
	}
	return readCatalogCohortReceipt(path)
}

// LoadCatalogCohortHostPreflightReportFile loads one canonical, owner-only
// report emitted by catalog-host-preflight. The host is checked again while
// creating the receipt, where it is bound to the sealed release manifest.
func LoadCatalogCohortHostPreflightReportFile(path string) (CatalogCohortHostPreflightReport, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return CatalogCohortHostPreflightReport{}, ErrCatalogOperationInvalid
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 ||
		info.Size() > 1<<20 || !catalogFileHasSingleLink(info) {
		return CatalogCohortHostPreflightReport{}, ErrCatalogOperationInvalid
	}
	var stat unix.Stat_t
	if unix.Lstat(path, &stat) != nil || stat.Uid != uint32(os.Getuid()) {
		return CatalogCohortHostPreflightReport{}, ErrCatalogOperationInvalid
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return CatalogCohortHostPreflightReport{}, err
	}
	var report CatalogCohortHostPreflightReport
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&report) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return CatalogCohortHostPreflightReport{}, ErrCatalogOperationInvalid
	}
	canonical, err := json.MarshalIndent(report, "", "  ")
	if err != nil || !bytes.Equal(data, append(canonical, '\n')) {
		return CatalogCohortHostPreflightReport{}, ErrCatalogOperationInvalid
	}
	return report, nil
}

// LoadCatalogLegacyRollbackCompatibilityFile loads the owner-only semantic
// test record supplied to an initial legacy-PRD preflight. Exact binary,
// configuration and schema identities are checked again against the host.
func LoadCatalogLegacyRollbackCompatibilityFile(path string) (CatalogLegacyRollbackCompatibility, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return CatalogLegacyRollbackCompatibility{}, ErrCatalogOperationInvalid
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 ||
		info.Size() > 1<<20 || !catalogFileHasSingleLink(info) {
		return CatalogLegacyRollbackCompatibility{}, ErrCatalogOperationInvalid
	}
	var stat unix.Stat_t
	if unix.Lstat(path, &stat) != nil || stat.Uid != uint32(os.Getuid()) {
		return CatalogLegacyRollbackCompatibility{}, ErrCatalogOperationInvalid
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return CatalogLegacyRollbackCompatibility{}, err
	}
	var evidence CatalogLegacyRollbackCompatibility
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&evidence) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return CatalogLegacyRollbackCompatibility{}, ErrCatalogOperationInvalid
	}
	canonical, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil || !bytes.Equal(data, append(canonical, '\n')) {
		return CatalogLegacyRollbackCompatibility{}, ErrCatalogOperationInvalid
	}
	return evidence, nil
}

func ReadCatalogCohortReceipt(plan Plan, operationID string) (CatalogCohortReceipt, error) {
	if validateCatalogPlan(plan) != nil || !validCatalogOperationID(operationID) {
		return CatalogCohortReceipt{}, ErrCatalogOperationInvalid
	}
	root := filepath.Join(plan.Namespace.Staging, "catalog-cohorts")
	if validateCatalogRootPath(root) != nil || validateExistingPrivateDirectory(root) != nil {
		return CatalogCohortReceipt{}, ErrCatalogOperationInvalid
	}
	receipt, err := readCatalogCohortReceipt(filepath.Join(root, operationID+".json"))
	if err != nil || !cohortContainsHost(receipt, plan.HostID) || receipt.Lane != plan.Lane {
		return CatalogCohortReceipt{}, ErrCatalogOperationInvalid
	}
	return receipt, nil
}

func readCatalogCohortReceipt(path string) (CatalogCohortReceipt, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return CatalogCohortReceipt{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > 1<<20 || !catalogFileHasSingleLink(info) {
		return CatalogCohortReceipt{}, ErrCatalogOperationInvalid
	}
	var stat unix.Stat_t
	if unix.Lstat(path, &stat) != nil || stat.Uid != uint32(os.Getuid()) {
		return CatalogCohortReceipt{}, ErrCatalogOperationInvalid
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return CatalogCohortReceipt{}, err
	}
	var receipt CatalogCohortReceipt
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&receipt) != nil || decoder.Decode(&struct{}{}) != io.EOF || ValidateCatalogCohortReceipt(receipt) != nil {
		return CatalogCohortReceipt{}, ErrCatalogOperationInvalid
	}
	canonical, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return CatalogCohortReceipt{}, ErrCatalogOperationInvalid
	}
	if bytes.Equal(data, append(canonical, '\n')) {
		return receipt, nil
	}
	legacyCanonical, legacyErr := marshalCatalogCohortReceiptWithoutServiceActiveField(receipt)
	if legacyErr != nil || !bytes.Equal(data, legacyCanonical) {
		return CatalogCohortReceipt{}, ErrCatalogOperationInvalid
	}
	return receipt, nil
}

// The first format-1 receipt writer predated ServiceActiveAtPreflight. Keep
// reading that exact canonical representation so terminal histories continue
// to participate in admission checks after an upgrade. The JSON decoder,
// receipt validator, file ownership/mode checks, and history digests still
// apply before this compatibility check. The omitted field can only represent
// false, which is also required here.
func marshalCatalogCohortReceiptWithoutServiceActiveField(receipt CatalogCohortReceipt) ([]byte, error) {
	type legacyHostPreflight struct {
		HostID                string                              `json:"host_id"`
		PreviousTarget        string                              `json:"previous_target,omitempty"`
		PreviousBundleSHA256  string                              `json:"previous_bundle_sha256,omitempty"`
		PreviousReadSchemaMax int                                 `json:"previous_read_schema_max,omitempty"`
		LegacyRollback        *CatalogLegacyRollbackCompatibility `json:"legacy_rollback_compatibility,omitempty"`
		Target                CatalogReleaseIdentity              `json:"target_release_identity"`
		ConfigurationSHA256   string                              `json:"configuration_sha256"`
		SchemaVersion         int                                 `json:"schema_version"`
		WorkInventorySHA256   string                              `json:"work_inventory_sha256"`
		WorkInventoryEmpty    bool                                `json:"work_inventory_empty"`
		PreflightAt           string                              `json:"preflight_at"`
		BackupDirectory       string                              `json:"backup_directory"`
		BackupManifestSHA256  string                              `json:"backup_manifest_sha256"`
		AvailableBytes        uint64                              `json:"available_bytes"`
		RequiredBytes         uint64                              `json:"required_bytes"`
	}
	type legacyReceipt struct {
		FormatVersion  int                          `json:"format_version"`
		OperationID    string                       `json:"operation_id"`
		Lane           config.ReleaseLane           `json:"lane"`
		ReleaseID      string                       `json:"release_id"`
		ReleaseSHA256  string                       `json:"release_sha256"`
		SourceRevision string                       `json:"source_revision"`
		Database       CatalogDatabaseCompatibility `json:"database_compatibility"`
		CreatedAt      string                       `json:"created_at"`
		UpdatedAt      string                       `json:"updated_at"`
		Stage          CatalogCohortStage           `json:"stage"`
		FenceProofs    []CatalogCohortFenceProof    `json:"fence_proofs"`
		Hosts          []legacyHostPreflight        `json:"hosts"`
		Transitions    []CatalogCohortTransition    `json:"transitions"`
	}

	legacy := legacyReceipt{
		FormatVersion: receipt.FormatVersion, OperationID: receipt.OperationID, Lane: receipt.Lane,
		ReleaseID: receipt.ReleaseID, ReleaseSHA256: receipt.ReleaseSHA256, SourceRevision: receipt.SourceRevision,
		Database: receipt.Database, CreatedAt: receipt.CreatedAt, UpdatedAt: receipt.UpdatedAt, Stage: receipt.Stage,
		FenceProofs: receipt.FenceProofs, Transitions: receipt.Transitions,
		Hosts: make([]legacyHostPreflight, len(receipt.Hosts)),
	}
	for index, host := range receipt.Hosts {
		if host.ServiceActiveAtPreflight {
			return nil, ErrCatalogOperationInvalid
		}
		legacy.Hosts[index] = legacyHostPreflight{
			HostID: host.HostID, PreviousTarget: host.PreviousTarget, PreviousBundleSHA256: host.PreviousBundleSHA256,
			PreviousReadSchemaMax: host.PreviousReadSchemaMax, LegacyRollback: host.LegacyRollback, Target: host.Target,
			ConfigurationSHA256: host.ConfigurationSHA256, SchemaVersion: host.SchemaVersion,
			WorkInventorySHA256: host.WorkInventorySHA256, WorkInventoryEmpty: host.WorkInventoryEmpty,
			PreflightAt: host.PreflightAt, BackupDirectory: host.BackupDirectory,
			BackupManifestSHA256: host.BackupManifestSHA256, AvailableBytes: host.AvailableBytes, RequiredBytes: host.RequiredBytes,
		}
	}
	canonical, err := json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(canonical, '\n'), nil
}

func acquireCohortReceiptLock(root string) (int, error) {
	path := filepath.Join(root, ".cohort-receipt.lock")
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return -1, err
	}
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != uint32(os.Getuid()) || stat.Mode&0o777 != 0o600 || stat.Nlink != 1 {
		_ = unix.Close(fd)
		return -1, ErrCatalogOperationInvalid
	}
	if err := unix.Flock(fd, unix.LOCK_EX); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

func catalogCohortTransitionPrefix(old, next []CatalogCohortTransition) bool {
	if len(old) > len(next) {
		return false
	}
	for index := range old {
		if !reflect.DeepEqual(old[index], next[index]) {
			return false
		}
	}
	return true
}

func catalogCohortStaticIdentityEqual(left, right CatalogCohortReceipt) bool {
	return left.FormatVersion == right.FormatVersion && left.OperationID == right.OperationID && left.Lane == right.Lane &&
		left.ReleaseID == right.ReleaseID && left.ReleaseSHA256 == right.ReleaseSHA256 && left.SourceRevision == right.SourceRevision &&
		left.CreatedAt == right.CreatedAt && reflect.DeepEqual(left.Database, right.Database) &&
		reflect.DeepEqual(left.FenceProofs, right.FenceProofs) && reflect.DeepEqual(left.Hosts, right.Hosts)
}

func cohortContainsHost(receipt CatalogCohortReceipt, hostID string) bool {
	for _, host := range receipt.Hosts {
		if host.HostID == hostID {
			return true
		}
	}
	return false
}

func validateCatalogCohortHostMaySwitch(receipt CatalogCohortReceipt, layout catalogSelectorLayout, currentTarget string,
	target CatalogReleaseIdentity, databaseSchema int, backupDirectory, backupManifestSHA256 string, now time.Time) error {
	if ValidateCatalogCohortReceipt(receipt) != nil || !cohortEvidenceFresh(receipt, now.UTC().Format(time.RFC3339Nano)) || receipt.Lane != layout.lane ||
		(receipt.Stage != CatalogCohortHeld && receipt.Stage != CatalogCohortSwitching) ||
		target.ReleaseID != receipt.ReleaseID || target.ReleaseSHA256 != receipt.ReleaseSHA256 || target.HostID != layout.hostID ||
		databaseSchema <= 0 || now.IsZero() {
		return ErrCatalogOperationInvalid
	}
	index := cohortHostIndex(receipt, layout.hostID)
	if index < 0 || index != countCohortSwitches(receipt) || lastCohortTransitionForHost(receipt, "preflight", layout.hostID) < 0 {
		return ErrCatalogOperationInvalid
	}
	host := receipt.Hosts[index]
	if validateCohortPreflightFreshness(host.PreflightAt, now) != nil ||
		currentTarget != host.PreviousTarget || target != host.Target || databaseSchema != host.SchemaVersion ||
		backupDirectory != host.BackupDirectory || backupManifestSHA256 != host.BackupManifestSHA256 {
		return ErrCatalogOperationInvalid
	}
	configurationSHA256, err := hashCatalogConfiguration(layout.configFile)
	if err != nil || configurationSHA256 != host.ConfigurationSHA256 {
		return ErrCatalogOperationInvalid
	}
	return nil
}

func recordCatalogCohortSwitch(plan Plan, operation CatalogSelectorOperation, reconciled bool) error {
	if validateCatalogPlan(plan) != nil || operation.CohortOperationID == "" || operation.HostID != plan.HostID || operation.Lane != plan.Lane {
		return ErrCatalogOperationInvalid
	}
	receipt, err := ReadCatalogCohortReceipt(plan, operation.CohortOperationID)
	if err != nil {
		return err
	}
	layout, err := catalogSelectorLayoutForPlan(plan, mustDefaultCatalogRoot(plan))
	if err != nil {
		return err
	}
	current, err := inspectCatalogSelector(layout)
	if err != nil || current.Target != operation.NextTarget || operation.Stage != catalogOperationComplete {
		return ErrCatalogSelectorConflict
	}
	evidence, err := catalogSelectorOperationDigest(layout, operation.OperationID)
	if err != nil {
		return err
	}
	if cohortHasSwitched(receipt, operation.HostID) {
		for _, transition := range receipt.Transitions {
			if (transition.Action == "switch" || transition.Action == "switch-reconciled") && transition.HostID == operation.HostID {
				if transition.EvidenceSHA256 == evidence {
					return nil
				}
				return ErrCatalogOperationInvalid
			}
		}
		return ErrCatalogOperationInvalid
	}
	index := cohortHostIndex(receipt, operation.HostID)
	if index < 0 || index != countCohortSwitches(receipt) {
		return ErrCatalogOperationInvalid
	}
	host := receipt.Hosts[index]
	if host.Target != operation.Release || host.PreviousTarget != operation.PreviousTarget ||
		host.BackupDirectory != operation.BackupDirectory || host.BackupManifestSHA256 != operation.BackupManifestSHA256 ||
		host.ConfigurationSHA256 != operation.ConfigurationSHA256 || host.SchemaVersion != operation.DatabaseSchemaAtSwitch {
		return ErrCatalogOperationInvalid
	}
	preflightIndex := lastCohortTransitionForHost(receipt, "preflight", operation.HostID)
	if preflightIndex < 0 {
		return ErrCatalogOperationInvalid
	}
	preflightAt, err := time.Parse(time.RFC3339Nano, receipt.Transitions[preflightIndex].At)
	createdAt, createdErr := time.Parse(time.RFC3339Nano, operation.CreatedUTC)
	if err != nil || createdErr != nil || !catalogCohortSwitchRecordTimingValid(preflightAt, createdAt, reconciled) {
		return ErrCatalogOperationInvalid
	}
	action := "switch"
	if reconciled {
		action = "switch-reconciled"
	}
	updated, err := AdvanceCatalogCohortReceipt(receipt, action, operation.HostID, evidence, time.Now().UTC())
	if err != nil {
		return err
	}
	_, err = WriteCatalogCohortReceipt(plan, updated)
	return err
}

// catalogCohortSwitchRecordTimingValid keeps new selector mutations tightly
// bound to their preflight. A reconciled mutation is different: the selector
// journal proves that the guarded switch already happened, and Reconcile has
// just repeated the stopped-service, empty-work, schema, configuration, and
// backup checks. Its receipt event must follow the original preflight, but a
// delayed recovery must not be stranded by the two-minute initiation window.
func catalogCohortSwitchRecordTimingValid(preflightAt, operationCreatedAt time.Time, reconciled bool) bool {
	if preflightAt.IsZero() || operationCreatedAt.IsZero() || operationCreatedAt.Before(preflightAt) {
		return false
	}
	return reconciled || operationCreatedAt.Sub(preflightAt) <= 2*time.Minute
}

func validateCatalogCohortHostMayRollback(receipt CatalogCohortReceipt, layout catalogSelectorLayout, operation CatalogSelectorOperation, currentTarget string) error {
	if ValidateCatalogCohortReceipt(receipt) != nil || receipt.OperationID != operation.CohortOperationID ||
		receipt.Lane != layout.lane || receipt.Stage != CatalogCohortRollingBack || operation.HostID != layout.hostID ||
		operation.Lane != layout.lane || operation.Stage != catalogOperationComplete || currentTarget != operation.NextTarget {
		return ErrCatalogOperationInvalid
	}
	switched := cohortSwitchedHosts(receipt)
	rolledBack := cohortRolledBackHosts(receipt)
	if len(switched) == 0 || len(rolledBack) >= len(switched) || switched[len(switched)-1-len(rolledBack)] != layout.hostID {
		return ErrCatalogOperationInvalid
	}
	index := cohortHostIndex(receipt, layout.hostID)
	if index < 0 {
		return ErrCatalogOperationInvalid
	}
	host := receipt.Hosts[index]
	if host.Target != operation.Release || host.PreviousTarget != operation.PreviousTarget {
		return ErrCatalogOperationInvalid
	}
	return nil
}

func recordCatalogCohortRollback(plan Plan, operation CatalogSelectorOperation) error {
	if validateCatalogPlan(plan) != nil || operation.CohortOperationID == "" || operation.HostID != plan.HostID ||
		operation.Lane != plan.Lane || operation.Stage != catalogOperationRolledBack {
		return ErrCatalogOperationInvalid
	}
	receipt, err := ReadCatalogCohortReceipt(plan, operation.CohortOperationID)
	if err != nil {
		return err
	}
	layout, err := catalogSelectorLayoutForPlan(plan, mustDefaultCatalogRoot(plan))
	if err != nil {
		return err
	}
	current, err := inspectCatalogSelector(layout)
	if err != nil || current.Target != operation.PreviousTarget {
		return ErrCatalogSelectorConflict
	}
	evidence, err := catalogSelectorOperationDigest(layout, operation.OperationID)
	if err != nil {
		return err
	}
	if containsString(cohortRolledBackHosts(receipt), operation.HostID) {
		for _, transition := range receipt.Transitions {
			if transition.Action == "rollback-host" && transition.HostID == operation.HostID {
				if transition.EvidenceSHA256 == evidence {
					return nil
				}
				return ErrCatalogOperationInvalid
			}
		}
		return ErrCatalogOperationInvalid
	}
	if validateCatalogCohortHostMayRollback(receipt, layout, CatalogSelectorOperation{
		FormatVersion: operation.FormatVersion, OperationID: operation.OperationID, CohortOperationID: operation.CohortOperationID,
		HostID: operation.HostID, Lane: operation.Lane, Release: operation.Release, PreviousTarget: operation.PreviousTarget,
		NextTarget: operation.NextTarget, ConfigurationSHA256: operation.ConfigurationSHA256,
		DatabaseSchemaAtSwitch: operation.DatabaseSchemaAtSwitch, BackupDirectory: operation.BackupDirectory,
		BackupManifestSHA256: operation.BackupManifestSHA256, Stage: catalogOperationComplete, CreatedUTC: operation.CreatedUTC,
	}, operation.NextTarget) != nil {
		return ErrCatalogOperationInvalid
	}
	updated, err := AdvanceCatalogCohortReceipt(receipt, "rollback-host", operation.HostID, evidence, time.Now().UTC())
	if err != nil {
		return err
	}
	_, err = WriteCatalogCohortReceipt(plan, updated)
	return err
}

func cohortHostIndex(receipt CatalogCohortReceipt, hostID string) int {
	for index, host := range receipt.Hosts {
		if host.HostID == hostID {
			return index
		}
	}
	return -1
}

func countCohortSwitches(receipt CatalogCohortReceipt) int { return len(cohortSwitchedHosts(receipt)) }

func cohortSwitchedHosts(receipt CatalogCohortReceipt) []string {
	return cohortSwitchedHostsFrom(receipt.Transitions)
}

func cohortRolledBackHosts(receipt CatalogCohortReceipt) []string {
	rolledBack := make(map[string]struct{})
	walkCatalogCohortTransitions(receipt.Transitions, func(transition CatalogCohortTransition) {
		if transition.Action == "rollback-host" {
			rolledBack[transition.HostID] = struct{}{}
		}
	})
	var hosts []string
	for index := len(catalogCohortSwitchOrder) - 1; index >= 0; index-- {
		if _, ok := rolledBack[catalogCohortSwitchOrder[index]]; ok {
			hosts = append(hosts, catalogCohortSwitchOrder[index])
		}
	}
	return hosts
}

func walkCatalogCohortTransitions(transitions []CatalogCohortTransition, visit func(CatalogCohortTransition)) {
	for _, transition := range transitions {
		visit(transition)
		if transition.Action == "reconcile-history" {
			for _, branch := range transition.HistoricalBranches {
				walkCatalogCohortTransitions(branch.Transitions, visit)
			}
		}
	}
}

func cohortHasSwitched(receipt CatalogCohortReceipt, hostID string) bool {
	return containsString(cohortSwitchedHosts(receipt), hostID)
}

func lastCohortTransitionForHost(receipt CatalogCohortReceipt, action, hostID string) int {
	for index := len(receipt.Transitions) - 1; index >= 0; index-- {
		transition := receipt.Transitions[index]
		if transition.Action == action && transition.HostID == hostID {
			return index
		}
	}
	return -1
}

func catalogSelectorOperationDigest(layout catalogSelectorLayout, operationID string) (string, error) {
	path := filepath.Join(layout.operationRoot, operationID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func mustDefaultCatalogRoot(plan Plan) string {
	root, _ := DefaultCatalogRootForPlan(plan)
	return root
}
