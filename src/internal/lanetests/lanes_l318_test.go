package lanetests

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLanesL318PriorL3HermeticCoverageIsComplete(t *testing.T) {
	root, err := FindRepoRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(root, "040-implementation-evidence", "LANES-test-coverage.json")
	handbookPath := filepath.Join(root, "040-improvments", "040-dev-qas-prd-three-machine-lanes", "020-dev-qas-prd-three-machine-lanes-execution-handbook.md")
	manifest, err := Load(manifestPath, handbookPath)
	if err != nil {
		t.Fatalf("load closed coverage contract: %v", err)
	}

	byID := make(map[string]UnitCoverage, len(manifest.Units))
	for _, unit := range manifest.Units {
		byID[unit.ID] = unit
	}
	for number := 1; number <= 17; number++ {
		id := fmt.Sprintf("LANES-L3-%02d", number)
		unit, exists := byID[id]
		if !exists || !contains(unit.RequiredTiers, HermeticTier) || len(unit.Assertions[HermeticTier]) == 0 {
			t.Fatalf("prior L3 unit %s lacks its required registered H assertions: %+v", id, unit)
		}
	}

	rehearsal, exists := byID["LANES-L3-18"]
	if !exists || !contains(rehearsal.RequiredTiers, HermeticTier) || !contains(rehearsal.RequiredTiers, "F") ||
		len(rehearsal.Assertions[HermeticTier]) != 2 {
		t.Fatalf("L3-18 must retain H+F and its two rehearsal assertions: %+v", rehearsal)
	}
}

func TestLanesL318ApprovalCheckpointsPreserveA01A06Order(t *testing.T) {
	root, err := FindRepoRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	handbookPath := filepath.Join(root, "040-improvments", "040-dev-qas-prd-three-machine-lanes", "020-dev-qas-prd-three-machine-lanes-execution-handbook.md")
	data, err := os.ReadFile(handbookPath)
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	inTable := false
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "## 3. Declared approval checkpoints" {
			inTable = true
			continue
		}
		if inTable && strings.HasPrefix(line, "## ") {
			break
		}
		if !inTable || !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(line, "|")
		if len(cells) < 4 {
			continue
		}
		id := strings.TrimSpace(cells[1])
		if len(id) < 3 || id[0] != 'A' || id[1] < '0' || id[1] > '9' || id[2] < '0' || id[2] > '9' {
			continue
		}
		got = append(got, id[:3])
	}

	want := []string{"A01", "A02", "A03", "A04", "A05", "A06"}
	if len(got) < len(want) {
		t.Fatalf("approval checkpoint table has %v; want leading order %v", got, want)
	}
	for index, id := range want {
		if got[index] != id {
			t.Fatalf("approval checkpoint order is %v; want leading order %v", got, want)
		}
	}
}
