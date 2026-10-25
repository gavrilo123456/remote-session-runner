package releaseflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/laneinstall"
	"remote-session-runner/src/internal/store"
)

type receiptSyncHostState struct {
	active bool
	work   laneinstall.CatalogWorkInventory
	target string
	bundle string
	config string
	schema int
}

type receiptSyncFixture struct {
	lane     string
	receipt  laneinstall.CatalogCohortReceipt
	states   map[string]receiptSyncHostState
	commands []string
}

func (f *receiptSyncFixture) run(_ context.Context, host string, args []string, stdin []byte) ([]byte, error) {
	if len(args) == 0 {
		return nil, ErrInvalidInput
	}
	value := func(flag string) string {
		for index := 0; index+1 < len(args); index++ {
			if args[index] == flag {
				return args[index+1]
			}
		}
		return ""
	}
	state := f.states[host]
	switch args[0] {
	case "catalog-work-inventory":
		if value("--host") != host || value("--lane") != f.lane {
			return nil, ErrInvalidInput
		}
		report := laneinstall.CatalogWorkInventoryReport{HostID: host, Lane: config.ReleaseLane(f.lane),
			ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), ServiceActive: state.active,
			SelectorTarget: state.target, BundleSHA256: state.bundle, ConfigurationSHA256: state.config,
			DatabaseSchema: state.schema, WorkInventory: state.work, WorkInventoryEmpty: state.work.Empty()}
		return json.Marshal(report)
	case "stop":
		if value("--host") != host || value("--lane") != f.lane {
			return nil, ErrInvalidInput
		}
		f.commands = append(f.commands, "stop:"+host)
		state.active = false
		f.states[host] = state
		return []byte("stopped\n"), nil
	case "catalog-cohort":
		if value("--host") != host || value("--lane") != f.lane {
			return nil, ErrInvalidInput
		}
		switch value("--action") {
		case "discover":
			held := f.receipt.Stage != laneinstall.CatalogCohortReleased
			view := promotionAdmissionView{HostID: host, Lane: config.ReleaseLane(f.lane), AdmissionHeld: held}
			if held {
				view.OperationID = f.receipt.OperationID
				receipt := f.receipt
				view.Receipt = &receipt
			}
			return json.Marshal(view)
		case "inspect":
			view := promotionReceiptView{Present: true, Receipt: f.receipt}
			return json.Marshal(view)
		case "sync":
			if value("--receipt-file") != "@promo-stdin@" || len(stdin) == 0 || stdin[len(stdin)-1] != '\n' {
				return nil, ErrInvalidInput
			}
			var receipt laneinstall.CatalogCohortReceipt
			if json.Unmarshal(stdin, &receipt) != nil || receipt.OperationID != f.receipt.OperationID {
				return nil, ErrInvalidInput
			}
			for _, hostState := range f.states {
				if hostState.active && !(f.lane == string(config.ReleaseLanePRD) && receipt.Stage == laneinstall.CatalogCohortPrepared) {
					return nil, errors.New("receipt was synchronized while a selected lane service remained active")
				}
			}
			f.commands = append(f.commands, "sync:"+host)
			f.receipt = receipt
			return json.Marshal(map[string]string{"operation_id": receipt.OperationID, "stage": string(receipt.Stage)})
		default:
			return nil, ErrInvalidInput
		}
	default:
		return nil, fmt.Errorf("unexpected fixture command %q", args[0])
	}
}

func newReceiptSyncFixture(t *testing.T, activeHost string, nonEmptyHost string) *receiptSyncFixture {
	return newReceiptSyncFixtureForLane(t, "dev", activeHost, nonEmptyHost)
}

func newReceiptSyncFixtureForLane(t *testing.T, lane string, activeHost string, nonEmptyHost string) *receiptSyncFixture {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	held := lane != string(config.ReleaseLanePRD)
	receipt := promotionTestCohortReceipt(t, now, strings.Repeat("8", 32), "v2.0.0", nil, held)
	emptyWorkSHA := (laneinstall.CatalogWorkInventory{}).SHA256()
	states := make(map[string]receiptSyncHostState, len(HostOrder))
	for index, host := range HostOrder {
		configSHA := strings.Repeat(fmt.Sprintf("%x", index+1), 64)
		receipt.Hosts[index].ConfigurationSHA256 = configSHA
		receipt.Hosts[index].WorkInventorySHA256 = emptyWorkSHA
		state := receiptSyncHostState{active: host == activeHost, target: receipt.Hosts[index].PreviousTarget,
			bundle: receipt.Hosts[index].PreviousBundleSHA256, config: configSHA, schema: store.CurrentSchemaVersion}
		receipt.Hosts[index].ServiceActiveAtPreflight = host == activeHost
		if host == nonEmptyHost {
			state.work.ActiveSessions = 1
		}
		states[host] = state
	}
	receipt.Lane = config.ReleaseLane(lane)
	return &receiptSyncFixture{lane: lane, receipt: receipt, states: states}
}

func TestSyncReceiptStopsHeldEmptyLaneBeforePublishingReceipt(t *testing.T) {
	fixture := newReceiptSyncFixture(t, "linux-host", "")
	backend := &NativePromotionBackend{laneCommandRunner: fixture.run}
	if err := backend.syncReceiptFrom(context.Background(), "linux-host", "dev", fixture.receipt.OperationID); err != nil {
		t.Fatalf("synchronize held empty receipt: %v", err)
	}
	firstSync := -1
	firstStop := -1
	for index, command := range fixture.commands {
		if strings.HasPrefix(command, "stop:") && firstStop < 0 {
			firstStop = index
		}
		if strings.HasPrefix(command, "sync:") && firstSync < 0 {
			firstSync = index
		}
	}
	if firstStop < 0 || firstSync < 0 || firstStop > firstSync {
		t.Fatalf("receipt sync was not preceded by selected-lane stop: %v", fixture.commands)
	}
	for host, state := range fixture.states {
		if state.active {
			t.Errorf("%s remained active after receipt synchronization", host)
		}
	}
}

func TestSyncReceiptRefusesWorkBeforeStoppingAnyLane(t *testing.T) {
	fixture := newReceiptSyncFixture(t, "linux-host", "factory-sandbox")
	backend := &NativePromotionBackend{laneCommandRunner: fixture.run}
	if err := backend.syncReceiptFrom(context.Background(), "linux-host", "dev", fixture.receipt.OperationID); err == nil {
		t.Fatal("receipt synchronization accepted retained work")
	}
	if fixture.states["linux-host"].active == false {
		t.Fatal("active Linux service was stopped before all-host work preflight passed")
	}
	for _, command := range fixture.commands {
		if strings.HasPrefix(command, "stop:") || strings.HasPrefix(command, "sync:") {
			t.Fatalf("mutation occurred before all-host empty-work checks: %v", fixture.commands)
		}
	}
}

func TestSyncPreparedPRDReceiptBeforeFencingActivePeers(t *testing.T) {
	fixture := newReceiptSyncFixtureForLane(t, string(config.ReleaseLanePRD), "linux-host", "")
	// Mac was already stopped for the exact-pair recovery; the three workers
	// remain active until their individual admission fences are recorded.
	for index, host := range HostOrder {
		state := fixture.states[host]
		state.active = host != "mac-control-plane"
		fixture.states[host] = state
		fixture.receipt.Hosts[index].ServiceActiveAtPreflight = state.active
	}
	backend := &NativePromotionBackend{laneCommandRunner: fixture.run}

	if err := backend.syncReceiptFrom(context.Background(), "mac-control-plane", "prd", fixture.receipt.OperationID); err != nil {
		t.Fatalf("synchronize prepared PRD receipt before fencing: %v", err)
	}
	if len(fixture.commands) != len(HostOrder) {
		t.Fatalf("prepared receipt sync should publish once to each host without stopping peers: %v", fixture.commands)
	}
	for index, host := range HostOrder {
		if fixture.commands[index] != "sync:"+host {
			t.Fatalf("unexpected command order, want sync-only cohort publication: %v", fixture.commands)
		}
	}
	for host, state := range fixture.states {
		if host == "mac-control-plane" && state.active {
			t.Fatal("Mac unexpectedly restarted during prepared receipt synchronization")
		}
		if host != "mac-control-plane" && !state.active {
			t.Fatalf("%s was stopped before its initial admission fence", host)
		}
	}
}

type switchReceiptFixture struct {
	op       Operation
	receipts map[string]laneinstall.CatalogCohortReceipt
	states   map[string]receiptSyncHostState
	selected map[string]bool
	commands []string
}

func (f *switchReceiptFixture) run(_ context.Context, host string, args []string, stdin []byte) ([]byte, error) {
	if len(args) == 0 {
		return nil, ErrInvalidInput
	}
	value := func(flag string) string {
		for index := 0; index+1 < len(args); index++ {
			if args[index] == flag {
				return args[index+1]
			}
		}
		return ""
	}
	state := f.states[host]
	command := args[0]
	if command == "catalog-cohort" {
		action := value("--action")
		f.commands = append(f.commands, "cohort:"+host+":"+action)
		switch action {
		case "inspect":
			return json.Marshal(promotionReceiptView{Present: true, Receipt: f.receipts[host]})
		case "discover":
			receipt := f.receipts[host]
			return json.Marshal(promotionAdmissionView{HostID: host, Lane: config.ReleaseLane(f.op.Lane),
				AdmissionHeld: true, OperationID: receipt.OperationID, Receipt: &receipt})
		case "preflight":
			receipt := f.receipts[host]
			updated, err := laneinstall.AdvanceCatalogCohortReceipt(receipt, "preflight", host, strings.Repeat("e", 64), time.Now().UTC())
			if err != nil {
				return nil, err
			}
			f.receipts[host] = updated
			return []byte("{}"), nil
		case "verify-start":
			if host != "mac-control-plane" {
				return nil, ErrInvalidInput
			}
			receipt := f.receipts[host]
			updated, err := laneinstall.AdvanceCatalogCohortReceipt(receipt, "verify-start", "", value("--evidence-sha256"), time.Now().UTC())
			if err != nil {
				return nil, err
			}
			f.receipts[host] = updated
			return []byte("{}"), nil
		case "verify":
			if value("--subject-host") != host {
				return nil, ErrInvalidInput
			}
			receipt := f.receipts[host]
			updated, err := laneinstall.AdvanceCatalogCohortReceipt(receipt, "verify", host, value("--evidence-sha256"), time.Now().UTC())
			if err != nil {
				return nil, err
			}
			f.receipts[host] = updated
			return []byte("{}"), nil
		case "sync":
			if value("--receipt-file") != "@promo-stdin@" || len(stdin) == 0 || stdin[len(stdin)-1] != '\n' {
				return nil, ErrInvalidInput
			}
			var receipt laneinstall.CatalogCohortReceipt
			if json.Unmarshal(stdin, &receipt) != nil {
				return nil, ErrInvalidInput
			}
			for _, hostState := range f.states {
				if hostState.active {
					return nil, laneinstall.ErrSimpleLaneNotQuiescent
				}
			}
			f.receipts[host] = receipt
			return []byte("{}"), nil
		default:
			return nil, ErrInvalidInput
		}
	}
	switch command {
	case "catalog-work-inventory":
		report := laneinstall.CatalogWorkInventoryReport{HostID: host, Lane: config.ReleaseLane(f.op.Lane),
			ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), ServiceActive: state.active,
			SelectorTarget: state.target, BundleSHA256: state.bundle, ConfigurationSHA256: state.config,
			DatabaseSchema: state.schema, WorkInventory: state.work, WorkInventoryEmpty: state.work.Empty()}
		return json.Marshal(report)
	case "catalog-status":
		var releaseID, releaseSHA, bundleSHA string
		if f.selected[host] {
			releaseID, releaseSHA, bundleSHA = f.op.Release.ID, f.op.Release.Digest, f.op.Release.Bundles[host].SHA256
		} else {
			releaseID, releaseSHA, bundleSHA = "cat-20261023-c01e633", strings.Repeat("9", 64), state.bundle
		}
		status := laneinstall.CatalogSelectorStatus{HostID: host, Lane: config.ReleaseLane(f.op.Lane), Target: state.target, Kind: "catalog",
			Release: &laneinstall.CatalogReleaseStatus{Identity: laneinstall.CatalogReleaseIdentity{
				ReleaseID: releaseID, ReleaseSHA256: releaseSHA, HostID: host, BundleSHA256: bundleSHA}}}
		return json.Marshal(status)
	case "catalog-selector-find":
		if f.selected[host] {
			return []byte("[{\"operation_id\":\"selector-" + host + "\",\"stage\":\"complete\"}]"), nil
		}
		return []byte("[]"), nil
	case "stop":
		f.commands = append(f.commands, "stop:"+host)
		state.active = false
		f.states[host] = state
		return []byte("stopped\\n"), nil
	case "catalog-select":
		hostIndex := hostIndex(host)
		if hostIndex < 0 || value("--expected-target") != f.receipts[host].Hosts[hostIndex].PreviousTarget {
			return nil, fmt.Errorf("catalog-select did not bind the exact predecessor target for %s", host)
		}
		f.commands = append(f.commands, "select:"+host)
		f.selected[host] = true
		receipt := f.receipts[host]
		updated, err := laneinstall.AdvanceCatalogCohortReceipt(receipt, "switch", host, value("--evidence-sha256"), time.Now().UTC())
		if err != nil {
			return nil, err
		}
		f.receipts[host] = updated
		state.target = filepath.Join("/catalog", f.op.Release.ID, host)
		state.bundle = f.op.Release.Bundles[host].SHA256
		f.states[host] = state
		return []byte("{\"operation_id\":\"selector-" + host + "\",\"stage\":\"complete\"}"), nil
	case "start":
		f.commands = append(f.commands, "start:"+host)
		state.active = true
		f.states[host] = state
		return []byte("started\n"), nil
	default:
		return nil, fmt.Errorf("unexpected switch fixture command %q", command)
	}
}

func TestEnsureCohortVerifyingSynchronizesAllHostProofBeforeAcceptance(t *testing.T) {
	op := Operation{Lane: "dev", OperationID: strings.Repeat("7", 32), Stage: StageVerifying, Release: testRelease()}
	op.CohortOperationID = strings.Repeat("8", 32)
	now := time.Now().UTC().Add(-30 * time.Second).Truncate(time.Second)
	receipt := promotionTestCohortReceipt(t, now, op.CohortOperationID, "v2.0.0", nil, true)
	receipt.ReleaseID = op.Release.ID
	receipt.ReleaseSHA256 = op.Release.Digest
	receipt.SourceRevision = op.Release.Source.Commit
	states := make(map[string]receiptSyncHostState, len(HostOrder))
	receipts := make(map[string]laneinstall.CatalogCohortReceipt, len(HostOrder))
	selected := make(map[string]bool, len(HostOrder))
	for index, host := range HostOrder {
		configSHA := strings.Repeat(fmt.Sprintf("%x", index+1), 64)
		receipt.Hosts[index].Target = laneinstall.CatalogReleaseIdentity{ReleaseID: op.Release.ID,
			ReleaseSHA256: op.Release.Digest, HostID: host, BundleSHA256: op.Release.Bundles[host].SHA256}
		receipt.Hosts[index].ConfigurationSHA256 = configSHA
		receipt.Hosts[index].WorkInventorySHA256 = (laneinstall.CatalogWorkInventory{}).SHA256()
		receipt.Hosts[index].PreflightAt = now.Format(time.RFC3339Nano)
		states[host] = receiptSyncHostState{active: true, target: filepath.Join("/catalog", op.Release.ID, host),
			bundle: op.Release.Bundles[host].SHA256, config: configSHA, schema: store.CurrentSchemaVersion}
		selected[host] = true
	}
	var err error
	for index, host := range HostOrder {
		preflightAt := now.Add(time.Duration(index*2) * time.Second)
		receipt, err = laneinstall.AdvanceCatalogCohortReceipt(receipt, "preflight", host, strings.Repeat("e", 64), preflightAt)
		if err != nil {
			t.Fatalf("record %s preflight: %v", host, err)
		}
		receipt, err = laneinstall.AdvanceCatalogCohortReceipt(receipt, "switch", host, strings.Repeat("f", 64), preflightAt.Add(time.Second))
		if err != nil {
			t.Fatalf("record %s switch: %v", host, err)
		}
	}
	for _, host := range HostOrder {
		receipts[host] = receipt
	}
	fixture := &switchReceiptFixture{op: op, receipts: receipts, states: states, selected: selected}
	backend := &NativePromotionBackend{laneCommandRunner: fixture.run,
		capacityReader: func(_ context.Context, _ string, _ string, snapshot *promotionLaneSnapshot) error {
			snapshot.AvailableBytes, snapshot.RequiredBytes = 2_000_000_000, 500_000_000
			return nil
		}}
	if err := backend.ensureCohortVerifying(context.Background(), op); err != nil {
		t.Fatalf("prepare synchronized verification state before probes: %v; commands=%v", err, fixture.commands)
	}
	for _, host := range HostOrder {
		current := fixture.receipts[host]
		if current.Stage != laneinstall.CatalogCohortVerifying || !receiptHasVerify(current, host) {
			t.Fatalf("%s receipt is not synchronized and verified before probe admission: stage=%s transitions=%+v", host, current.Stage, current.Transitions)
		}
		if fixture.states[host].active {
			t.Fatalf("%s was left active after receipt synchronization", host)
		}
	}
	if !reflectReceiptEqual(fixture.receipts[HostOrder[0]], fixture.receipts[HostOrder[len(HostOrder)-1]]) {
		t.Fatal("host receipts diverged after verification preparation")
	}
	startIndex := -1
	for index, command := range fixture.commands {
		if command == "cohort:mac-control-plane:verify-start" {
			startIndex = index
			break
		}
	}
	if startIndex < 0 {
		t.Fatalf("verify-start was not issued through the public cohort command: %v", fixture.commands)
	}
	for index, host := range HostOrder {
		want := "cohort:" + host + ":verify"
		found := -1
		for commandIndex := startIndex + 1; commandIndex < len(fixture.commands); commandIndex++ {
			if fixture.commands[commandIndex] == want {
				found = commandIndex
				break
			}
		}
		if found < 0 || index > 0 && found <= startIndex {
			t.Fatalf("ordered verification command %s missing after verify-start: %v", want, fixture.commands)
		}
		startIndex = found
	}
	commandsBeforeRetry := len(fixture.commands)
	if err := backend.ensureCohortVerifying(context.Background(), op); err != nil {
		t.Fatalf("resume already synchronized verifying receipt: %v", err)
	}
	for _, command := range fixture.commands[commandsBeforeRetry:] {
		if strings.Contains(command, ":verify-start") || strings.HasSuffix(command, ":verify") {
			t.Fatalf("retry repeated a completed receipt transition: %v", fixture.commands[commandsBeforeRetry:])
		}
	}
}

func TestSwitchHostSynchronizesSelectedReceiptBeforeStartingService(t *testing.T) {
	op := Operation{Lane: "dev", OperationID: strings.Repeat("7", 32), Stage: StageSwitching, Release: testRelease()}
	op.CohortOperationID = strings.Repeat("8", 32)
	now := time.Now().UTC().Truncate(time.Second)
	receipt := promotionTestCohortReceipt(t, now, op.CohortOperationID, "v2.0.0", nil, true)
	receipt.ReleaseID = op.Release.ID
	receipt.ReleaseSHA256 = op.Release.Digest
	receipt.SourceRevision = op.Release.Source.Commit
	states := make(map[string]receiptSyncHostState, len(HostOrder))
	receipts := make(map[string]laneinstall.CatalogCohortReceipt, len(HostOrder))
	selected := make(map[string]bool, len(HostOrder))
	for index, host := range HostOrder {
		configSHA := strings.Repeat(fmt.Sprintf("%x", index+1), 64)
		receipt.Hosts[index].Target = laneinstall.CatalogReleaseIdentity{ReleaseID: op.Release.ID,
			ReleaseSHA256: op.Release.Digest, HostID: host, BundleSHA256: op.Release.Bundles[host].SHA256}
		receipt.Hosts[index].ConfigurationSHA256 = configSHA
		receipt.Hosts[index].WorkInventorySHA256 = (laneinstall.CatalogWorkInventory{}).SHA256()
		receipt.Hosts[index].PreflightAt = now.Format(time.RFC3339Nano)
		states[host] = receiptSyncHostState{target: receipt.Hosts[index].PreviousTarget,
			bundle: receipt.Hosts[index].PreviousBundleSHA256, config: configSHA, schema: store.CurrentSchemaVersion}
		receipts[host] = receipt
	}
	fixture := &switchReceiptFixture{op: op, receipts: receipts, states: states, selected: selected}
	backend := &NativePromotionBackend{laneCommandRunner: fixture.run,
		capacityReader: func(_ context.Context, _ string, _ string, snapshot *promotionLaneSnapshot) error {
			snapshot.AvailableBytes, snapshot.RequiredBytes = 2_000_000_000, 500_000_000
			return nil
		}}
	observation, err := backend.SwitchHost(context.Background(), op, "linux-host")
	if err != nil {
		t.Fatalf("switch Linux selector and synchronize receipt: %v", err)
	}
	if observation.SelectedReleaseID != op.Release.ID || observation.ServiceStopped != true || !observation.JournalVerified {
		t.Fatalf("selector should be verified while the held service remains stopped: %+v", observation)
	}
	selectIndex, syncAfterSelect := -1, -1
	for index, command := range fixture.commands {
		if command == "select:linux-host" {
			selectIndex = index
		}
		if selectIndex >= 0 && strings.HasPrefix(command, "cohort:") && strings.HasSuffix(command, ":sync") {
			syncAfterSelect = index
			break
		}
		if strings.HasPrefix(command, "start:") {
			t.Fatalf("service started before cohort acceptance: %v", fixture.commands)
		}
	}
	if selectIndex < 0 || syncAfterSelect < selectIndex {
		t.Fatalf("selected receipt was not synchronized after selector change: %v", fixture.commands)
	}
}

func TestSwitchHostBindsExactPRDPredecessorTarget(t *testing.T) {
	op := Operation{Lane: "prd", OperationID: strings.Repeat("7", 32), Stage: StageSwitching, Release: testRelease()}
	op.CohortOperationID = strings.Repeat("8", 32)
	now := time.Now().UTC().Truncate(time.Second)
	receipt := promotionTestCohortReceipt(t, now, op.CohortOperationID, "v2.0.0", nil, false)
	receipt.Lane = config.ReleaseLanePRD
	receipt.ReleaseID = op.Release.ID
	receipt.ReleaseSHA256 = op.Release.Digest
	receipt.SourceRevision = op.Release.Source.Commit
	receipt.FenceProofs = []laneinstall.CatalogCohortFenceProof{{
		Scope: "project-publishers:prd", Control: "fixture-prd-publisher-pause", Owner: "fixture-owner",
		Paused: true, NoNewAccepted: true, EvidenceSHA256: strings.Repeat("7", 64),
		VerifiedAt: now.Format(time.RFC3339Nano),
	}}
	states := make(map[string]receiptSyncHostState, len(HostOrder))
	receipts := make(map[string]laneinstall.CatalogCohortReceipt, len(HostOrder))
	selected := make(map[string]bool, len(HostOrder))
	var err error
	for index, host := range HostOrder {
		plan, err := laneinstall.BuildCatalogPlan(host, config.ReleaseLanePRD)
		if err != nil {
			t.Fatal(err)
		}
		root, err := laneinstall.DefaultCatalogRootForPlan(plan)
		if err != nil {
			t.Fatal(err)
		}
		previousTarget := filepath.Join(root, "cat-20261023-c01e633", host)
		receipt.Hosts[index].PreviousTarget = previousTarget
		receipt.Hosts[index].PreviousReadSchemaMax = store.CurrentSchemaVersion
		receipt.Hosts[index].Target = laneinstall.CatalogReleaseIdentity{ReleaseID: op.Release.ID,
			ReleaseSHA256: op.Release.Digest, HostID: host, BundleSHA256: op.Release.Bundles[host].SHA256}
		configSHA := strings.Repeat(fmt.Sprintf("%x", index+1), 64)
		receipt.Hosts[index].ConfigurationSHA256 = configSHA
		receipt.Hosts[index].WorkInventorySHA256 = (laneinstall.CatalogWorkInventory{}).SHA256()
		receipt.Hosts[index].PreflightAt = now.Format(time.RFC3339Nano)
		states[host] = receiptSyncHostState{target: previousTarget, bundle: receipt.Hosts[index].PreviousBundleSHA256,
			config: configSHA, schema: store.CurrentSchemaVersion}
	}
	for _, host := range HostOrder {
		hostIndex := hostIndex(host)
		preflight := receipt.Hosts[hostIndex]
		observedAt := time.Now().UTC()
		receipt, err = laneinstall.AdvanceCatalogCohortHostFence(receipt, laneinstall.CatalogCohortHostFence{
			HostID: host, ServiceStopped: true, ServiceWasActive: preflight.ServiceActiveAtPreflight,
			WorkInventorySHA256: preflight.WorkInventorySHA256, ConfigurationSHA256: preflight.ConfigurationSHA256,
			SelectorTarget: preflight.PreviousTarget, PreviousBundleSHA256: preflight.PreviousBundleSHA256,
			DatabaseSchema: preflight.SchemaVersion, ObservedAt: observedAt.Format(time.RFC3339Nano),
		}, observedAt)
		if err != nil {
			t.Fatalf("record PRD host fence for %s: %v", host, err)
		}
	}
	receipt, err = laneinstall.AdvanceCatalogCohortReceipt(receipt, "hold", "", strings.Repeat("d", 64), time.Now().UTC())
	if err != nil {
		t.Fatalf("record PRD cohort hold: %v", err)
	}
	for _, host := range HostOrder {
		receipts[host] = receipt
	}
	if err := laneinstall.ValidateCatalogCohortReceipt(receipt); err != nil {
		t.Fatalf("invalid PRD switch fixture receipt: %v", err)
	}
	fixture := &switchReceiptFixture{op: op, receipts: receipts, states: states, selected: selected}
	backend := &NativePromotionBackend{laneCommandRunner: fixture.run,
		capacityReader: func(_ context.Context, _ string, _ string, snapshot *promotionLaneSnapshot) error {
			snapshot.AvailableBytes, snapshot.RequiredBytes = 2_000_000_000, 500_000_000
			return nil
		}}
	observation, err := backend.SwitchHost(context.Background(), op, "linux-host")
	if err != nil {
		t.Fatalf("switch PRD selector with exact predecessor binding: %v; commands=%v", err, fixture.commands)
	}
	if !observation.JournalVerified || observation.SelectedReleaseID != op.Release.ID {
		t.Fatalf("expected verified PRD selector switch while held: %+v", observation)
	}
	preflightIndex, selectIndex := -1, -1
	for index, command := range fixture.commands {
		if command == "cohort:linux-host:preflight" {
			preflightIndex = index
		}
		if command == "select:linux-host" && selectIndex < 0 {
			selectIndex = index
		}
	}
	if preflightIndex < 0 || selectIndex <= preflightIndex {
		t.Fatalf("expected a synchronized preflight before selector mutation: %v", fixture.commands)
	}
	for _, command := range fixture.commands[preflightIndex+1 : selectIndex] {
		if command == "catalog-status" {
			t.Fatalf("redundant coordinator state read consumed the per-host preflight window: %v", fixture.commands)
		}
	}
}

func TestRunHostTasksExecutesIndependentHostsConcurrently(t *testing.T) {
	backend := &NativePromotionBackend{}
	started := make(chan string, len(HostOrder))
	release := make(chan struct{})
	done := make(chan []error, 1)
	go func() {
		done <- backend.runHostTasks(context.Background(), func(_ int, host string) error {
			started <- host
			<-release
			return nil
		})
	}()
	seen := make(map[string]bool, len(HostOrder))
	for range HostOrder {
		select {
		case host := <-started:
			if seen[host] {
				close(release)
				t.Fatalf("host task started more than once: %s", host)
			}
			seen[host] = true
		case <-time.After(2 * time.Second):
			close(release)
			<-done
			t.Fatal("independent host tasks did not all start before any was released")
		}
	}
	close(release)
	for _, err := range <-done {
		if err != nil {
			t.Fatalf("host task returned an error: %v", err)
		}
	}
	if len(seen) != len(HostOrder) {
		t.Fatalf("expected one task for each host, got %v", seen)
	}
}

func interruptedActiveSwitchFixture(t *testing.T, nonEmptyHost string) (*switchReceiptFixture, *NativePromotionBackend, Operation) {
	t.Helper()
	op := Operation{Lane: "dev", OperationID: strings.Repeat("7", 32), Stage: StageSwitching, Release: testRelease()}
	op.CohortOperationID = strings.Repeat("8", 32)
	now := time.Now().UTC().Truncate(time.Second)
	receipt := promotionTestCohortReceipt(t, now, op.CohortOperationID, "v2.0.0", nil, true)
	receipt.ReleaseID = op.Release.ID
	receipt.ReleaseSHA256 = op.Release.Digest
	receipt.SourceRevision = op.Release.Source.Commit
	states := make(map[string]receiptSyncHostState, len(HostOrder))
	receipts := make(map[string]laneinstall.CatalogCohortReceipt, len(HostOrder))
	selected := make(map[string]bool, len(HostOrder))
	for index, host := range HostOrder {
		configSHA := strings.Repeat(fmt.Sprintf("%x", index+1), 64)
		receipt.Hosts[index].Target = laneinstall.CatalogReleaseIdentity{ReleaseID: op.Release.ID,
			ReleaseSHA256: op.Release.Digest, HostID: host, BundleSHA256: op.Release.Bundles[host].SHA256}
		receipt.Hosts[index].ConfigurationSHA256 = configSHA
		receipt.Hosts[index].WorkInventorySHA256 = (laneinstall.CatalogWorkInventory{}).SHA256()
		receipt.Hosts[index].PreflightAt = now.Format(time.RFC3339Nano)
		state := receiptSyncHostState{target: receipt.Hosts[index].PreviousTarget,
			bundle: receipt.Hosts[index].PreviousBundleSHA256, config: configSHA, schema: store.CurrentSchemaVersion}
		if host == nonEmptyHost {
			state.work.ActiveSessions = 1
		}
		states[host] = state
	}
	var err error
	receipt, err = laneinstall.AdvanceCatalogCohortReceipt(receipt, "preflight", "linux-host", strings.Repeat("e", 64), now)
	if err != nil {
		t.Fatalf("add Linux preflight transition: %v", err)
	}
	for _, host := range HostOrder {
		receipts[host] = receipt
	}
	linuxReceipt, err := laneinstall.AdvanceCatalogCohortReceipt(receipt, "switch", "linux-host", strings.Repeat("f", 64), now.Add(time.Second))
	if err != nil {
		t.Fatalf("record interrupted Linux switch: %v", err)
	}
	receipts["linux-host"] = linuxReceipt
	selected["linux-host"] = true
	linux := states["linux-host"]
	linux.active = true
	linux.target = filepath.Join("/catalog", op.Release.ID, "linux-host")
	linux.bundle = op.Release.Bundles["linux-host"].SHA256
	states["linux-host"] = linux
	fixture := &switchReceiptFixture{op: op, receipts: receipts, states: states, selected: selected}
	backend := &NativePromotionBackend{laneCommandRunner: fixture.run,
		capacityReader: func(_ context.Context, _ string, _ string, snapshot *promotionLaneSnapshot) error {
			snapshot.AvailableBytes, snapshot.RequiredBytes = 2_000_000_000, 500_000_000
			return nil
		}}
	return fixture, backend, op
}

func TestObserveHostRecoversInterruptedActiveHeldSwitchThroughReceiptSync(t *testing.T) {
	fixture, backend, op := interruptedActiveSwitchFixture(t, "")
	observation, err := backend.ObserveHost(context.Background(), op, "linux-host")
	if err != nil {
		t.Fatalf("observe interrupted held Linux switch: %v", err)
	}
	if observation.SelectedReleaseID != op.Release.ID || observation.SelectedBundleSHA != op.Release.Bundles["linux-host"].SHA256 ||
		!observation.JournalVerified || !observation.ServiceStopped || !observation.WorkInventoryEmpty {
		t.Fatalf("interrupted Linux switch was not reconciled to stopped, verified state: %+v", observation)
	}
	stopIndex, firstSync := -1, -1
	for index, command := range fixture.commands {
		if command == "stop:linux-host" && stopIndex < 0 {
			stopIndex = index
		}
		if strings.HasSuffix(command, ":sync") && firstSync < 0 {
			firstSync = index
		}
	}
	if stopIndex < 0 || firstSync < 0 || stopIndex > firstSync {
		t.Fatalf("active selected lane was not stopped before receipt synchronization: %v", fixture.commands)
	}
	for _, host := range HostOrder {
		if fixture.states[host].active {
			t.Errorf("%s remained active after receipt synchronization", host)
		}
		if !reflectReceiptEqual(fixture.receipts[host], fixture.receipts["linux-host"]) {
			t.Errorf("%s receipt did not receive the interrupted switch history", host)
		}
	}
}

func TestObserveHostDoesNotStopInterruptedSwitchWhenAnyHostHasWork(t *testing.T) {
	fixture, backend, op := interruptedActiveSwitchFixture(t, "factory-sandbox")
	if _, err := backend.ObserveHost(context.Background(), op, "linux-host"); err == nil {
		t.Fatal("active interrupted selector was reconciled while Factory retained work")
	}
	if !fixture.states["linux-host"].active {
		t.Fatal("Linux service was stopped before every cohort work inventory passed")
	}
	for _, command := range fixture.commands {
		if strings.HasPrefix(command, "stop:") || strings.HasSuffix(command, ":sync") {
			t.Fatalf("receipt or service mutation occurred before all-host empty-work checks: %v", fixture.commands)
		}
	}
}

func TestObserveHostDoesNotStopActiveCandidateWithoutMatchingSwitchReceipt(t *testing.T) {
	fixture, backend, op := interruptedActiveSwitchFixture(t, "")
	// The selector and journal report the candidate, but the local receipt has
	// only the held preflight history. Recovery must not infer the missing
	// switch transition from selector state alone.
	fixture.receipts["linux-host"] = fixture.receipts["sandbox-host"]
	if _, err := backend.ObserveHost(context.Background(), op, "linux-host"); err == nil {
		t.Fatal("active candidate selector without matching receipt evidence was accepted")
	}
	if !fixture.states["linux-host"].active {
		t.Fatal("active lane was stopped without its matching switch transition")
	}
	for _, command := range fixture.commands {
		if strings.HasPrefix(command, "stop:") || strings.HasSuffix(command, ":sync") {
			t.Fatalf("host state changed without matching switch receipt: %v", fixture.commands)
		}
	}
}
