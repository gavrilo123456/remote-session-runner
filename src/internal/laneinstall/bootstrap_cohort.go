package laneinstall

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"
)

var (
	ErrInvalidBootstrapCohort = errors.New("bootstrap cohort contract or transition rejected")
	ErrBootstrapCohortOrder   = errors.New("bootstrap cohort is not in the required host order")
)

const (
	BootstrapCohortInstalling = "installing"
	BootstrapCohortAbandoning = "abandoning"
	BootstrapCohortAbandoned  = "abandoned"
	BootstrapCohortReady      = "ready_to_commit"
	BootstrapCohortComplete   = "complete"
)

var bootstrapHostOrder = []string{"mac-control-plane", "linux-host", "sandbox-host", "factory-sandbox"}

type BootstrapPublicKeyPins struct {
	AuthorityKeyID           string `json:"authority_key_id"`
	AuthorityFingerprint     string `json:"authority_public_key_fingerprint"`
	IssuerKeyID              string `json:"issuer_key_id"`
	IssuerFingerprint        string `json:"issuer_public_key_fingerprint"`
	CACertificateFingerprint string `json:"ca_certificate_fingerprint"`
}

// BootstrapArtifactPin is public build/install metadata. It cannot carry
// source, artifact bytes, commands, credentials, certificates, or private keys.
type BootstrapArtifactPin struct {
	Component        string `json:"component"`
	GOOS             string `json:"goos"`
	GOARCH           string `json:"goarch"`
	InstallPath      string `json:"install_path"`
	Owner            string `json:"owner"`
	Group            string `json:"group"`
	Mode             string `json:"mode"`
	SourceCommit     string `json:"source_commit"`
	ToolchainDigest  string `json:"toolchain_digest"`
	BuildInputDigest string `json:"build_input_digest"`
	ArtifactDigest   string `json:"artifact_digest"`
}

type BootstrapHostContract struct {
	HostID                    string                 `json:"host_id"`
	ManagementRoot            string                 `json:"management_root"`
	PKI                       PKIHostPlan            `json:"pki_mapping"`
	BootstrapAttestationKeyID string                 `json:"bootstrap_attestation_key_id"`
	BootstrapKeyFingerprint   string                 `json:"bootstrap_attestation_key_fingerprint"`
	ManagementPackage         HostManagementPackage  `json:"management_package"`
	Artifacts                 []BootstrapArtifactPin `json:"artifacts"`
}

// BootstrapCohortContract is the exact reviewed public package for one
// all-host bootstrap attempt. Private authority, CA, installer, and
// attestation key material is deliberately absent.
type BootstrapCohortContract struct {
	ApprovalRecordID string                  `json:"bootstrap_approval_record_id"`
	Generation       uint64                  `json:"cohort_generation"`
	NotBefore        time.Time               `json:"not_before"`
	NotAfter         time.Time               `json:"not_after"`
	SourceCommit     string                  `json:"source_commit"`
	GiteaCommit      string                  `json:"gitea_commit_id"`
	GitHubCommit     string                  `json:"github_commit_id"`
	PublicPins       BootstrapPublicKeyPins  `json:"public_key_pins"`
	Hosts            []BootstrapHostContract `json:"hosts"`
}

func (contract BootstrapCohortContract) Validate(now time.Time) error {
	if !isManagementRecordID(contract.ApprovalRecordID) || contract.Generation == 0 || now.IsZero() ||
		contract.NotBefore.Location() != time.UTC || contract.NotAfter.Location() != time.UTC ||
		contract.NotBefore.IsZero() || !contract.NotAfter.After(contract.NotBefore) || now.Before(contract.NotBefore) || !now.Before(contract.NotAfter) ||
		!isCommit(contract.SourceCommit) || contract.GiteaCommit != contract.SourceCommit || contract.GitHubCommit != contract.SourceCommit ||
		!validBootstrapKeyID(contract.PublicPins.AuthorityKeyID) || !validBootstrapKeyID(contract.PublicPins.IssuerKeyID) ||
		contract.PublicPins.AuthorityKeyID == contract.PublicPins.IssuerKeyID || !isSHA256Hex(contract.PublicPins.AuthorityFingerprint) ||
		!isSHA256Hex(contract.PublicPins.IssuerFingerprint) || !isSHA256Hex(contract.PublicPins.CACertificateFingerprint) ||
		contract.PublicPins.AuthorityFingerprint == contract.PublicPins.IssuerFingerprint ||
		contract.PublicPins.AuthorityFingerprint == contract.PublicPins.CACertificateFingerprint ||
		contract.PublicPins.IssuerFingerprint == contract.PublicPins.CACertificateFingerprint || len(contract.Hosts) != len(bootstrapHostOrder) {
		return ErrInvalidBootstrapCohort
	}
	seenKeyIDs := map[string]struct{}{contract.PublicPins.AuthorityKeyID: {}, contract.PublicPins.IssuerKeyID: {}}
	seenKeyPins := map[string]struct{}{contract.PublicPins.AuthorityFingerprint: {}, contract.PublicPins.IssuerFingerprint: {}, contract.PublicPins.CACertificateFingerprint: {}}
	for i, host := range contract.Hosts {
		if host.HostID != bootstrapHostOrder[i] || !isSHA256Hex(host.BootstrapKeyFingerprint) ||
			!validBootstrapKeyID(host.BootstrapAttestationKeyID) || !isManagementRoot(host.HostID, host.ManagementRoot) {
			return ErrInvalidBootstrapCohort
		}
		if _, duplicate := seenKeyIDs[host.BootstrapAttestationKeyID]; duplicate {
			return ErrInvalidBootstrapCohort
		}
		if _, duplicate := seenKeyPins[host.BootstrapKeyFingerprint]; duplicate {
			return ErrInvalidBootstrapCohort
		}
		seenKeyIDs[host.BootstrapAttestationKeyID] = struct{}{}
		seenKeyPins[host.BootstrapKeyFingerprint] = struct{}{}
		pkiPlan, err := BuildPKIHostPlan(host.HostID)
		management, managementErr := ManagementPackage(host.HostID)
		if err != nil || managementErr != nil || host.PKI != pkiPlan || !sameManagementPackage(host.ManagementPackage, management) ||
			len(host.Artifacts) != len(management.Components) {
			return ErrInvalidBootstrapCohort
		}
		goos, goarch, group := bootstrapPlatform(host.HostID)
		for index, component := range management.Components {
			pin := host.Artifacts[index]
			if pin.Component != component.Name || pin.GOOS != goos || pin.GOARCH != goarch || pin.InstallPath != filepath.Join(host.ManagementRoot, "bin", component.Name) ||
				pin.Owner != "root" || pin.Group != group || pin.Mode != "0755" || pin.SourceCommit != contract.SourceCommit ||
				!isManagementDigest(pin.ToolchainDigest) || !isManagementDigest(pin.BuildInputDigest) || !isManagementDigest(pin.ArtifactDigest) ||
				!bootstrapComponentPlatform(host.HostID, pin.Component, pin.GOOS, pin.GOARCH) {
				return ErrInvalidBootstrapCohort
			}
		}
	}
	return nil
}

func (contract BootstrapCohortContract) Digest() (string, error) {
	data, err := json.Marshal(contract)
	if err != nil {
		return "", ErrInvalidBootstrapCohort
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

func isManagementRoot(hostID, root string) bool {
	want := "/var/lib/remote-session-runner/management"
	if hostID == "mac-control-plane" {
		want = "/Library/Application Support/RemoteSessionRunner/management"
	}
	return root == want && filepath.IsAbs(root) && filepath.Clean(root) == root
}

func bootstrapPlatform(hostID string) (goos, goarch, group string) {
	switch hostID {
	case "mac-control-plane":
		return "darwin", "arm64", "wheel"
	case "sandbox-host":
		return "linux", "arm64", "root"
	default:
		return "linux", "amd64", "root"
	}
}

func bootstrapComponentPlatform(hostID, component, goos, goarch string) bool {
	wantOS, wantArch, _ := bootstrapPlatform(hostID)
	return goos == wantOS && goarch == wantArch && component != ""
}

func sameManagementPackage(got, want HostManagementPackage) bool {
	gotJSON, gotErr := json.Marshal(got)
	wantJSON, wantErr := json.Marshal(want)
	return gotErr == nil && wantErr == nil && string(gotJSON) == string(wantJSON)
}

func validBootstrapKeyID(value string) bool {
	if len(value) < 1 || len(value) > 128 || !((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z') || (value[0] >= '0' && value[0] <= '9')) {
		return false
	}
	return strings.Trim(value, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._:-") == ""
}

type BootstrapCohortHostProgress struct {
	HostID                    string    `json:"host_id"`
	L0EvidenceDigest          string    `json:"l0_evidence_digest,omitempty"`
	L0ObservedAt              time.Time `json:"l0_observed_at,omitempty"`
	L0ExpiresAt               time.Time `json:"l0_expires_at,omitempty"`
	IssuanceMandateID         string    `json:"pki_issuance_mandate_id,omitempty"`
	CertificateRequestID      string    `json:"pki_certificate_request_id,omitempty"`
	IssuerReceiptID           string    `json:"issuer_receipt_id,omitempty"`
	CertificatePackageDigest  string    `json:"certificate_package_digest,omitempty"`
	InstallerTupleDigest      string    `json:"installer_tuple_digest,omitempty"`
	InstallEvidenceDigest     string    `json:"install_evidence_digest,omitempty"`
	HealthEvidenceDigest      string    `json:"first_health_evidence_digest,omitempty"`
	ExceptionDisabledEvidence string    `json:"bootstrap_exception_disabled_evidence_digest,omitempty"`
	AbandonDisableEvidence    string    `json:"abandon_disable_evidence_digest,omitempty"`
}

type BootstrapCohortAttempt struct {
	Contract                BootstrapCohortContract       `json:"contract"`
	ContractDigest          string                        `json:"contract_digest"`
	Status                  string                        `json:"status"`
	NextHost                int                           `json:"next_host_index"`
	Hosts                   []BootstrapCohortHostProgress `json:"host_progress"`
	AbandonReasonDigest     string                        `json:"abandon_reason_digest,omitempty"`
	AbandonEvidenceDigest   string                        `json:"abandon_evidence_digest,omitempty"`
	CompletedManifestID     string                        `json:"completed_host_management_manifest_id,omitempty"`
	CompletedEvidenceDigest string                        `json:"completed_cohort_evidence_digest,omitempty"`
}

// BootstrapCohortLedger is append-only state owned by the root release
// authority journal. Callers advance it only through that journal so retries
// cannot reset the generation or evidence high-water mark.
type BootstrapCohortLedger struct {
	Attempts []BootstrapCohortAttempt `json:"attempts"`
}

func (ledger BootstrapCohortLedger) Start(contract BootstrapCohortContract, now time.Time) (BootstrapCohortLedger, error) {
	if err := ledger.Validate(); err != nil || contract.Validate(now) != nil {
		return BootstrapCohortLedger{}, ErrInvalidBootstrapCohort
	}
	contract = cloneBootstrapCohortContract(contract)
	if len(ledger.Attempts) > 0 {
		previous := ledger.Attempts[len(ledger.Attempts)-1]
		if previous.Status != BootstrapCohortAbandoned || contract.Generation <= previous.Contract.Generation ||
			contract.ApprovalRecordID == previous.Contract.ApprovalRecordID || !bootstrapRetryUsesFreshPins(previous.Contract, contract) ||
			!ledger.retryPinsAreFresh(contract) {
			return BootstrapCohortLedger{}, ErrInvalidBootstrapCohort
		}
	}
	digest, err := contract.Digest()
	if err != nil || ledger.hasContractDigest(digest) || ledger.hasGeneration(contract.Generation) || ledger.hasApprovalID(contract.ApprovalRecordID) {
		return BootstrapCohortLedger{}, ErrInvalidBootstrapCohort
	}
	next := cloneBootstrapCohortLedger(ledger)
	attempt := BootstrapCohortAttempt{Contract: contract, ContractDigest: digest, Status: BootstrapCohortInstalling,
		Hosts: make([]BootstrapCohortHostProgress, len(bootstrapHostOrder))}
	for i, host := range bootstrapHostOrder {
		attempt.Hosts[i].HostID = host
	}
	next.Attempts = append(next.Attempts, attempt)
	return next, nil
}

func (ledger BootstrapCohortLedger) RecordFreshL0(hostID, evidenceDigest string, observedAt, expiresAt, now time.Time) (BootstrapCohortLedger, error) {
	attempt, next, err := ledger.currentInstalling()
	if err != nil || attempt.NextHost >= len(bootstrapHostOrder) || hostID != bootstrapHostOrder[attempt.NextHost] ||
		!isManagementRecordID(evidenceDigest) || observedAt.IsZero() || expiresAt.IsZero() || !observedAt.Before(expiresAt) ||
		observedAt.After(now) || !now.Before(expiresAt) || observedAt.Before(attempt.Contract.NotBefore) || expiresAt.After(attempt.Contract.NotAfter) ||
		attempt.Hosts[attempt.NextHost].L0EvidenceDigest != "" || ledger.hasEvidenceDigest(evidenceDigest) {
		return BootstrapCohortLedger{}, ErrBootstrapCohortOrder
	}
	progress := &next.Attempts[len(next.Attempts)-1].Hosts[attempt.NextHost]
	progress.L0EvidenceDigest, progress.L0ObservedAt, progress.L0ExpiresAt = evidenceDigest, observedAt.UTC(), expiresAt.UTC()
	return next, nil
}

type BootstrapPKIIssuanceEvidence struct {
	HostID                   string
	ContractDigest           string
	Context                  PKIIssuanceContext
	IssuanceMandateID        string
	CertificateRequestID     string
	IssuerReceiptID          string
	CertificatePackageDigest string
	InstallerTupleDigest     string
}

func (ledger BootstrapCohortLedger) RecordMandate(mandate PKIIssuanceMandate) (BootstrapCohortLedger, error) {
	attempt, next, err := ledger.currentInstalling()
	if err != nil || attempt.NextHost >= len(bootstrapHostOrder) || mandate.HostID != bootstrapHostOrder[attempt.NextHost] || mandate.HostID == "mac-control-plane" ||
		ValidatePKIIssuanceMandateShape(mandate) != nil || mandate.Context.Mode != "bootstrap" {
		return BootstrapCohortLedger{}, ErrBootstrapCohortOrder
	}
	contractHost := attempt.Contract.Hosts[attempt.NextHost]
	wantContext := PKIIssuanceContext{Mode: "bootstrap", BootstrapContractDigest: attempt.ContractDigest,
		BootstrapGeneration: attempt.Contract.Generation, Installer: bootstrapInstallerTuple(contractHost),
		BootstrapAttestationKeyID: contractHost.BootstrapAttestationKeyID}
	progress := attempt.Hosts[attempt.NextHost]
	if progress.L0EvidenceDigest == "" || mandate.Context != wantContext ||
		mandate.NotBefore.Before(attempt.Contract.NotBefore) || mandate.ExpiresAt.After(attempt.Contract.NotAfter) ||
		mandate.NotBefore.Before(progress.L0ObservedAt) || !mandate.NotBefore.Before(progress.L0ExpiresAt) ||
		progress.IssuanceMandateID != "" || ledger.hasIssuanceMandate(mandate.ID) || ledger.hasEvidenceDigest(mandate.ID) {
		return BootstrapCohortLedger{}, ErrInvalidBootstrapCohort
	}
	stored := &next.Attempts[len(next.Attempts)-1].Hosts[attempt.NextHost]
	stored.IssuanceMandateID = mandate.ID
	return next, nil
}

func (ledger BootstrapCohortLedger) RecordBootstrapIssuance(evidence BootstrapPKIIssuanceEvidence, issuedAt time.Time) (BootstrapCohortLedger, error) {
	attempt, next, err := ledger.currentInstalling()
	if err != nil || attempt.NextHost >= len(bootstrapHostOrder) || evidence.HostID != bootstrapHostOrder[attempt.NextHost] || evidence.HostID == "mac-control-plane" ||
		evidence.ContractDigest != attempt.ContractDigest || !isManagementRecordID(evidence.IssuanceMandateID) ||
		!isManagementRecordID(evidence.CertificateRequestID) || !isManagementRecordID(evidence.IssuerReceiptID) ||
		!isManagementRecordID(evidence.CertificatePackageDigest) {
		return BootstrapCohortLedger{}, ErrBootstrapCohortOrder
	}
	progress := attempt.Hosts[attempt.NextHost]
	contractHost := attempt.Contract.Hosts[attempt.NextHost]
	wantContext := PKIIssuanceContext{Mode: "bootstrap", BootstrapContractDigest: attempt.ContractDigest,
		BootstrapGeneration: attempt.Contract.Generation, Installer: bootstrapInstallerTuple(contractHost),
		BootstrapAttestationKeyID: contractHost.BootstrapAttestationKeyID}
	installerDigest, digestErr := bootstrapInstallerTupleDigest(contractHost)
	if progress.IssuanceMandateID != evidence.IssuanceMandateID || progress.CertificateRequestID != "" ||
		evidence.Context != wantContext || digestErr != nil || evidence.InstallerTupleDigest != installerDigest ||
		issuedAt.IsZero() || issuedAt.Before(attempt.Contract.NotBefore) || !issuedAt.Before(attempt.Contract.NotAfter) ||
		!issuedAt.Before(progress.L0ExpiresAt) || !bootstrapIssuanceIDsDistinct(evidence) || ledger.hasIssuanceIdentity(evidence) ||
		ledger.hasEvidenceDigest(evidence.CertificateRequestID) || ledger.hasEvidenceDigest(evidence.IssuerReceiptID) ||
		ledger.hasEvidenceDigest(evidence.CertificatePackageDigest) {
		return BootstrapCohortLedger{}, ErrInvalidBootstrapCohort
	}
	stored := &next.Attempts[len(next.Attempts)-1].Hosts[attempt.NextHost]
	stored.CertificateRequestID = evidence.CertificateRequestID
	stored.IssuerReceiptID = evidence.IssuerReceiptID
	stored.CertificatePackageDigest = evidence.CertificatePackageDigest
	stored.InstallerTupleDigest = evidence.InstallerTupleDigest
	return next, nil
}

func (ledger BootstrapCohortLedger) RecordInstalled(hostID, evidenceDigest string, installedAt time.Time) (BootstrapCohortLedger, error) {
	attempt, next, err := ledger.currentInstalling()
	if err != nil || attempt.NextHost >= len(bootstrapHostOrder) || hostID != bootstrapHostOrder[attempt.NextHost] || !isManagementRecordID(evidenceDigest) {
		return BootstrapCohortLedger{}, ErrBootstrapCohortOrder
	}
	progress := attempt.Hosts[attempt.NextHost]
	if (hostID != "mac-control-plane" && progress.CertificatePackageDigest == "") || progress.InstallEvidenceDigest != "" || installedAt.IsZero() ||
		installedAt.Before(attempt.Contract.NotBefore) || !installedAt.Before(attempt.Contract.NotAfter) ||
		!installedAt.Before(progress.L0ExpiresAt) || ledger.hasEvidenceDigest(evidenceDigest) {
		return BootstrapCohortLedger{}, ErrInvalidBootstrapCohort
	}
	next.Attempts[len(next.Attempts)-1].Hosts[attempt.NextHost].InstallEvidenceDigest = evidenceDigest
	return next, nil
}

func (ledger BootstrapCohortLedger) RecordHealthy(hostID, evidenceDigest string, healthyAt time.Time) (BootstrapCohortLedger, error) {
	attempt, next, err := ledger.currentInstalling()
	if err != nil || attempt.NextHost >= len(bootstrapHostOrder) || hostID != bootstrapHostOrder[attempt.NextHost] ||
		!isManagementRecordID(evidenceDigest) || ledger.hasEvidenceDigest(evidenceDigest) {
		return BootstrapCohortLedger{}, ErrBootstrapCohortOrder
	}
	progress := &next.Attempts[len(next.Attempts)-1].Hosts[attempt.NextHost]
	if progress.InstallEvidenceDigest == "" || progress.HealthEvidenceDigest != "" || healthyAt.IsZero() ||
		healthyAt.Before(attempt.Contract.NotBefore) || !healthyAt.Before(attempt.Contract.NotAfter) || !healthyAt.Before(progress.L0ExpiresAt) {
		return BootstrapCohortLedger{}, ErrInvalidBootstrapCohort
	}
	progress.HealthEvidenceDigest = evidenceDigest
	return next, nil
}

func (ledger BootstrapCohortLedger) DisableHostException(hostID, evidenceDigest string) (BootstrapCohortLedger, error) {
	attempt, next, err := ledger.currentInstalling()
	if err != nil || attempt.NextHost >= len(bootstrapHostOrder) || hostID != bootstrapHostOrder[attempt.NextHost] ||
		!isManagementRecordID(evidenceDigest) || ledger.hasEvidenceDigest(evidenceDigest) {
		return BootstrapCohortLedger{}, ErrBootstrapCohortOrder
	}
	progress := &next.Attempts[len(next.Attempts)-1].Hosts[attempt.NextHost]
	if progress.HealthEvidenceDigest == "" {
		return BootstrapCohortLedger{}, ErrBootstrapCohortOrder
	}
	if progress.ExceptionDisabledEvidence != "" {
		return BootstrapCohortLedger{}, ErrInvalidBootstrapCohort
	}
	progress.ExceptionDisabledEvidence = evidenceDigest
	next.Attempts[len(next.Attempts)-1].NextHost++
	if next.Attempts[len(next.Attempts)-1].NextHost == len(bootstrapHostOrder) {
		next.Attempts[len(next.Attempts)-1].Status = BootstrapCohortReady
	}
	return next, nil
}

func (ledger BootstrapCohortLedger) Complete(manifestID, evidenceDigest string) (BootstrapCohortLedger, error) {
	if ledger.Validate() != nil || len(ledger.Attempts) == 0 || !isManagementRecordID(manifestID) || !isManagementRecordID(evidenceDigest) ||
		manifestID == evidenceDigest || ledger.hasEvidenceDigest(manifestID) || ledger.hasEvidenceDigest(evidenceDigest) {
		return BootstrapCohortLedger{}, ErrInvalidBootstrapCohort
	}
	next := cloneBootstrapCohortLedger(ledger)
	attempt := &next.Attempts[len(next.Attempts)-1]
	if attempt.Status != BootstrapCohortReady || attempt.NextHost != len(bootstrapHostOrder) {
		return BootstrapCohortLedger{}, ErrBootstrapCohortOrder
	}
	attempt.Status = BootstrapCohortComplete
	attempt.CompletedManifestID = manifestID
	attempt.CompletedEvidenceDigest = evidenceDigest
	return next, nil
}

func (ledger BootstrapCohortLedger) BeginAbandonment(reasonDigest, evidenceDigest string) (BootstrapCohortLedger, error) {
	if ledger.Validate() != nil || len(ledger.Attempts) == 0 || !isManagementRecordID(reasonDigest) || !isManagementRecordID(evidenceDigest) ||
		reasonDigest == evidenceDigest || ledger.hasEvidenceDigest(reasonDigest) || ledger.hasEvidenceDigest(evidenceDigest) {
		return BootstrapCohortLedger{}, ErrInvalidBootstrapCohort
	}
	next := cloneBootstrapCohortLedger(ledger)
	attempt := &next.Attempts[len(next.Attempts)-1]
	if attempt.Status == BootstrapCohortComplete || attempt.Status == BootstrapCohortAbandoned || attempt.Status == BootstrapCohortAbandoning {
		return BootstrapCohortLedger{}, ErrBootstrapCohortOrder
	}
	attempt.Status = BootstrapCohortAbandoning
	attempt.AbandonReasonDigest = reasonDigest
	attempt.AbandonEvidenceDigest = evidenceDigest
	return next, nil
}

func (ledger BootstrapCohortLedger) ConfirmAbandonDisable(hostID, evidenceDigest string) (BootstrapCohortLedger, error) {
	if ledger.Validate() != nil || len(ledger.Attempts) == 0 || !isManagementRecordID(evidenceDigest) || ledger.hasEvidenceDigest(evidenceDigest) {
		return BootstrapCohortLedger{}, ErrInvalidBootstrapCohort
	}
	next := cloneBootstrapCohortLedger(ledger)
	attempt := &next.Attempts[len(next.Attempts)-1]
	if attempt.Status != BootstrapCohortAbandoning {
		return BootstrapCohortLedger{}, ErrBootstrapCohortOrder
	}
	index := bootstrapHostIndex(hostID)
	if index < 0 || attempt.Hosts[index].AbandonDisableEvidence != "" {
		return BootstrapCohortLedger{}, ErrInvalidBootstrapCohort
	}
	attempt.Hosts[index].AbandonDisableEvidence = evidenceDigest
	allDisabled := true
	for _, host := range attempt.Hosts {
		allDisabled = allDisabled && host.AbandonDisableEvidence != ""
	}
	if allDisabled {
		attempt.Status = BootstrapCohortAbandoned
	}
	return next, nil
}

func (ledger BootstrapCohortLedger) Validate() error {
	seenGeneration := make(map[uint64]struct{}, len(ledger.Attempts))
	seenContract := make(map[string]struct{}, len(ledger.Attempts))
	seenApproval := make(map[string]struct{}, len(ledger.Attempts))
	seenEvidence := make(map[string]struct{})
	for i, attempt := range ledger.Attempts {
		if attempt.Contract.Validate(attempt.Contract.NotBefore) != nil || attempt.ContractDigest == "" {
			return ErrInvalidBootstrapCohort
		}
		digest, err := attempt.Contract.Digest()
		if err != nil || digest != attempt.ContractDigest {
			return ErrInvalidBootstrapCohort
		}
		if _, duplicate := seenGeneration[attempt.Contract.Generation]; duplicate {
			return ErrInvalidBootstrapCohort
		}
		if _, duplicate := seenContract[attempt.ContractDigest]; duplicate {
			return ErrInvalidBootstrapCohort
		}
		if _, duplicate := seenApproval[attempt.Contract.ApprovalRecordID]; duplicate {
			return ErrInvalidBootstrapCohort
		}
		seenGeneration[attempt.Contract.Generation] = struct{}{}
		seenContract[attempt.ContractDigest] = struct{}{}
		seenApproval[attempt.Contract.ApprovalRecordID] = struct{}{}
		if len(attempt.Hosts) != len(bootstrapHostOrder) || attempt.NextHost < 0 || attempt.NextHost > len(bootstrapHostOrder) {
			return ErrInvalidBootstrapCohort
		}
		if i > 0 {
			priorLedger := BootstrapCohortLedger{Attempts: ledger.Attempts[:i]}
			previous := ledger.Attempts[i-1]
			if previous.Status != BootstrapCohortAbandoned || attempt.Contract.Generation <= previous.Contract.Generation ||
				!bootstrapRetryUsesFreshPins(previous.Contract, attempt.Contract) || !priorLedger.retryPinsAreFresh(attempt.Contract) {
				return ErrInvalidBootstrapCohort
			}
		}
		for hostIndex, progress := range attempt.Hosts {
			if progress.HostID != bootstrapHostOrder[hostIndex] || !bootstrapProgressValid(attempt, hostIndex, progress) {
				return ErrInvalidBootstrapCohort
			}
		}
		if !bootstrapAttemptValid(attempt) {
			return ErrInvalidBootstrapCohort
		}
		ids := []string{attempt.ContractDigest, attempt.Contract.ApprovalRecordID, attempt.AbandonReasonDigest,
			attempt.AbandonEvidenceDigest, attempt.CompletedManifestID, attempt.CompletedEvidenceDigest}
		for _, progress := range attempt.Hosts {
			ids = append(ids, progress.L0EvidenceDigest, progress.IssuanceMandateID, progress.CertificateRequestID,
				progress.IssuerReceiptID, progress.CertificatePackageDigest, progress.InstallEvidenceDigest,
				progress.HealthEvidenceDigest, progress.ExceptionDisabledEvidence, progress.AbandonDisableEvidence)
		}
		for _, id := range ids {
			if id == "" {
				continue
			}
			if _, duplicate := seenEvidence[id]; duplicate {
				return ErrInvalidBootstrapCohort
			}
			seenEvidence[id] = struct{}{}
		}
	}
	return nil
}

func bootstrapProgressValid(attempt BootstrapCohortAttempt, index int, progress BootstrapCohortHostProgress) bool {
	if progress.L0EvidenceDigest != "" && (!isManagementRecordID(progress.L0EvidenceDigest) || progress.L0ObservedAt.IsZero() ||
		progress.L0ExpiresAt.IsZero() || progress.L0ObservedAt.Location() != time.UTC || progress.L0ExpiresAt.Location() != time.UTC ||
		!progress.L0ExpiresAt.After(progress.L0ObservedAt)) {
		return false
	}
	for _, value := range []string{progress.IssuanceMandateID, progress.CertificateRequestID, progress.IssuerReceiptID,
		progress.CertificatePackageDigest, progress.InstallerTupleDigest, progress.InstallEvidenceDigest,
		progress.HealthEvidenceDigest, progress.ExceptionDisabledEvidence, progress.AbandonDisableEvidence} {
		if value != "" && !isManagementRecordID(value) {
			return false
		}
	}
	if progress.InstallEvidenceDigest != "" && progress.L0EvidenceDigest == "" {
		return false
	}
	if progress.InstallEvidenceDigest != "" && progress.HostID != "mac-control-plane" &&
		(progress.IssuanceMandateID == "" || progress.CertificateRequestID == "" || progress.IssuerReceiptID == "" ||
			progress.CertificatePackageDigest == "" || progress.InstallerTupleDigest == "") {
		return false
	}
	issuanceFields := []string{progress.CertificateRequestID, progress.IssuerReceiptID, progress.CertificatePackageDigest, progress.InstallerTupleDigest}
	issuancePresent := 0
	for _, value := range issuanceFields {
		if value != "" {
			issuancePresent++
		}
	}
	if (issuancePresent != 0 && issuancePresent != len(issuanceFields)) || (issuancePresent != 0 && progress.IssuanceMandateID == "") {
		return false
	}
	if progress.HealthEvidenceDigest != "" && progress.InstallEvidenceDigest == "" ||
		progress.ExceptionDisabledEvidence != "" && progress.HealthEvidenceDigest == "" {
		return false
	}
	// The current host may have L0, install, and first-health evidence while
	// its one-time exception is still enabled. Later hosts must remain untouched
	// until that host is healthy and the exception-disable proof is recorded.
	if index > attempt.NextHost && attempt.Status != BootstrapCohortAbandoning && attempt.Status != BootstrapCohortAbandoned &&
		(progress.L0EvidenceDigest != "" || progress.InstallEvidenceDigest != "" || progress.HealthEvidenceDigest != "" || progress.ExceptionDisabledEvidence != "") {
		return false
	}
	return true
}

func bootstrapAttemptValid(attempt BootstrapCohortAttempt) bool {
	switch attempt.Status {
	case BootstrapCohortInstalling:
		if attempt.NextHost >= len(bootstrapHostOrder) || attempt.AbandonReasonDigest != "" || attempt.CompletedManifestID != "" {
			return false
		}
		for i := 0; i < attempt.NextHost; i++ {
			if attempt.Hosts[i].ExceptionDisabledEvidence == "" {
				return false
			}
		}
		return attempt.Hosts[attempt.NextHost].ExceptionDisabledEvidence == ""
	case BootstrapCohortReady:
		if attempt.NextHost != len(bootstrapHostOrder) || attempt.AbandonReasonDigest != "" || attempt.CompletedManifestID != "" {
			return false
		}
		for _, host := range attempt.Hosts {
			if host.InstallEvidenceDigest == "" || host.HealthEvidenceDigest == "" || host.ExceptionDisabledEvidence == "" {
				return false
			}
		}
		return true
	case BootstrapCohortAbandoning:
		return isManagementRecordID(attempt.AbandonReasonDigest) && isManagementRecordID(attempt.AbandonEvidenceDigest) && attempt.CompletedManifestID == ""
	case BootstrapCohortAbandoned:
		if !isManagementRecordID(attempt.AbandonReasonDigest) || !isManagementRecordID(attempt.AbandonEvidenceDigest) || attempt.CompletedManifestID != "" {
			return false
		}
		for _, host := range attempt.Hosts {
			if host.AbandonDisableEvidence == "" {
				return false
			}
		}
		return true
	case BootstrapCohortComplete:
		if attempt.NextHost != len(bootstrapHostOrder) || !isManagementRecordID(attempt.CompletedManifestID) || !isManagementRecordID(attempt.CompletedEvidenceDigest) || attempt.AbandonReasonDigest != "" {
			return false
		}
		for _, host := range attempt.Hosts {
			if host.InstallEvidenceDigest == "" || host.HealthEvidenceDigest == "" || host.ExceptionDisabledEvidence == "" {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func (ledger BootstrapCohortLedger) currentInstalling() (BootstrapCohortAttempt, BootstrapCohortLedger, error) {
	if ledger.Validate() != nil || len(ledger.Attempts) == 0 || ledger.Attempts[len(ledger.Attempts)-1].Status != BootstrapCohortInstalling {
		return BootstrapCohortAttempt{}, BootstrapCohortLedger{}, ErrInvalidBootstrapCohort
	}
	return ledger.Attempts[len(ledger.Attempts)-1], cloneBootstrapCohortLedger(ledger), nil
}

func bootstrapRetryUsesFreshPins(previous, next BootstrapCohortContract) bool {
	if previous.PublicPins.AuthorityKeyID == next.PublicPins.AuthorityKeyID || previous.PublicPins.IssuerKeyID == next.PublicPins.IssuerKeyID ||
		previous.PublicPins.AuthorityFingerprint == next.PublicPins.AuthorityFingerprint || previous.PublicPins.IssuerFingerprint == next.PublicPins.IssuerFingerprint ||
		previous.PublicPins.CACertificateFingerprint == next.PublicPins.CACertificateFingerprint || len(previous.Hosts) != len(next.Hosts) {
		return false
	}
	for i := range previous.Hosts {
		if previous.Hosts[i].HostID != next.Hosts[i].HostID ||
			previous.Hosts[i].BootstrapAttestationKeyID == next.Hosts[i].BootstrapAttestationKeyID ||
			previous.Hosts[i].BootstrapKeyFingerprint == next.Hosts[i].BootstrapKeyFingerprint {
			return false
		}
	}
	return true
}

func (ledger BootstrapCohortLedger) retryPinsAreFresh(next BootstrapCohortContract) bool {
	for _, attempt := range ledger.Attempts {
		if attempt.Contract.PublicPins.AuthorityKeyID == next.PublicPins.AuthorityKeyID || attempt.Contract.PublicPins.IssuerKeyID == next.PublicPins.IssuerKeyID ||
			attempt.Contract.PublicPins.AuthorityFingerprint == next.PublicPins.AuthorityFingerprint ||
			attempt.Contract.PublicPins.IssuerFingerprint == next.PublicPins.IssuerFingerprint ||
			attempt.Contract.PublicPins.CACertificateFingerprint == next.PublicPins.CACertificateFingerprint ||
			len(attempt.Contract.Hosts) != len(next.Hosts) {
			return false
		}
		for i := range next.Hosts {
			if attempt.Contract.Hosts[i].HostID != next.Hosts[i].HostID ||
				attempt.Contract.Hosts[i].BootstrapAttestationKeyID == next.Hosts[i].BootstrapAttestationKeyID ||
				attempt.Contract.Hosts[i].BootstrapKeyFingerprint == next.Hosts[i].BootstrapKeyFingerprint {
				return false
			}
		}
	}
	return true
}

func (ledger BootstrapCohortLedger) hasContractDigest(value string) bool {
	for _, attempt := range ledger.Attempts {
		if attempt.ContractDigest == value {
			return true
		}
	}
	return false
}

func (ledger BootstrapCohortLedger) hasGeneration(value uint64) bool {
	for _, attempt := range ledger.Attempts {
		if attempt.Contract.Generation == value {
			return true
		}
	}
	return false
}

func (ledger BootstrapCohortLedger) hasApprovalID(value string) bool {
	for _, attempt := range ledger.Attempts {
		if attempt.Contract.ApprovalRecordID == value {
			return true
		}
	}
	return false
}

func (ledger BootstrapCohortLedger) hasEvidenceDigest(value string) bool {
	for _, attempt := range ledger.Attempts {
		if attempt.ContractDigest == value || attempt.Contract.ApprovalRecordID == value || attempt.AbandonReasonDigest == value ||
			attempt.AbandonEvidenceDigest == value || attempt.CompletedManifestID == value || attempt.CompletedEvidenceDigest == value {
			return true
		}
		for _, host := range attempt.Hosts {
			for _, candidate := range []string{host.L0EvidenceDigest, host.IssuanceMandateID, host.CertificateRequestID, host.IssuerReceiptID,
				host.CertificatePackageDigest, host.InstallEvidenceDigest, host.HealthEvidenceDigest,
				host.ExceptionDisabledEvidence, host.AbandonDisableEvidence} {
				if candidate == value {
					return true
				}
			}
		}
	}
	return false
}

func bootstrapIssuanceIDsDistinct(evidence BootstrapPKIIssuanceEvidence) bool {
	ids := []string{evidence.IssuanceMandateID, evidence.CertificateRequestID, evidence.IssuerReceiptID, evidence.CertificatePackageDigest}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, duplicate := seen[id]; duplicate {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}

func (ledger BootstrapCohortLedger) hasIssuanceIdentity(evidence BootstrapPKIIssuanceEvidence) bool {
	for _, attempt := range ledger.Attempts {
		for _, host := range attempt.Hosts {
			if host.CertificateRequestID == evidence.CertificateRequestID || host.IssuerReceiptID == evidence.IssuerReceiptID ||
				host.CertificatePackageDigest == evidence.CertificatePackageDigest {
				return true
			}
		}
	}
	return false
}

func (ledger BootstrapCohortLedger) hasIssuanceMandate(mandateID string) bool {
	for _, attempt := range ledger.Attempts {
		for _, host := range attempt.Hosts {
			if host.IssuanceMandateID == mandateID {
				return true
			}
		}
	}
	return false
}

func bootstrapInstallerTuple(host BootstrapHostContract) PKIInstallerArtifactTuple {
	for _, pin := range host.Artifacts {
		if pin.Component == "runner-release-transport-pki-installer" {
			return PKIInstallerArtifactTuple{SourceCommit: pin.SourceCommit, ToolchainDigest: strings.TrimPrefix(pin.ToolchainDigest, "sha256:"),
				InputDigest: strings.TrimPrefix(pin.BuildInputDigest, "sha256:"), BinaryDigest: strings.TrimPrefix(pin.ArtifactDigest, "sha256:")}
		}
	}
	return PKIInstallerArtifactTuple{}
}

func bootstrapInstallerTupleDigest(host BootstrapHostContract) (string, error) {
	data, err := json.Marshal(bootstrapInstallerTuple(host))
	if err != nil || bootstrapInstallerTuple(host) == (PKIInstallerArtifactTuple{}) {
		return "", ErrInvalidBootstrapCohort
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

// BootstrapInstallerTupleDigest returns the public digest of the contract's
// pinned host installer tuple, for recording a verified issuance receipt.
func (contract BootstrapCohortContract) BootstrapInstallerTupleDigest(hostID string) (string, error) {
	for _, host := range contract.Hosts {
		if host.HostID == hostID {
			return bootstrapInstallerTupleDigest(host)
		}
	}
	return "", ErrInvalidBootstrapCohort
}

func bootstrapHostIndex(hostID string) int {
	for index, candidate := range bootstrapHostOrder {
		if candidate == hostID {
			return index
		}
	}
	return -1
}

func cloneBootstrapCohortLedger(ledger BootstrapCohortLedger) BootstrapCohortLedger {
	data, _ := json.Marshal(ledger)
	var cloned BootstrapCohortLedger
	_ = json.Unmarshal(data, &cloned)
	return cloned
}

func cloneBootstrapCohortContract(contract BootstrapCohortContract) BootstrapCohortContract {
	data, _ := json.Marshal(contract)
	var cloned BootstrapCohortContract
	_ = json.Unmarshal(data, &cloned)
	return cloned
}
