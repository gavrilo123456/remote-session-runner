package release

import (
	"encoding/json"
	"time"

	"remote-session-runner/src/internal/laneinstall"
)

// BootstrapCohortSnapshot returns the durable append-only cohort ledger.
func (j *AuthorityJournal) BootstrapCohortSnapshot() (laneinstall.BootstrapCohortLedger, error) {
	state, err := j.snapshot()
	if err != nil {
		return laneinstall.BootstrapCohortLedger{}, err
	}
	return state.BootstrapCohort, nil
}

func (j *AuthorityJournal) BeginBootstrapCohort(contract laneinstall.BootstrapCohortContract, now time.Time) error {
	return j.advanceBootstrapCohort(func(current laneinstall.BootstrapCohortLedger) (laneinstall.BootstrapCohortLedger, error) {
		return current.Start(contract, now)
	})
}

func (j *AuthorityJournal) RecordBootstrapCohortL0(hostID, evidenceDigest string, observedAt, expiresAt, now time.Time) error {
	return j.advanceBootstrapCohort(func(current laneinstall.BootstrapCohortLedger) (laneinstall.BootstrapCohortLedger, error) {
		return current.RecordFreshL0(hostID, evidenceDigest, observedAt, expiresAt, now)
	})
}

func (j *AuthorityJournal) RecordBootstrapCohortInstallation(hostID, evidenceDigest string, installedAt time.Time) error {
	return j.advanceBootstrapCohort(func(current laneinstall.BootstrapCohortLedger) (laneinstall.BootstrapCohortLedger, error) {
		return current.RecordInstalled(hostID, evidenceDigest, installedAt)
	})
}

func (j *AuthorityJournal) RecordBootstrapCohortHealth(hostID, evidenceDigest string, healthyAt time.Time) error {
	_, err := j.mutate(func(next *authorityRootState) error {
		updated, advanceErr := next.BootstrapCohort.RecordHealthy(hostID, evidenceDigest, healthyAt)
		if advanceErr != nil {
			return advanceErr
		}
		next.BootstrapCohort = updated
		if hostID != "mac-control-plane" {
			next.PKIBootstrapHealth[hostID] = true
		}
		return nil
	})
	return err
}

func (j *AuthorityJournal) DisableBootstrapCohortException(hostID, evidenceDigest string) error {
	return j.advanceBootstrapCohort(func(current laneinstall.BootstrapCohortLedger) (laneinstall.BootstrapCohortLedger, error) {
		return current.DisableHostException(hostID, evidenceDigest)
	})
}

func (j *AuthorityJournal) CompleteBootstrapCohort(manifestID, evidenceDigest string) error {
	return j.advanceBootstrapCohort(func(current laneinstall.BootstrapCohortLedger) (laneinstall.BootstrapCohortLedger, error) {
		return current.Complete(manifestID, evidenceDigest)
	})
}

func (j *AuthorityJournal) BeginBootstrapCohortAbandonment(reasonDigest, evidenceDigest string) error {
	return j.advanceBootstrapCohort(func(current laneinstall.BootstrapCohortLedger) (laneinstall.BootstrapCohortLedger, error) {
		return current.BeginAbandonment(reasonDigest, evidenceDigest)
	})
}

func (j *AuthorityJournal) ConfirmBootstrapCohortAbandonDisable(hostID, evidenceDigest string) error {
	return j.advanceBootstrapCohort(func(current laneinstall.BootstrapCohortLedger) (laneinstall.BootstrapCohortLedger, error) {
		return current.ConfirmAbandonDisable(hostID, evidenceDigest)
	})
}

func (j *AuthorityJournal) advanceBootstrapCohort(advance func(laneinstall.BootstrapCohortLedger) (laneinstall.BootstrapCohortLedger, error)) error {
	_, err := j.mutate(func(next *authorityRootState) error {
		updated, advanceErr := advance(next.BootstrapCohort)
		if advanceErr != nil {
			return advanceErr
		}
		next.BootstrapCohort = updated
		return nil
	})
	return err
}

func bootstrapCohortStatePreserved(before, after laneinstall.BootstrapCohortLedger) bool {
	if before.Validate() != nil || after.Validate() != nil || len(after.Attempts) < len(before.Attempts) ||
		len(after.Attempts) > len(before.Attempts)+1 {
		return false
	}
	stableAttempts := len(before.Attempts)
	if len(after.Attempts) == len(before.Attempts) && stableAttempts > 0 {
		stableAttempts-- // only the active tail may advance through its state machine
	}
	for i := 0; i < stableAttempts; i++ {
		beforeBytes, beforeErr := json.Marshal(before.Attempts[i])
		afterBytes, afterErr := json.Marshal(after.Attempts[i])
		if beforeErr != nil || afterErr != nil || string(beforeBytes) != string(afterBytes) {
			return false
		}
	}
	if stableAttempts < len(before.Attempts) {
		prior, next := before.Attempts[stableAttempts], after.Attempts[stableAttempts]
		priorContract, priorErr := json.Marshal(prior.Contract)
		nextContract, nextErr := json.Marshal(next.Contract)
		if priorErr != nil || nextErr != nil || string(priorContract) != string(nextContract) || prior.ContractDigest != next.ContractDigest ||
			!bootstrapAttemptTransitionValid(prior, next) {
			return false
		}
	}
	return true
}

func bootstrapAttemptTransitionValid(before, after laneinstall.BootstrapCohortAttempt) bool {
	if after.NextHost < before.NextHost || after.NextHost > before.NextHost+1 || len(before.Hosts) != len(after.Hosts) {
		return false
	}
	switch before.Status {
	case laneinstall.BootstrapCohortInstalling:
		if after.Status != laneinstall.BootstrapCohortInstalling && after.Status != laneinstall.BootstrapCohortReady && after.Status != laneinstall.BootstrapCohortAbandoning {
			return false
		}
	case laneinstall.BootstrapCohortReady:
		if after.Status != laneinstall.BootstrapCohortReady && after.Status != laneinstall.BootstrapCohortComplete && after.Status != laneinstall.BootstrapCohortAbandoning {
			return false
		}
	case laneinstall.BootstrapCohortAbandoning:
		if after.Status != laneinstall.BootstrapCohortAbandoning && after.Status != laneinstall.BootstrapCohortAbandoned {
			return false
		}
	case laneinstall.BootstrapCohortAbandoned:
		if after.Status != laneinstall.BootstrapCohortAbandoned {
			return false
		}
	case laneinstall.BootstrapCohortComplete:
		if after.Status != laneinstall.BootstrapCohortComplete {
			return false
		}
	default:
		return false
	}
	if before.NextHost == after.NextHost+1 || after.NextHost == before.NextHost+1 {
		if before.Status != laneinstall.BootstrapCohortInstalling || before.NextHost >= len(before.Hosts) ||
			before.Hosts[before.NextHost].ExceptionDisabledEvidence != "" || after.Hosts[before.NextHost].ExceptionDisabledEvidence == "" {
			return false
		}
	}
	if !appendOnlyBootstrapValue(before.AbandonReasonDigest, after.AbandonReasonDigest) ||
		!appendOnlyBootstrapValue(before.AbandonEvidenceDigest, after.AbandonEvidenceDigest) ||
		!appendOnlyBootstrapValue(before.CompletedManifestID, after.CompletedManifestID) ||
		!appendOnlyBootstrapValue(before.CompletedEvidenceDigest, after.CompletedEvidenceDigest) {
		return false
	}
	for i := range before.Hosts {
		old, current := before.Hosts[i], after.Hosts[i]
		if old.HostID != current.HostID || !appendOnlyBootstrapValue(old.L0EvidenceDigest, current.L0EvidenceDigest) ||
			!appendOnlyBootstrapValue(old.IssuanceMandateID, current.IssuanceMandateID) ||
			!appendOnlyBootstrapValue(old.CertificateRequestID, current.CertificateRequestID) ||
			!appendOnlyBootstrapValue(old.IssuerReceiptID, current.IssuerReceiptID) ||
			!appendOnlyBootstrapValue(old.CertificatePackageDigest, current.CertificatePackageDigest) ||
			!appendOnlyBootstrapValue(old.InstallerTupleDigest, current.InstallerTupleDigest) ||
			!appendOnlyBootstrapValue(old.InstallEvidenceDigest, current.InstallEvidenceDigest) ||
			!appendOnlyBootstrapValue(old.HealthEvidenceDigest, current.HealthEvidenceDigest) ||
			!appendOnlyBootstrapValue(old.ExceptionDisabledEvidence, current.ExceptionDisabledEvidence) ||
			!appendOnlyBootstrapValue(old.AbandonDisableEvidence, current.AbandonDisableEvidence) {
			return false
		}
		if old.L0EvidenceDigest != "" && (old.L0ObservedAt != current.L0ObservedAt || old.L0ExpiresAt != current.L0ExpiresAt) {
			return false
		}
	}
	return true
}

func appendOnlyBootstrapValue(before, after string) bool {
	return before == "" || before == after
}
