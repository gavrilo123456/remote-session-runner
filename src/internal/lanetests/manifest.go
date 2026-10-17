// Package lanetests loads the checked-in lane coverage contract and dispatches
// only explicitly registered hermetic assertions for one exact LANES unit.
package lanetests

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"remote-session-runner/src/internal/lanefixtures"
)

const (
	ManifestVersion = 1
	HermeticTier    = "H"
	NativeTier      = "N"
	ControlsTier    = "F"
)

var (
	ErrUnknownUnit       = errors.New("unknown lane unit")
	ErrMissingAssertions = errors.New("lane unit has no registered assertions for this proof tier")
	ErrWrongProofTier    = errors.New("lane unit does not declare the requested proof tier")
	ErrInvalidManifest   = errors.New("invalid lane test coverage manifest")
	unitIDPattern        = regexp.MustCompile(`^LANES-(?:PREP-00|L0|READY|B\d{3}-\d{2}|L[1-7]-\d{2})$`)
	assertionNamePattern = regexp.MustCompile(`^Test[A-Za-z0-9_]+$`)
	handbookUnitRow      = regexp.MustCompile(`^\|\s*` + "`" + `(LANES-[^` + "`" + `]+)` + "`" + `\s*\|`)
	validProofTiers      = map[string]struct{}{"R": {}, "H": {}, "N": {}, "F": {}, "B": {}, "D": {}}
	expectedDesignItems  = []string{"D9-01", "D9-02", "D9-03", "D9-04", "D9-05", "D9-06", "D9-07", "D9-08", "D9-09", "D9-10"}
)

type Manifest struct {
	Version          int              `json:"version"`
	Units            []UnitCoverage   `json:"units"`
	DesignAcceptance []DesignCoverage `json:"design_acceptance"`
}

type UnitCoverage struct {
	ID            string                     `json:"id"`
	RequiredTiers []string                   `json:"required_tiers"`
	Assertions    map[string][]TestAssertion `json:"assertions,omitempty"`
}

type TestAssertion struct {
	Package string `json:"package"`
	Name    string `json:"name"`
}

type DesignCoverage struct {
	ID    string   `json:"id"`
	Units []string `json:"units"`
}

type goTestEvent struct {
	Action  string `json:"Action"`
	Package string `json:"Package"`
	Test    string `json:"Test"`
}

type CommandRunner func(context.Context, string, []string, string) ([]byte, error)

// Load reads and validates the manifest against the handbook's exact unit
// table. A unit added to the handbook cannot silently fall outside dispatch.
func Load(path, handbookPath string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: read manifest: %v", ErrInvalidManifest, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("%w: decode manifest", ErrInvalidManifest)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Manifest{}, ErrInvalidManifest
	}
	if err := Validate(manifest, handbookPath); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// Validate checks closed unit/tier/assertion keys and complete traceability to
// all unit rows and all ten acceptance items in the current plan.
func Validate(manifest Manifest, handbookPath string) error {
	if manifest.Version != ManifestVersion || len(manifest.Units) == 0 || len(manifest.DesignAcceptance) != len(expectedDesignItems) {
		return ErrInvalidManifest
	}
	planIDs, err := readHandbookUnitIDs(handbookPath)
	if err != nil {
		return fmt.Errorf("%w: read handbook unit rows", ErrInvalidManifest)
	}
	units := make(map[string]UnitCoverage, len(manifest.Units))
	for _, unit := range manifest.Units {
		if !unitIDPattern.MatchString(unit.ID) || len(unit.RequiredTiers) == 0 {
			return ErrInvalidManifest
		}
		if _, exists := units[unit.ID]; exists {
			return ErrInvalidManifest
		}
		tiers := make(map[string]struct{}, len(unit.RequiredTiers))
		for _, tier := range unit.RequiredTiers {
			if _, ok := validProofTiers[tier]; !ok {
				return ErrInvalidManifest
			}
			if _, duplicate := tiers[tier]; duplicate {
				return ErrInvalidManifest
			}
			tiers[tier] = struct{}{}
		}
		for tier, assertions := range unit.Assertions {
			if _, ok := validProofTiers[tier]; !ok {
				return ErrInvalidManifest
			}
			if _, required := tiers[tier]; !required || len(assertions) == 0 {
				return ErrInvalidManifest
			}
			seen := make(map[string]struct{}, len(assertions))
			for _, assertion := range assertions {
				if !validAssertion(assertion) {
					return ErrInvalidManifest
				}
				key := assertion.Package + "\x00" + assertion.Name
				if _, duplicate := seen[key]; duplicate {
					return ErrInvalidManifest
				}
				seen[key] = struct{}{}
			}
		}
		units[unit.ID] = unit
	}
	if len(units) != len(planIDs) {
		return ErrInvalidManifest
	}
	for id := range planIDs {
		if _, ok := units[id]; !ok {
			return ErrInvalidManifest
		}
	}

	acceptance := make(map[string]struct{}, len(manifest.DesignAcceptance))
	for _, item := range manifest.DesignAcceptance {
		if _, duplicate := acceptance[item.ID]; duplicate || len(item.Units) == 0 {
			return ErrInvalidManifest
		}
		acceptance[item.ID] = struct{}{}
		seenUnits := make(map[string]struct{}, len(item.Units))
		for _, unitID := range item.Units {
			if _, ok := units[unitID]; !ok {
				return ErrInvalidManifest
			}
			if _, duplicate := seenUnits[unitID]; duplicate {
				return ErrInvalidManifest
			}
			seenUnits[unitID] = struct{}{}
		}
	}
	for _, id := range expectedDesignItems {
		if _, ok := acceptance[id]; !ok {
			return ErrInvalidManifest
		}
	}
	return nil
}

func validAssertion(assertion TestAssertion) bool {
	if !strings.HasPrefix(assertion.Package, "./src/") || filepath.IsAbs(assertion.Package) {
		return false
	}
	clean := filepath.Clean(assertion.Package)
	return clean == strings.TrimPrefix(assertion.Package, "./") && !strings.HasPrefix(clean, "../") &&
		assertionNamePattern.MatchString(assertion.Name)
}

func readHandbookUnitIDs(path string) (map[string]struct{}, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	ids := make(map[string]struct{})
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		match := handbookUnitRow.FindStringSubmatch(scanner.Text())
		if len(match) != 2 {
			continue
		}
		if _, duplicate := ids[match[1]]; duplicate {
			return nil, fmt.Errorf("duplicate handbook unit row %s", match[1])
		}
		ids[match[1]] = struct{}{}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

// RunHermetic dispatches the registered H assertions for one unit. It fails
// closed on unknown units, an unimplemented H tier, or tests that skip, fail,
// or never run.
func RunHermetic(ctx context.Context, root, manifestPath, handbookPath, goBinary, unitID string, output io.Writer, runner CommandRunner) error {
	return runTier(ctx, root, manifestPath, handbookPath, goBinary, unitID, HermeticTier, output, runner)
}

// RunNative dispatches the registered native, unprivileged assertions for one
// exact unit. N tests may use local OS descriptors and temporary fixtures, but
// they never touch installed services or external lane roots.
func RunNative(ctx context.Context, root, manifestPath, handbookPath, goBinary, unitID string, output io.Writer, runner CommandRunner) error {
	if runner == nil {
		runner = runNativeGo
	}
	return runTier(ctx, root, manifestPath, handbookPath, goBinary, unitID, NativeTier, output, runner)
}

// RunControls validates the separately approved, host-bound A01 fixture and
// its live mounted identities before dispatching registered F assertions. The
// fixture file must remain outside Git and its exact SHA must match the A01
// owner decision supplied to the command.
func RunControls(ctx context.Context, root, manifestPath, handbookPath, goBinary, unitID, hostID, fixturePath, approvedDigest string, output io.Writer, runner CommandRunner) error {
	fixture, err := lanefixtures.LoadApproved(fixturePath, approvedDigest, root, unitID, hostID, time.Now())
	if err != nil {
		return err
	}
	if err := lanefixtures.VerifyLive(fixture); err != nil {
		return fmt.Errorf("fixture live preflight: %w", err)
	}
	previous := make(map[string]string)
	for key, value := range map[string]string{
		"LANES_CONTROLS_DISPATCH":     "1",
		"LANES_FIXTURE_MANIFEST":      fixturePath,
		"LANES_FIXTURE_SHA256":        approvedDigest,
		"LANES_FIXTURE_UNIT":          unitID,
		"LANES_FIXTURE_HOST":          hostID,
		"LANES_FIXTURE_TEST_IDENTITY": fixture.TestIdentity,
		"LANES_FIXTURE_ROOT":          fixture.FixtureRoot,
	} {
		previous[key] = os.Getenv(key)
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("set F fixture context: %w", err)
		}
	}
	defer func() {
		for key, value := range previous {
			if value == "" {
				_ = os.Unsetenv(key)
			} else {
				_ = os.Setenv(key, value)
			}
		}
	}()
	return runTier(ctx, root, manifestPath, handbookPath, goBinary, unitID, ControlsTier, output, runner)
}

func runTier(ctx context.Context, root, manifestPath, handbookPath, goBinary, unitID, selectedTier string, output io.Writer, runner CommandRunner) error {
	if runner == nil {
		if selectedTier == ControlsTier {
			runner = runControlsGo
		} else {
			runner = runGo
		}
	}
	manifest, err := Load(manifestPath, handbookPath)
	if err != nil {
		return err
	}
	var selected *UnitCoverage
	for i := range manifest.Units {
		if manifest.Units[i].ID == unitID {
			selected = &manifest.Units[i]
			break
		}
	}
	if selected == nil {
		return fmt.Errorf("%w: %s", ErrUnknownUnit, unitID)
	}
	declaresTier := false
	for _, requiredTier := range selected.RequiredTiers {
		if requiredTier == selectedTier {
			declaresTier = true
			break
		}
	}
	if !declaresTier {
		return fmt.Errorf("%w: %s requires %v", ErrWrongProofTier, unitID, selected.RequiredTiers)
	}
	assertions := selected.Assertions[selectedTier]
	if len(assertions) == 0 {
		return fmt.Errorf("%w: %s %s", ErrMissingAssertions, unitID, selectedTier)
	}
	if goBinary == "" {
		return fmt.Errorf("%w: Go binary is not configured", ErrInvalidManifest)
	}

	groups := make(map[string][]TestAssertion)
	packages := make([]string, 0)
	for _, assertion := range assertions {
		if _, exists := groups[assertion.Package]; !exists {
			packages = append(packages, assertion.Package)
		}
		groups[assertion.Package] = append(groups[assertion.Package], assertion)
	}
	sort.Strings(packages)
	if output != nil {
		host, _ := os.Hostname()
		_, _ = fmt.Fprintf(output, "LANES unit=%s tier=%s host=%s os=%s go=%s assertions=%d\n", unitID, selectedTier, host, runtime.GOOS, runtime.Version(), len(assertions))
	}
	for _, packagePath := range packages {
		tests := groups[packagePath]
		names := make([]string, 0, len(tests))
		for _, test := range tests {
			names = append(names, test.Name)
		}
		sort.Strings(names)
		selector := "^(" + strings.Join(names, "|") + ")$"
		args := []string{"test", "-json", "-count=1", "-run", selector, packagePath}
		if selectedTier == ControlsTier {
			args = append(args, "-timeout=4h")
		}
		result, runErr := runner(ctx, goBinary, args, root)
		if output != nil && len(result) != 0 {
			_, _ = output.Write(result)
		}
		checkErr := CheckTestEvents(result, tests)
		if runErr != nil {
			return fmt.Errorf("lane unit %s tier %s package %s: go test failed: %w", unitID, selectedTier, packagePath, runErr)
		}
		if checkErr != nil {
			return fmt.Errorf("lane unit %s tier %s package %s: %w", unitID, selectedTier, packagePath, checkErr)
		}
	}
	if output != nil {
		if selectedTier == ControlsTier {
			_, _ = fmt.Fprintf(output, "PASS %s F fixture controls subtests (%d registered assertions); full unit physical acceptance and evidence closeout remain separate\n", unitID, len(assertions))
		} else {
			_, _ = fmt.Fprintf(output, "PASS %s %s (%d registered assertions)\n", unitID, selectedTier, len(assertions))
		}
	}
	return nil
}

func runGo(ctx context.Context, goBinary string, args []string, root string) ([]byte, error) {
	return runGoWithNativeContext(ctx, goBinary, args, root, false)
}

func runControlsGo(ctx context.Context, goBinary string, args []string, root string) ([]byte, error) {
	command := exec.CommandContext(ctx, goBinary, args...)
	command.Dir = root
	environment, err := controlsTestEnvironment()
	if err != nil {
		return nil, err
	}
	command.Env = environment
	return command.CombinedOutput()
}

func controlsTestEnvironment() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve isolated controls-test HOME: %w", err)
	}
	if home == "" || !filepath.IsAbs(home) {
		return nil, errors.New("resolve isolated controls-test HOME: empty or relative path")
	}
	fixtureRoot := os.Getenv("LANES_FIXTURE_ROOT")
	if fixtureRoot == "" || !filepath.IsAbs(fixtureRoot) || filepath.Clean(fixtureRoot) != fixtureRoot {
		return nil, errors.New("resolve isolated controls-test fixture root: empty, relative, or unclean path")
	}
	environment := []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=" + home,
		"GOTOOLCHAIN=local",
		"GOMAXPROCS=2",
	}
	goBuildPath := filepath.Join(fixtureRoot, "go-build")
	goModulesPath := filepath.Join(fixtureRoot, "go-modules")
	if runtime.GOOS == "darwin" {
		environment = append(environment,
			"TMPDIR="+goBuildPath,
			"GOCACHE="+goBuildPath,
			"GOMODCACHE="+goModulesPath)
	} else {
		goBuildPath = filepath.Join(fixtureRoot, "data", "go-build")
		goModulesPath = filepath.Join(fixtureRoot, "data", "go-modules")
		environment = append(environment,
			"TMPDIR="+goBuildPath,
			"GOCACHE="+goBuildPath,
			"GOMODCACHE="+goModulesPath)
	}
	for _, key := range []string{
		"LANES_CONTROLS_DISPATCH", "LANES_FIXTURE_MANIFEST", "LANES_FIXTURE_SHA256",
		"LANES_FIXTURE_UNIT", "LANES_FIXTURE_HOST", "LANES_FIXTURE_TEST_IDENTITY", "LANES_FIXTURE_ROOT",
	} {
		value := os.Getenv(key)
		if value == "" {
			return nil, fmt.Errorf("required controls-test context %s is missing", key)
		}
		environment = append(environment, key+"="+value)
	}
	return environment, nil
}

func runNativeGo(ctx context.Context, goBinary string, args []string, root string) ([]byte, error) {
	return runGoWithNativeContext(ctx, goBinary, args, root, true)
}

func runGoWithNativeContext(ctx context.Context, goBinary string, args []string, root string, native bool) ([]byte, error) {
	command := exec.CommandContext(ctx, goBinary, args...)
	command.Dir = root
	command.Env = setEnvironment(os.Environ(), "GOTOOLCHAIN", "local")
	if native {
		command.Env = setEnvironment(command.Env, "LANES_NATIVE_VALIDATION", "1")
	}
	return command.CombinedOutput()
}

func setEnvironment(environment []string, name, value string) []string {
	prefix := name + "="
	for i, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			environment[i] = prefix + value
			return environment
		}
	}
	return append(environment, prefix+value)
}

// CheckTestEvents verifies the go test JSON stream contains a run and pass for
// each registered top-level test. A skipped or absent test can never count as
// a coverage pass.
func CheckTestEvents(output []byte, assertions []TestAssertion) error {
	expected := make(map[string]struct{}, len(assertions))
	for _, assertion := range assertions {
		if !validAssertion(assertion) {
			return ErrInvalidManifest
		}
		expected[assertion.Name] = struct{}{}
	}
	run := make(map[string]bool, len(expected))
	passed := make(map[string]bool, len(expected))
	skipped := make(map[string]bool, len(expected))
	decoder := json.NewDecoder(bytes.NewReader(output))
	var eventCount int
	for {
		var event goTestEvent
		err := decoder.Decode(&event)
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("invalid go test JSON output: %w", err)
		}
		eventCount++
		if _, ok := expected[event.Test]; !ok || event.Test == "" {
			continue
		}
		switch event.Action {
		case "run":
			run[event.Test] = true
		case "skip":
			skipped[event.Test] = true
		case "pass":
			if run[event.Test] {
				passed[event.Test] = true
			}
		case "fail":
			return fmt.Errorf("assertion %s failed", event.Test)
		}
	}
	if eventCount == 0 {
		return errors.New("go test produced no JSON events")
	}
	for name := range expected {
		if skipped[name] {
			return fmt.Errorf("assertion %s was skipped", name)
		}
		if !run[name] || !passed[name] {
			return fmt.Errorf("assertion %s did not run and pass", name)
		}
	}
	return nil
}

// FindRepoRoot returns the closest parent containing go.mod.
func FindRepoRoot(start string) (string, error) {
	absolute, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for current := absolute; ; current = filepath.Dir(current) {
		if info, err := os.Stat(filepath.Join(current, "go.mod")); err == nil && !info.IsDir() {
			return current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", errors.New("could not find repository go.mod")
		}
	}
}
