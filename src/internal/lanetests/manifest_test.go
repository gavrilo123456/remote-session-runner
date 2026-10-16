package lanetests

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestLanesL101ManifestMatchesPlan(t *testing.T) {
	root, err := FindRepoRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(root, "040-implementation-evidence", "LANES-test-coverage.json")
	handbookPath := filepath.Join(root, "040-improvments", "040-dev-qas-prd-three-machine-lanes", "020-dev-qas-prd-three-machine-lanes-execution-handbook.md")
	manifest, err := Load(manifestPath, handbookPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Units) != 68 || len(manifest.DesignAcceptance) != 10 {
		t.Fatalf("manifest units=%d design acceptance items=%d, want 68 and 10", len(manifest.Units), len(manifest.DesignAcceptance))
	}
	var l101 *UnitCoverage
	for i := range manifest.Units {
		if manifest.Units[i].ID == "LANES-L1-01" {
			l101 = &manifest.Units[i]
			break
		}
	}
	if l101 == nil || len(l101.Assertions[HermeticTier]) != 5 {
		t.Fatalf("LANES-L1-01 coverage = %+v; want five registered H assertions", l101)
	}
}

func TestLanesL101RejectsUnknownMissingAndWrongProofTier(t *testing.T) {
	root, err := FindRepoRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(root, "040-implementation-evidence", "LANES-test-coverage.json")
	handbookPath := filepath.Join(root, "040-improvments", "040-dev-qas-prd-three-machine-lanes", "020-dev-qas-prd-three-machine-lanes-execution-handbook.md")
	runnerCalls := 0
	fakeRunner := func(context.Context, string, []string, string) ([]byte, error) {
		runnerCalls++
		return nil, nil
	}
	for _, test := range []struct {
		unit string
		want error
	}{
		{unit: "LANES-L9-99", want: ErrUnknownUnit},
		{unit: "LANES-L1-02", want: ErrMissingAssertions},
		{unit: "LANES-PREP-00", want: ErrWrongProofTier},
	} {
		err := RunHermetic(context.Background(), root, manifestPath, handbookPath, "go", test.unit, nil, fakeRunner)
		if !errors.Is(err, test.want) {
			t.Errorf("RunHermetic(%s) = %v, want %v", test.unit, err, test.want)
		}
	}
	if runnerCalls != 0 {
		t.Fatalf("invalid dispatch invoked go test %d times", runnerCalls)
	}
}

func TestLanesL101RejectsSkippedAndUnobservedAssertions(t *testing.T) {
	assertion := TestAssertion{Package: "./src/internal/config", Name: "TestLanesL101NamespaceDerivationAndV3Identity"}
	for _, test := range []struct {
		name string
		data string
	}{
		{name: "skip", data: `{"Action":"run","Package":"p","Test":"TestLanesL101NamespaceDerivationAndV3Identity"}` + "\n" + `{"Action":"skip","Package":"p","Test":"TestLanesL101NamespaceDerivationAndV3Identity"}`},
		{name: "not run", data: `{"Action":"pass","Package":"p"}`},
		{name: "malformed stream", data: "the test runner printed no JSON"},
	} {
		if err := CheckTestEvents([]byte(test.data), []TestAssertion{assertion}); err == nil {
			t.Errorf("CheckTestEvents accepted %s stream", test.name)
		}
	}

	root, err := FindRepoRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(root, "040-implementation-evidence", "LANES-test-coverage.json")
	handbookPath := filepath.Join(root, "040-improvments", "040-dev-qas-prd-three-machine-lanes", "020-dev-qas-prd-three-machine-lanes-execution-handbook.md")
	output := strings.Builder{}
	fakeRunner := func(_ context.Context, _ string, args []string, _ string) ([]byte, error) {
		if len(args) < 2 || args[0] != "test" || args[1] != "-json" || !contains(args, "-count=1") {
			return nil, fmt.Errorf("unexpected go test arguments: %v", args)
		}
		return []byte(`{"Action":"run","Package":"p","Test":"TestLanesL101NamespaceDerivationAndV3Identity"}` + "\n" +
			`{"Action":"pass","Package":"p","Test":"TestLanesL101NamespaceDerivationAndV3Identity"}`), nil
	}
	err = RunHermetic(context.Background(), root, manifestPath, handbookPath, "go", "LANES-L1-01", &output, fakeRunner)
	if err == nil || !strings.Contains(err.Error(), "did not run and pass") {
		t.Fatalf("dispatcher accepted an incomplete selected test set: %v", err)
	}
}

func contains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}
