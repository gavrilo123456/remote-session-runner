package laneinstall

import (
	"context"
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

func TestCatalogPRDCompletedTeardownSelectionIsMacOnlyAndExclusive(t *testing.T) {
	pair := "sess-0123456789abcdef0123456789abcdef:cmd-0123456789abcdef0123456789abcdef"
	valid := catalogPRDCompletedTeardownFixtureRequest(pair)
	if err := ValidateCatalogPRDRecoverySelection("mac-control-plane", valid); err != nil {
		t.Fatalf("exact Mac completed-teardown selection refused: %v", err)
	}
	for _, host := range []string{"linux-host", "sandbox-host", "factory-sandbox", "unknown-host"} {
		if err := ValidateCatalogPRDRecoverySelection(host, valid); err == nil {
			t.Fatalf("completed teardown accepted on unsupported host %s", host)
		}
	}
	for name, request := range map[string]CatalogPRDRecoveryRequest{
		"empty":            {},
		"mixed selections": {LostPairs: []string{pair}, TeardownPairs: []string{pair}},
		"duplicate":        {TeardownPairs: []string{pair, pair}},
		"malformed":        {TeardownPairs: []string{"sess-invalid/identity:cmd-0123456789abcdef0123456789abcdef"}},
		"missing command":  {TeardownPairs: []string{"sess-0123456789abcdef0123456789abcdef:"}},
	} {
		t.Run(name, func(t *testing.T) {
			request.ReleaseID, request.ExpectedReleaseSHA256, request.ExpectedBundleSHA256 = valid.ReleaseID, valid.ExpectedReleaseSHA256, valid.ExpectedBundleSHA256
			request.ExpectedSelectedReleaseID, request.ExpectedSelectedReleaseSHA256, request.ExpectedSelectedBundleSHA256 = valid.ExpectedSelectedReleaseID, valid.ExpectedSelectedReleaseSHA256, valid.ExpectedSelectedBundleSHA256
			if err := ValidateCatalogPRDRecoverySelection("mac-control-plane", request); err == nil {
				t.Fatal("unsafe recovery selection accepted")
			}
		})
	}
	for _, host := range []string{"mac-control-plane", "linux-host", "sandbox-host", "factory-sandbox"} {
		if err := ValidateCatalogPRDRecoverySelection(host, CatalogPRDRecoveryRequest{LostPairs: []string{pair}}); err != nil {
			t.Fatalf("existing lost-command selection regressed for %s: %v", host, err)
		}
	}
}

func TestCatalogPRDCompletedTeardownInvokesOnlyExactBuiltinSelection(t *testing.T) {
	pair := "sess-0123456789abcdef0123456789abcdef:cmd-0123456789abcdef0123456789abcdef"
	request := catalogPRDCompletedTeardownFixtureRequest(pair)
	want := []string{"recover-stalled", "--config", "/private/service/config/prd.yaml", "--apply", "--teardown-pair", pair}
	args, err := buildCatalogPRDRecoverySelectionCommand("mac-control-plane", "/private/service/config/prd.yaml", request)
	if err != nil || !reflect.DeepEqual(args, want) {
		t.Fatalf("completed-teardown invocation=%v error=%v, want %v", args, err, want)
	}
	for _, host := range []string{"linux-host", "sandbox-host", "factory-sandbox", "unknown-host"} {
		if _, err := buildCatalogPRDRecoverySelectionCommand(host, "/private/service/config/prd.yaml", request); err == nil {
			t.Fatalf("built completed-teardown invocation for %s", host)
		}
	}
	if _, err := buildCatalogPRDRecoverySelectionCommand("mac-control-plane", "relative/prd.yaml", request); err == nil {
		t.Fatal("relative configuration path accepted")
	}
}

func TestCatalogPRDCompletedTeardownRequiresItsAlreadySelectedRelease(t *testing.T) {
	baseline := catalogPRDCompletedTeardownFixtureRequest("sess-0123456789abcdef0123456789abcdef:cmd-0123456789abcdef0123456789abcdef")
	for name, mutate := range map[string]func(*CatalogPRDRecoveryRequest){
		"unselected legacy": func(r *CatalogPRDRecoveryRequest) {
			r.ExpectedSelectedReleaseID, r.ExpectedSelectedReleaseSHA256, r.ExpectedSelectedBundleSHA256 = "", "", ""
		},
		"another candidate":      func(r *CatalogPRDRecoveryRequest) { r.ReleaseID = "cat-other" },
		"another release digest": func(r *CatalogPRDRecoveryRequest) { r.ExpectedReleaseSHA256 = strings.Repeat("c", 64) },
		"another bundle":         func(r *CatalogPRDRecoveryRequest) { r.ExpectedBundleSHA256 = strings.Repeat("d", 64) },
		"malformed selected digest": func(r *CatalogPRDRecoveryRequest) {
			r.ExpectedSelectedReleaseSHA256, r.ExpectedReleaseSHA256 = "invalid", "invalid"
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := baseline
			mutate(&changed)
			if err := ValidateCatalogPRDRecoverySelection("mac-control-plane", changed); err == nil {
				t.Fatal("completed teardown accepted without its exact already-selected sealed executable")
			}
		})
	}
}

func catalogPRDCompletedTeardownFixtureRequest(pair string) CatalogPRDRecoveryRequest {
	return CatalogPRDRecoveryRequest{
		ReleaseID: "cat-20261023-c01e633", ExpectedReleaseSHA256: strings.Repeat("a", 64), ExpectedBundleSHA256: strings.Repeat("b", 64),
		ExpectedSelectedReleaseID: "cat-20261023-c01e633", ExpectedSelectedReleaseSHA256: strings.Repeat("a", 64), ExpectedSelectedBundleSHA256: strings.Repeat("b", 64),
		TeardownPairs: []string{pair},
	}
}

func TestCatalogPRDCompletedTeardownWorkGuardRequiresOnlyExactSessionCapacity(t *testing.T) {
	baseline := CatalogWorkInventory{ActiveSessionReservations: 2}
	if !catalogPRDRecoveryWorkMatches(baseline, "completed_teardown_pairs", 2) {
		t.Fatal("fully idle completed-teardown authority with exact session hold refused")
	}
	for name, mutate := range map[string]func(*CatalogWorkInventory){
		"active session":       func(w *CatalogWorkInventory) { w.ActiveSessions = 1 },
		"running command":      func(w *CatalogWorkInventory) { w.RunningCommands = 1 },
		"queued command":       func(w *CatalogWorkInventory) { w.QueuedCommands = 1 },
		"resumable job":        func(w *CatalogWorkInventory) { w.ResumableJobs = 1 },
		"live command slot":    func(w *CatalogWorkInventory) { w.ActiveCommandSlots = 1 },
		"extra session hold":   func(w *CatalogWorkInventory) { w.ActiveSessionReservations = 3 },
		"missing session hold": func(w *CatalogWorkInventory) { w.ActiveSessionReservations = 1 },
		"pending finalization": func(w *CatalogWorkInventory) { w.PendingRecoveryFinalizations = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			changed := baseline
			mutate(&changed)
			if catalogPRDRecoveryWorkMatches(changed, "completed_teardown_pairs", 2) {
				t.Fatal("unsafe completed-teardown work inventory accepted")
			}
		})
	}
	if catalogPRDRecoveryWorkMatches(baseline, "lost_pairs", 2) {
		t.Fatal("completed command with released slot was classified as lost-command recovery")
	}
	lost := CatalogWorkInventory{ActiveSessionReservations: 2, ActiveCommandSlots: 2}
	if !catalogPRDRecoveryWorkMatches(lost, "lost_pairs", 2) {
		t.Fatal("existing exact lost-command capacity guard regressed")
	}
	for _, mode := range []string{"", "unknown", "completed_teardown_pairs", "lost_pairs"} {
		if catalogPRDRecoveryWorkMatches(CatalogWorkInventory{}, mode, 0) {
			t.Fatalf("empty recovery selection accepted for mode %q", mode)
		}
	}
}

func TestCatalogPRDCompletedTeardownSummaryDoesNotAcceptLostOrPartialRecovery(t *testing.T) {
	valid := []byte("runner-locald recover-stalled: recovered_teardown_pairs=2 already_recovered_teardown_pairs=0\n")
	if count, err := parseCatalogPRDRecoverySummary(valid, "completed_teardown_pairs", 2); err != nil || count != 2 {
		t.Fatalf("completed-teardown summary=(%d,%v)", count, err)
	}
	for _, text := range []string{
		"runner-locald recover-stalled: recovered_lost_pairs=2 already_recovered_lost_pairs=0\n",
		"runner-locald recover-stalled: recovered_teardown_pairs=1 already_recovered_teardown_pairs=0\n",
		"runner-locald recover-stalled: recovered_teardown_pairs=2 already_recovered_teardown_pairs=1\n",
		"runner-locald recover-stalled: recovered_teardown_pairs=two already_recovered_teardown_pairs=0\n",
		"runner-locald recover-stalled: recovered_teardown_pairs=2 already_recovered_teardown_pairs=0junk\n",
		"runner-locald recover-stalled: recovered_teardown_pairs=2 already_recovered_teardown_pairs=0 recovered_lost_pairs=1\n",
		string(valid) + string(valid),
	} {
		if _, err := parseCatalogPRDRecoverySummary([]byte(text), "completed_teardown_pairs", 2); err == nil {
			t.Fatalf("invalid completed-teardown summary accepted: %q", text)
		}
	}
	if _, err := parseCatalogPRDRecoverySummary(valid, "completed_teardown_pairs", 0); err == nil {
		t.Fatal("zero expected pair count accepted")
	}
	if _, err := parseCatalogPRDRecoverySummary(valid, "unknown", 2); err == nil {
		t.Fatal("unknown recovery mode accepted")
	}
}

func TestCatalogPRDCompletedTeardownRefusesInvalidSelectionBeforeRuntimeBoundary(t *testing.T) {
	pair := "sess-0123456789abcdef0123456789abcdef:cmd-0123456789abcdef0123456789abcdef"
	for _, tc := range []struct {
		name, host string
		mutate     func(*CatalogPRDRecoveryRequest)
	}{
		{"mixed", "mac-control-plane", func(r *CatalogPRDRecoveryRequest) { r.LostPairs = []string{pair} }},
		{"another release", "mac-control-plane", func(r *CatalogPRDRecoveryRequest) { r.ReleaseID = "cat-another" }},
		{"non-Mac", "linux-host", func(*CatalogPRDRecoveryRequest) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := BuildPRDUpgradePlan(tc.host)
			if err != nil {
				t.Fatal(err)
			}
			request := catalogPRDCompletedTeardownFixtureRequest(pair)
			request.BackupDirectory = "/private/test-only/no-backup-created"
			request.ExpectedBackupManifestSHA = strings.Repeat("e", 64)
			tc.mutate(&request)
			service := &prdFenceOrderService{}
			_, err = RecoverCatalogPRDLostPairs(context.Background(), plan, service, request, neverCatalogRecoveryExecutor{})
			if err != ErrCatalogPRDRecovery || len(service.events) != 0 {
				t.Fatalf("invalid selection reached runtime boundary: error=%v service-events=%v", err, service.events)
			}
		})
	}
}

type neverCatalogRecoveryExecutor struct{}

func (neverCatalogRecoveryExecutor) Run(context.Context, string, []string) ([]byte, error) {
	panic("refused catalog recovery must not execute a service component")
}
