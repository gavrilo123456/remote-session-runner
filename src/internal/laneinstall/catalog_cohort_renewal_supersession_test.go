package laneinstall

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/config"
)

func supersessionRenewalFixture(t *testing.T, successor CatalogCohortReceipt, now time.Time) CatalogCohortContinuationRenewal {
	t.Helper()
	var observations []CatalogCohortContinuationHostObservation
	for _, host := range successor.Hosts {
		kind := "catalog"
		if host.PreviousTarget == "" {
			kind = "unselected"
		} else if strings.HasPrefix(host.PreviousTarget, "legacy-bin:") {
			kind = "legacy"
		}
		required, err := catalogCohortContinuationRequiredBytesForLane(successor.Lane, host.RequiredBytes)
		if err != nil {
			t.Fatal(err)
		}
		observations = append(observations, CatalogCohortContinuationHostObservation{
			FormatVersion: catalogCohortContinuationFormatVersion, OperationID: successor.OperationID, HostID: host.HostID,
			Lane: successor.Lane, ReleaseID: successor.ReleaseID, ReleaseSHA256: successor.ReleaseSHA256,
			BaseTransitionCount: len(successor.Transitions), BaseTransitionSHA256: lastCohortTransitionDigest(successor.Transitions),
			SelectorKind: kind, SelectorTarget: host.PreviousTarget, SelectorBundleSHA256: host.PreviousBundleSHA256,
			ConfigurationSHA256: host.ConfigurationSHA256, DatabaseSchema: host.SchemaVersion,
			WorkInventorySHA256: host.WorkInventorySHA256, WorkInventoryEmpty: true,
			BackupDirectory: host.BackupDirectory, BackupManifestSHA256: host.BackupManifestSHA256,
			AvailableBytes: 2 << 30, RequiredBytes: required, ServiceStopped: true,
			AdmissionHeld: true, AdmissionOperationID: successor.OperationID, ObservedAt: now.Add(-time.Second).Format(time.RFC3339Nano),
		})
	}
	renewal, err := NewCatalogCohortContinuationRenewal(successor, observations, now)
	if err != nil {
		t.Fatalf("renew held successor with exact four-host observations: %v", err)
	}
	return renewal
}

func TestSupersessionContinuationPreservesExactRolledBackHistory(t *testing.T) {
	for _, freshLinux := range []bool{false, true} {
		name := "catalog predecessor"
		if freshLinux {
			name = "fresh unselected Linux predecessor"
		}
		t.Run(name, func(t *testing.T) {
			started := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
			predecessor, prepared := rollbackHoldReceiptFixture(t, started, freshLinux)
			successor := advanceRollbackHoldFixture(t, prepared, started)
			now := started.Add(2 * time.Hour)
			renewal := supersessionRenewalFixture(t, successor, now)
			history := cloneRollbackHoldReceipt(t, predecessor).Transitions
			successorBefore, _ := json.Marshal(successor)
			firstObservation := fixtureCohortSupersessionEvidence(predecessor, successor, 0, "rolled_back")
			if _, err := AdvanceCatalogCohortSupersedeHost(predecessor, successor, firstObservation, now); err == nil {
				t.Fatal("ordinary pure supersession accepted expired evidence without explicit continuation")
			}
			for index, host := range catalogCohortSwitchOrder {
				disposition := "none"
				if cohortHasSwitched(predecessor, host) {
					disposition = "rolled_back"
				}
				observation := fixtureCohortSupersessionEvidence(predecessor, successor, index, disposition)
				updated, err := advanceCatalogCohortSupersedeHost(predecessor, successor, observation, &renewal, now)
				if err != nil {
					t.Fatalf("renewed exact %s supersession failed: %v", host, err)
				}
				// Model interruption after a durable per-host write and reload. An
				// exact repeat may not append a duplicate; resume advances next host.
				raw, _ := json.Marshal(updated)
				if json.Unmarshal(raw, &predecessor) != nil || ValidateCatalogCohortReceipt(predecessor) != nil {
					t.Fatal("renewed supersession history could not be reloaded")
				}
				if _, err := advanceCatalogCohortSupersedeHost(predecessor, successor, observation, &renewal, now); err == nil {
					t.Fatal("repeated per-host supersession appended duplicate evidence")
				}
			}
			closed, err := AdvanceCatalogCohortSupersede(predecessor, successor.OperationID, now)
			if err != nil || closed.Stage != CatalogCohortSuperseded || !reflect.DeepEqual(history, closed.Transitions[:len(history)]) {
				t.Fatalf("continuation rewrote rollback history or failed to close predecessor: %v", err)
			}
			successorAfter, _ := json.Marshal(successor)
			if string(successorBefore) != string(successorAfter) || successor.Stage != CatalogCohortHeld {
				t.Fatal("continuation modified successor receipt or opened admissions")
			}
		})
	}
}

func TestSupersessionContinuationRejectsUnboundOrExpiredEvidence(t *testing.T) {
	started := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	predecessor, prepared := rollbackHoldReceiptFixture(t, started, true)
	successor := advanceRollbackHoldFixture(t, prepared, started)
	now := started.Add(2 * time.Hour)
	renewal := supersessionRenewalFixture(t, successor, now)
	observation := fixtureCohortSupersessionEvidence(predecessor, successor, 0, "rolled_back")
	for _, fault := range []string{"missing", "tampered", "wrong operation", "wrong history", "expired", "local configuration drift", "local work", "local service active"} {
		t.Run(fault, func(t *testing.T) {
			candidate := renewal
			proof := &candidate
			at := now
			observed := observation
			switch fault {
			case "missing":
				proof = nil
			case "tampered":
				candidate.Hosts = append([]CatalogCohortContinuationHostObservation(nil), candidate.Hosts...)
				candidate.Hosts[1].ConfigurationSHA256 = strings.Repeat("f", 64)
			case "wrong operation":
				candidate.OperationID = strings.Repeat("f", 32)
				candidate.SHA256 = catalogCohortContinuationDigest(candidate)
			case "wrong history":
				candidate.BaseTransitionSHA256 = strings.Repeat("f", 64)
				candidate.SHA256 = catalogCohortContinuationDigest(candidate)
			case "expired":
				at = now.Add(CatalogCohortContinuationWindow + time.Nanosecond)
			case "local configuration drift":
				observed.ConfigurationSHA256 = strings.Repeat("f", 64)
			case "local work":
				observed.WorkInventoryEmpty = false
			case "local service active":
				observed.ServiceActive = true
			}
			if _, err := advanceCatalogCohortSupersedeHost(predecessor, successor, observed, proof, at); err == nil {
				t.Fatalf("accepted %s", fault)
			}
		})
	}
}

func TestSupersessionWindowRequiresVerifiedStoredRenewalAndExactCurrentReceipt(t *testing.T) {
	started := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	_, prepared := rollbackHoldReceiptFixture(t, started, true)
	successor := advanceRollbackHoldFixture(t, prepared, started)
	now := started.Add(2 * time.Hour)
	renewal := supersessionRenewalFixture(t, successor, now)
	plan, err := BuildCatalogPlan("linux-host", config.ReleaseLanePRD)
	if err != nil {
		t.Fatal(err)
	}
	for _, fault := range []string{"", "missing sidecar", "local state rejected", "receipt changed"} {
		verified := false
		verify := func(_ context.Context, got Plan, operationID string) (CatalogCohortContinuationRenewal, error) {
			verified = true
			if got.HostID != plan.HostID || operationID != successor.OperationID {
				t.Fatal("renewal verification targeted another authority")
			}
			if fault == "missing sidecar" || fault == "local state rejected" {
				return CatalogCohortContinuationRenewal{}, errors.New(fault)
			}
			return renewal, nil
		}
		read := func(_ Plan, _ string) (CatalogCohortReceipt, error) {
			current := successor
			if fault == "receipt changed" {
				current.SourceRevision = strings.Repeat("f", 40)
			}
			return current, nil
		}
		got, err := verifyCatalogCohortSupersessionWindowWithReaders(context.Background(), plan, successor, now, verify, read)
		if !verified || (fault == "" && (err != nil || got == nil)) || (fault != "" && err == nil) {
			t.Fatalf("fault=%q verified=%v renewal=%v err=%v", fault, verified, got != nil, err)
		}
	}
	_, freshPrepared := rollbackHoldReceiptFixture(t, now, true)
	freshSuccessor := advanceRollbackHoldFixture(t, freshPrepared, now)
	verifyUnexpected := func(context.Context, Plan, string) (CatalogCohortContinuationRenewal, error) {
		t.Fatal("original fresh evidence unnecessarily required a renewal")
		return CatalogCohortContinuationRenewal{}, nil
	}
	readUnexpected := func(Plan, string) (CatalogCohortReceipt, error) {
		t.Fatal("original fresh evidence unexpectedly entered continuation reading")
		return CatalogCohortReceipt{}, nil
	}
	if got, err := verifyCatalogCohortSupersessionWindowWithReaders(context.Background(), plan, freshSuccessor, now, verifyUnexpected, readUnexpected); err != nil || got != nil {
		t.Fatalf("original fresh evidence did not retain its existing path: renewal=%v err=%v", got != nil, err)
	}
}

func TestContinuationEmptyBundleRequiresExactFreshLinuxBaseline(t *testing.T) {
	started := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	_, prepared := rollbackHoldReceiptFixture(t, started, true)
	successor := advanceRollbackHoldFixture(t, prepared, started)
	renewal := supersessionRenewalFixture(t, successor, started.Add(2*time.Hour))
	if renewal.Hosts[0].SelectorKind != "unselected" || renewal.Hosts[0].SelectorBundleSHA256 != "" {
		t.Fatal("fixture lost the explicitly unselected fresh Linux baseline")
	}
	for index := 1; index < len(renewal.Hosts); index++ {
		observation := renewal.Hosts[index]
		observation.SelectorKind, observation.SelectorTarget, observation.SelectorBundleSHA256 = "unselected", "", ""
		if validateCatalogCohortContinuationHostObservation(observation, successor) == nil {
			t.Fatalf("non-fresh host %s accepted an empty predecessor identity", observation.HostID)
		}
	}
}
