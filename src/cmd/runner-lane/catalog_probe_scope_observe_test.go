package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/laneinstall"
)

func TestObserveCatalogProbeScopeReturnsOnlyExactAcceptedBinding(t *testing.T) {
	const (
		opID     = "63471d41af07b7afaf577d6a0989cc03"
		jobID    = "job-705974bc8f474d77898670837be15b61"
		configSH = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		bodySH   = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
		tokenSH  = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	)
	staging := t.TempDir()
	root := filepath.Join(staging, "catalog-probes")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	plan := laneinstall.Plan{HostID: "linux-host", Lane: config.ReleaseLanePRD,
		Namespace: config.LaneNamespace{Staging: staging}}
	created := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	expires := time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano)
	scope := catalogProbeScopeRecord{
		FormatVersion: 3, OperationID: opID, HostID: "linux-host", Lane: "prd", Transport: laneinstall.CatalogProbeDirectMTLS,
		ControllerType: domain.ControllerTypeDirectMTLS, ControllerID: "tomasz.walczuk", Environment: "linux-dev",
		ConfigurationSHA: configSH, CreatedAt: created, ExpiresAt: expires, TokenSHA256: tokenSH,
		IdempotencyKey: "catalog-probe-" + opID + "-linux-host", RequestBodySHA256: bodySH, State: "accepted", JobID: jobID,
	}
	path := filepath.Join(root, opID+".json")
	writeScope := func(value catalogProbeScopeRecord) {
		t.Helper()
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeScope(scope)
	got, err := observeCatalogProbeScope(plan, opID, laneinstall.CatalogProbeDirectMTLS, domain.ControllerTypeDirectMTLS,
		"tomasz.walczuk", "linux-dev", configSH, scope.IdempotencyKey, bodySH)
	if err != nil {
		t.Fatalf("observe exact accepted scope: %v", err)
	}
	if got.State != "accepted" || got.JobID != jobID || got.OperationID != opID || got.ConfigurationSHA256 != configSH || got.RequestSHA256 != bodySH {
		t.Fatalf("observation did not preserve exact accepted binding: %+v", got)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) == "" || strings.Contains(string(encoded), tokenSH) {
		t.Fatal("scope observation exposed a token digest")
	}

	if _, err := observeCatalogProbeScope(plan, opID, laneinstall.CatalogProbeDirectMTLS, domain.ControllerTypeDirectMTLS,
		"tomasz.walczuk", "linux-dev", "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", scope.IdempotencyKey, bodySH); err == nil {
		t.Fatal("scope with configuration drift was accepted")
	}

	retryScope := scope
	retryScope.IdempotencyKey += "-retry-1"
	writeScope(retryScope)
	got, err = observeCatalogProbeScope(plan, opID, laneinstall.CatalogProbeDirectMTLS, domain.ControllerTypeDirectMTLS,
		"tomasz.walczuk", "linux-dev", configSH, scope.IdempotencyKey, bodySH)
	if err != nil || got.IdempotencyKey != retryScope.IdempotencyKey {
		t.Fatalf("one-time exact retry scope observation=%+v err=%v", got, err)
	}
	otherOperation := "64471d41af07b7afaf577d6a0989cc03"
	otherScope := retryScope
	otherScope.OperationID = otherOperation
	otherScope.IdempotencyKey = "catalog-probe-" + otherOperation + "-linux-host-retry-1"
	otherPath := filepath.Join(root, otherOperation+".json")
	otherData, err := json.MarshalIndent(otherScope, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(otherPath, append(otherData, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := observeCatalogProbeScope(plan, otherOperation, laneinstall.CatalogProbeDirectMTLS, domain.ControllerTypeDirectMTLS,
		"tomasz.walczuk", "linux-dev", configSH, "catalog-probe-"+otherOperation+"-linux-host", bodySH); err == nil {
		t.Fatal("one-time retry suffix was accepted for another operation")
	}
	mutated := scope
	mutated.JobID = "job-not-a-valid-identity"
	writeScope(mutated)
	if _, err := observeCatalogProbeScope(plan, opID, laneinstall.CatalogProbeDirectMTLS, domain.ControllerTypeDirectMTLS,
		"tomasz.walczuk", "linux-dev", configSH, scope.IdempotencyKey, bodySH); err == nil {
		t.Fatal("scope with changed job identity was accepted")
	}
}

func TestObserveCatalogProbeScopeReportsAbsentWithoutCreatingState(t *testing.T) {
	staging := t.TempDir()
	plan := laneinstall.Plan{HostID: "linux-host", Lane: config.ReleaseLaneDev,
		Namespace: config.LaneNamespace{Staging: staging}}
	got, err := observeCatalogProbeScope(plan, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", laneinstall.CatalogProbeDirectMTLS,
		domain.ControllerTypeDirectMTLS, "tomasz.walczuk", "linux-dev",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"catalog-probe-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-linux-host",
		"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")
	if err != nil || got.State != "absent" {
		t.Fatalf("absent scope observation=%+v err=%v", got, err)
	}
	if _, err := os.Lstat(filepath.Join(staging, "catalog-probes")); !os.IsNotExist(err) {
		t.Fatalf("read-only absence check created scope root: err=%v", err)
	}
}
