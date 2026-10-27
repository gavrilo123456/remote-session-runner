package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"remote-session-runner/src/internal/laneinstall"
)

func TestBUG027DetailedInventoryRejectsInvalidScopeBeforeAuthorityRead(t *testing.T) {
	for _, args := range [][]string{
		{"--host", "unlisted", "--lane", "prd", "--details"},
		{"--host", "mac-control-plane", "--lane", "other", "--details"},
		{"--host", "mac-control-plane", "--lane", "prd", "--details", "unexpected"},
	} {
		var stdout, stderr bytes.Buffer
		if status := run(append([]string{"catalog-work-inventory"}, args...), &stdout, &stderr); status == 0 {
			t.Fatalf("invalid inventory scope succeeded: %v", args)
		}
		if stdout.Len() != 0 {
			t.Fatalf("invalid scope emitted a usable inventory: %s", stdout.String())
		}
	}
}

func TestBUG027IncompleteInventoryEmitsNoPartialIdentityOrUnderlyingError(t *testing.T) {
	var stdout bytes.Buffer
	partial := laneinstall.CatalogRetainedWorkInventoryReport{HostID: "partial-authority-must-not-escape"}
	err := writeRetainedWorkInventory(&stdout, partial, errors.New("SQL failure with private-path-must-not-escape"))
	if err == nil {
		t.Fatal("incomplete read was turned into successful acceptance")
	}
	var report map[string]any
	if json.Unmarshal(stdout.Bytes(), &report) != nil || report["inventory_complete"] != false ||
		report["safe_to_resume"] != "not_established" || report["recovery_authorized"] != false {
		t.Fatalf("failure report lacks explicit safety/completeness fields: %s", stdout.String())
	}
	if strings.Contains(stdout.String(), "must-not-escape") || strings.Contains(err.Error(), "must-not-escape") {
		t.Fatal("partial identity or underlying error leaked from incomplete report")
	}
}

func TestBUG027InventoryWriterPropagatesOutputFailure(t *testing.T) {
	if err := writeRetainedWorkInventory(bug027FailingWriter{}, laneinstall.CatalogRetainedWorkInventoryReport{}, errors.New("fixture")); err == nil {
		t.Fatal("failed report output was accepted")
	}
}

func TestBUG027InventoryWriterRejectsIncompleteSuccess(t *testing.T) {
	var stdout bytes.Buffer
	if err := writeRetainedWorkInventory(&stdout, laneinstall.CatalogRetainedWorkInventoryReport{}, nil); err == nil {
		t.Fatal("incomplete nil-error result was published as success")
	}
	var report map[string]any
	if json.Unmarshal(stdout.Bytes(), &report) != nil || report["inventory_complete"] != false {
		t.Fatalf("incomplete nil-error result omitted failure marker: %s", stdout.String())
	}
}

type bug027FailingWriter struct{}

func (bug027FailingWriter) Write([]byte) (int, error) { return 0, errors.New("fixture writer closed") }
