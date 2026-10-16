package lanetests

import (
	"context"
	"encoding/json"
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
	var l101, l102, l103 *UnitCoverage
	for i := range manifest.Units {
		switch manifest.Units[i].ID {
		case "LANES-L1-01":
			l101 = &manifest.Units[i]
		case "LANES-L1-02":
			l102 = &manifest.Units[i]
		case "LANES-L1-03":
			l103 = &manifest.Units[i]
		}
	}
	if l101 == nil || len(l101.Assertions[HermeticTier]) != 5 {
		t.Fatalf("LANES-L1-01 coverage = %+v; want five registered H assertions", l101)
	}
	if l102 == nil || len(l102.Assertions[HermeticTier]) != 6 {
		t.Fatalf("LANES-L1-02 coverage = %+v; want six registered H assertions", l102)
	}
	if l103 == nil || len(l103.Assertions[HermeticTier]) != 6 || len(l103.Assertions[NativeTier]) != 6 ||
		!contains(l103.RequiredTiers, HermeticTier) || !contains(l103.RequiredTiers, NativeTier) {
		t.Fatalf("LANES-L1-03 coverage = %+v; want six H and six N assertions", l103)
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
		{unit: "LANES-L1-04", want: ErrMissingAssertions},
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

func TestLanesL103CoverageMatchesPlan(t *testing.T) {
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
	for _, unit := range manifest.Units {
		if unit.ID != "LANES-L1-03" {
			continue
		}
		if !contains(unit.RequiredTiers, HermeticTier) || !contains(unit.RequiredTiers, NativeTier) ||
			len(unit.Assertions[HermeticTier]) != 6 || len(unit.Assertions[NativeTier]) != 6 {
			t.Fatalf("L1-03 coverage mismatch: %+v", unit)
		}
		return
	}
	t.Fatal("L1-03 missing from validated manifest")
}

func TestLanesL103NativeDispatcherIsClosed(t *testing.T) {
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
	var native []TestAssertion
	for _, unit := range manifest.Units {
		if unit.ID == "LANES-L1-03" {
			native = unit.Assertions[NativeTier]
			break
		}
	}
	if len(native) == 0 {
		t.Fatal("L1-03 has no registered N assertions")
	}
	calls := 0
	fakeRunner := func(_ context.Context, _ string, args []string, _ string) ([]byte, error) {
		calls++
		if !contains(args, "./src/internal/laneroot") || !contains(args, "-json") || !contains(args, "-count=1") {
			return nil, fmt.Errorf("unexpected native test command: %v", args)
		}
		var stream strings.Builder
		for _, assertion := range native {
			for _, action := range []string{"run", "pass"} {
				event, err := json.Marshal(map[string]string{"Action": action, "Package": "laneroot", "Test": assertion.Name})
				if err != nil {
					return nil, err
				}
				stream.Write(event)
				stream.WriteByte('\n')
			}
		}
		return []byte(stream.String()), nil
	}
	var output strings.Builder
	if err := RunNative(context.Background(), root, manifestPath, handbookPath, "go", "LANES-L1-03", &output, fakeRunner); err != nil {
		t.Fatalf("RunNative rejected registered N tier: %v", err)
	}
	if calls != 1 || !strings.Contains(output.String(), "PASS LANES-L1-03 N (6 registered assertions)") {
		t.Fatalf("native dispatch calls=%d output=%q", calls, output.String())
	}
	for unit, want := range map[string]error{"LANES-L1-01": ErrWrongProofTier, "LANES-L1-04": ErrWrongProofTier} {
		if err := RunNative(context.Background(), root, manifestPath, handbookPath, "go", unit, nil, fakeRunner); !errors.Is(err, want) {
			t.Errorf("RunNative(%s) = %v, want %v", unit, err, want)
		}
	}
	if calls != 1 {
		t.Fatalf("invalid native dispatch invoked tests; calls=%d", calls)
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
