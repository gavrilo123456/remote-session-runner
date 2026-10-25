package laneinstall

import (
	"reflect"
	"strings"
	"testing"

	"remote-session-runner/src/internal/config"
)

func TestCatalogPRDRecoveryCommandUsesOnlySealedHostComponentAndExactPairs(t *testing.T) {
	pairs := []string{
		"sess-0123456789abcdef0123456789abcdef:cmd-0123456789abcdef0123456789abcdef",
		"sess-fedcba9876543210fedcba9876543210:cmd-fedcba9876543210fedcba9876543210",
	}
	want := []string{"recover-stalled", "--config", "/private/service/config/prd.yaml", "--apply",
		"--lost-pair", pairs[0], "--lost-pair", pairs[1]}
	for _, host := range []string{"linux-host", "sandbox-host", "factory-sandbox", "mac-control-plane"} {
		args, err := buildCatalogPRDRecoveryCommand(host, "/private/service/config/prd.yaml", pairs)
		if err != nil || !reflect.DeepEqual(args, want) {
			t.Fatalf("recovery args for %s=%v err=%v, want %v", host, args, err, want)
		}
		component, ok := CatalogPRDRecoveryCommandComponent(host)
		wantComponent := "runnerd"
		if host == "mac-control-plane" {
			wantComponent = "runner-locald"
		}
		if !ok || component != wantComponent {
			t.Fatalf("recovery component for %s=(%q,%v), want %q", host, component, ok, wantComponent)
		}
	}
	if _, err := buildCatalogPRDRecoveryCommand("unknown-host", "/private/service/config/prd.yaml", pairs); err == nil {
		t.Fatal("recovery accepted an unlisted host")
	}
	if _, err := buildCatalogPRDRecoveryCommand("linux-host", "relative/config.yaml", pairs); err == nil {
		t.Fatal("recovery accepted a relative configuration path")
	}
}

func TestCatalogPRDRecoveryRejectsIncompleteMalformedAndDuplicatePairs(t *testing.T) {
	valid := "sess-0123456789abcdef0123456789abcdef:cmd-0123456789abcdef0123456789abcdef"
	for name, pairs := range map[string][]string{
		"empty":            nil,
		"missing command":  {"sess-0123456789abcdef0123456789abcdef:"},
		"extra separator":  {valid + ":extra"},
		"invalid identity": {"sess-invalid/identity:cmd-0123456789abcdef0123456789abcdef"},
		"duplicate session": {
			valid,
			"sess-0123456789abcdef0123456789abcdef:cmd-fedcba9876543210fedcba9876543210",
		},
		"duplicate command": {
			valid,
			"sess-fedcba9876543210fedcba9876543210:cmd-0123456789abcdef0123456789abcdef",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := validatePRDRecoveryLostPairs(pairs); err == nil {
				t.Fatal("unsafe lost-pair list was accepted")
			}
		})
	}
}

func TestCatalogPRDRecoveryRequiresCurrentSchemaToAvoidMigration(t *testing.T) {
	if !catalogPRDRecoverySchemaIsCurrent(40, 37, 40, 40) {
		t.Fatal("current schema accepted by the sealed candidate was rejected")
	}
	for _, schema := range []int{0, 36, 37, 39, 41} {
		if catalogPRDRecoverySchemaIsCurrent(schema, 37, 40, 40) {
			t.Fatalf("schema %d was accepted for offline recovery; recovery must not migrate the database", schema)
		}
	}
	for _, inputs := range [][4]int{
		{40, 0, 40, 40},
		{40, 41, 40, 40},
		{40, 37, 39, 40},
		{40, 37, 40, 39},
	} {
		if catalogPRDRecoverySchemaIsCurrent(inputs[0], inputs[1], inputs[2], inputs[3]) {
			t.Fatalf("inconsistent schema bounds were accepted: %v", inputs)
		}
	}
}

func TestCatalogPRDRecoveryRequiresExactSelectedCatalogPredecessor(t *testing.T) {
	request := CatalogPRDRecoveryRequest{
		ExpectedSelectedReleaseID:     "cat-20261023-c01e633",
		ExpectedSelectedReleaseSHA256: strings.Repeat("a", 64),
		ExpectedSelectedBundleSHA256:  strings.Repeat("b", 64),
	}
	if err := ValidateCatalogPRDRecoverySelectedRelease(request); err != nil {
		t.Fatalf("complete selected-release identity rejected: %v", err)
	}
	selector := CatalogSelectorStatus{
		HostID: "mac-control-plane", Lane: config.ReleaseLanePRD,
		Kind: "catalog", Target: "/Users/tomasz.walczuk/projects-runtime/runner-releases/cat-20261023-c01e633/mac-control-plane",
		Release: &CatalogReleaseStatus{Identity: CatalogReleaseIdentity{
			ReleaseID: request.ExpectedSelectedReleaseID, ReleaseSHA256: request.ExpectedSelectedReleaseSHA256,
			HostID: "mac-control-plane", BundleSHA256: request.ExpectedSelectedBundleSHA256,
		}},
	}
	root := "/Users/tomasz.walczuk/projects-runtime/runner-releases"
	if !catalogPRDRecoverySelectorMatches(selector, "mac-control-plane", root, request) {
		t.Fatal("exact selected catalog predecessor was rejected")
	}
	changed := selector
	changed.Target += ".other"
	if catalogPRDRecoverySelectorMatches(changed, "mac-control-plane", root, request) {
		t.Fatal("selector target drift was accepted")
	}
	changed = selector
	changed.Release = &CatalogReleaseStatus{Identity: selector.Release.Identity}
	changed.Release.Identity.BundleSHA256 = strings.Repeat("c", 64)
	if catalogPRDRecoverySelectorMatches(changed, "mac-control-plane", root, request) {
		t.Fatal("predecessor bundle drift was accepted")
	}
	for _, partial := range []CatalogPRDRecoveryRequest{
		{ExpectedSelectedReleaseID: request.ExpectedSelectedReleaseID},
		{ExpectedSelectedReleaseID: request.ExpectedSelectedReleaseID, ExpectedSelectedReleaseSHA256: request.ExpectedSelectedReleaseSHA256},
		{ExpectedSelectedReleaseSHA256: request.ExpectedSelectedReleaseSHA256, ExpectedSelectedBundleSHA256: request.ExpectedSelectedBundleSHA256},
	} {
		if err := ValidateCatalogPRDRecoverySelectedRelease(partial); err == nil {
			t.Fatalf("partial selected-release identity was accepted: %+v", partial)
		}
	}
	unselected := CatalogSelectorStatus{HostID: "mac-control-plane", Lane: config.ReleaseLanePRD, Kind: "unselected"}
	legacy := CatalogPRDRecoveryRequest{}
	if !catalogPRDRecoverySelectorMatches(unselected, "mac-control-plane", root, legacy) {
		t.Fatal("existing unselected legacy recovery path regressed")
	}
}

func TestParsePRDRecoverySummaryRequiresTheExactSuccessfulPairCount(t *testing.T) {
	for _, output := range [][]byte{
		[]byte("runnerd recover-stalled: settled_jobs=0 recovered_lost_pairs=2 already_recovered_lost_pairs=0 recovered_lost_sessions=0\n"),
		[]byte("runner-locald recover-stalled: recovered_lost_pairs=2 already_recovered_lost_pairs=0 recovered_lost_sessions=0\n"),
	} {
		recovered, err := parsePRDRecoverySummary(output, 2)
		if err != nil || recovered != 2 {
			t.Fatalf("recovery summary=(%d) err=%v", recovered, err)
		}
	}
	for _, output := range [][]byte{
		[]byte("recovered_lost_pairs=2 already_recovered_lost_pairs=1"),
		[]byte("recovered_lost_pairs=two already_recovered_lost_pairs=0"),
		[]byte("runnerd exited without a recovery summary"),
	} {
		if _, err := parsePRDRecoverySummary(output, 2); err == nil {
			t.Fatalf("invalid recovery summary was accepted: %q", output)
		}
	}
	if _, err := parsePRDRecoverySummary([]byte("recovered_lost_pairs=1 already_recovered_lost_pairs=0"), 2); err == nil {
		t.Fatal("a partial recovery count was accepted")
	}
}
