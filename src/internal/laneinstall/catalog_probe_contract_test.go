package laneinstall

import (
	"errors"
	"fmt"
	"testing"

	"remote-session-runner/src/internal/catalogprobe"
)

func TestCatalogProbeSharedContractPreservesWireAndErrorIdentity(t *testing.T) {
	if CatalogMaintenanceProbeTokenHeader != catalogprobe.TokenHeader || CatalogMaintenanceProbeDigestHeader != catalogprobe.RequestDigestHeader {
		t.Fatal("catalog probe headers differ from the shared contract")
	}
	if CatalogMaintenanceProbeTokenHeader != "X-Runner-Catalog-Maintenance-Token" || CatalogMaintenanceProbeDigestHeader != "X-Runner-Catalog-Maintenance-Request-SHA256" {
		t.Fatal("catalog probe wire headers changed")
	}
	if ErrCatalogProbeAdmission != catalogprobe.ErrAdmission ||
		!errors.Is(fmt.Errorf("guard refusal: %w", ErrCatalogProbeAdmission), catalogprobe.ErrAdmission) {
		t.Fatal("catalog probe admission error identity changed")
	}
}
