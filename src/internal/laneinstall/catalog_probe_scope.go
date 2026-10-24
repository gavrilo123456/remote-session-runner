package laneinstall

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
)

const catalogProbeScopeFormat = 3

const (
	CatalogMaintenanceProbeTokenHeader  = "X-Runner-Catalog-Maintenance-Token"
	CatalogMaintenanceProbeDigestHeader = "X-Runner-Catalog-Maintenance-Request-SHA256"
)

type CatalogProbeTransport string

const (
	CatalogProbeDirectMTLS CatalogProbeTransport = "direct-mtls"
	CatalogProbeQueuedSSH  CatalogProbeTransport = "queued-ssh"
	CatalogProbeMacLocal   CatalogProbeTransport = "mac-local"
)

var ErrCatalogProbeAdmission = errors.New("catalog maintenance probe admission refused")

var probeEnvironmentName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

type catalogMaintenanceProbeRequest struct {
	Environment string `json:"environment"`
	Target      struct {
		Kind    string `json:"kind"`
		Profile string `json:"profile"`
	} `json:"execution_target"`
	Source struct {
		Mode string `json:"mode"`
	} `json:"source"`
	Script         string         `json:"script"`
	TimeoutSeconds int            `json:"timeout_seconds,omitempty"`
	Limits         map[string]int `json:"limits,omitempty"`
}

type catalogProbeScope struct {
	FormatVersion     int                   `json:"format_version"`
	OperationID       string                `json:"operation_id"`
	HostID            string                `json:"host_id"`
	Lane              config.ReleaseLane    `json:"lane"`
	Transport         CatalogProbeTransport `json:"transport"`
	ControllerType    domain.ControllerType `json:"controller_type"`
	ControllerID      string                `json:"controller_id"`
	Environment       string                `json:"environment"`
	ConfigurationSHA  string                `json:"configuration_sha256"`
	CreatedAt         string                `json:"created_at"`
	ExpiresAt         string                `json:"expires_at"`
	TokenSHA256       string                `json:"token_sha256"`
	IdempotencyKey    string                `json:"idempotency_key"`
	RequestBodySHA256 string                `json:"request_body_sha256"`
	State             string                `json:"state"`
	JobID             string                `json:"job_id,omitempty"`
	IntentID          string                `json:"intent_id,omitempty"`
}

// CatalogMaintenanceProbeGuard admits one exact transport-specific one-off
// job while this host's selected cohort is in its verification stage. The
// ephemeral token supplements, and never replaces, the normal authenticated
// controller mapping.
type CatalogMaintenanceProbeGuard struct {
	plan          Plan
	operationID   string
	transport     CatalogProbeTransport
	configuration config.Config
	testPaths     *catalogProbeTestPaths
}

// CatalogMaintenanceProbeBinding is the unique policy binding for one fixed
// maintenance probe. Its configuration digest is the digest of the exact
// validated bytes loaded by the host.
type CatalogMaintenanceProbeBinding struct {
	Environment      string
	ConfigurationSHA string
	TargetKind       string
	TargetProfile    string
}

// ResolveCatalogMaintenanceProbeBinding resolves the only environment that
// permits the fixed empty-source five-second probe for this authenticated
// controller and transport. V3 keeps its explicit lane environment name;
// V1/V2 use their existing policy names and are rejected when resolution is
// missing or ambiguous.
func ResolveCatalogMaintenanceProbeBinding(loaded config.Config, plan Plan, transport CatalogProbeTransport,
	controllerType domain.ControllerType, controllerID string) (CatalogMaintenanceProbeBinding, error) {
	if validateCatalogPlan(plan) != nil || !validCatalogProbeTransport(plan, transport) ||
		loaded.SourceSHA256() == "" || loaded.ReleaseLane() != plan.Lane ||
		(plan.Namespace.Host == config.HostKindMac && loaded.Kind() != config.HostKindMac) ||
		(plan.Namespace.Host == config.HostKindLinux && loaded.Kind() != config.HostKindLinux) ||
		!catalogProbeControllerAllowed(transport, controllerType) {
		return CatalogMaintenanceProbeBinding{}, ErrCatalogProbeAdmission
	}
	targetKind, targetProfile := domain.TargetKindRemote, plan.HostID
	if transport == CatalogProbeMacLocal {
		targetKind, targetProfile = domain.TargetKindLocal, "mac-workstation"
	}
	target, err := domain.NewExecutionTarget(targetKind, targetProfile)
	if err != nil {
		return CatalogMaintenanceProbeBinding{}, ErrCatalogProbeAdmission
	}
	controller, err := domain.NewControllerIdentity(controllerType, domain.ControllerID(controllerID))
	if err != nil {
		return CatalogMaintenanceProbeBinding{}, ErrCatalogProbeAdmission
	}
	request := domain.SessionPolicyRequest{
		Target: target, Source: domain.NewEmptySource(), Controller: controller,
		Limits: domain.RequestedLimits{CommandTimeout: 5 * time.Second},
	}
	var matches []string
	for _, name := range loaded.EnvironmentNames() {
		if !catalogMaintenanceEnvironmentAllowed(loaded.SchemaVersion(), name, targetProfile, plan.Lane) {
			continue
		}
		registered, ok := loaded.Environment(name)
		if !ok {
			continue
		}
		if _, policyErr := registered.Policy().ValidateSessionPolicy(request); policyErr == nil {
			matches = append(matches, name)
		}
	}
	if len(matches) != 1 {
		return CatalogMaintenanceProbeBinding{}, ErrCatalogProbeAdmission
	}
	return CatalogMaintenanceProbeBinding{
		Environment: matches[0], ConfigurationSHA: loaded.SourceSHA256(),
		TargetKind: string(targetKind), TargetProfile: targetProfile,
	}, nil
}

func catalogMaintenanceEnvironmentAllowed(schemaVersion int, name, targetProfile string, lane config.ReleaseLane) bool {
	if schemaVersion != config.VersionV3 {
		return schemaVersion == config.VersionV1 || schemaVersion == config.VersionV2
	}
	return name == targetProfile+"-"+string(lane)
}

type catalogProbeTestPaths struct {
	cohortRoot string
	scopeRoot  string
	layout     catalogSelectorLayout
}

func (g *CatalogMaintenanceProbeGuard) OperationID() string {
	if g == nil {
		return ""
	}
	return g.operationID
}

func (g *CatalogMaintenanceProbeGuard) ValidateFixedProbeRequest(environment, targetKind, targetProfile, sourceMode, script string, timeoutSeconds int64) error {
	if g == nil {
		return ErrCatalogProbeAdmission
	}
	if g.configuration.SourceSHA256() == "" {
		// Compatibility for isolated handler tests. Production services always
		// construct the guard with the already-loaded configuration snapshot.
		wantEnvironment := string(g.plan.HostID) + "-" + string(g.plan.Lane)
		wantKind, wantProfile := "remote", g.plan.HostID
		if g.transport == CatalogProbeMacLocal {
			wantEnvironment, wantKind, wantProfile = "mac-workstation-"+string(g.plan.Lane), "local", "mac-workstation"
		}
		if environment != wantEnvironment || targetKind != wantKind || targetProfile != wantProfile || sourceMode != "empty" ||
			script != "printf 'catalog release maintenance probe\\n'" || timeoutSeconds != 5 {
			return ErrCatalogProbeAdmission
		}
		return nil
	}
	paths, err := g.paths()
	if err != nil {
		return ErrCatalogProbeAdmission
	}
	scope, err := readCatalogProbeScope(g.scopePath(paths.scopeRoot))
	if err != nil {
		return ErrCatalogProbeAdmission
	}
	return g.ValidateFixedProbeRequestForController(environment, targetKind, targetProfile, sourceMode, script,
		timeoutSeconds, scope.ControllerType, scope.ControllerID)
}

// ValidateFixedProbeRequestForController validates the exact fixed command
// against the loaded policy and the authenticated controller. It also
// reopens the owner-only config to detect drift since service startup.
func (g *CatalogMaintenanceProbeGuard) ValidateFixedProbeRequestForController(environment, targetKind, targetProfile, sourceMode, script string,
	timeoutSeconds int64, controllerType domain.ControllerType, controllerID string) error {
	if g == nil {
		return ErrCatalogProbeAdmission
	}
	if g.configuration.SourceSHA256() == "" {
		if _, err := domain.NewControllerIdentity(controllerType, domain.ControllerID(controllerID)); err != nil ||
			!catalogProbeControllerAllowed(g.transport, controllerType) {
			return ErrCatalogProbeAdmission
		}
		return g.ValidateFixedProbeRequest(environment, targetKind, targetProfile, sourceMode, script, timeoutSeconds)
	}
	current, err := config.LoadFile(g.plan.Namespace.ConfigFile)
	if err != nil || current.SourceSHA256() != g.configuration.SourceSHA256() {
		return ErrCatalogProbeAdmission
	}
	binding, err := ResolveCatalogMaintenanceProbeBinding(current, g.plan, g.transport, controllerType, controllerID)
	if err != nil || binding.ConfigurationSHA != g.configuration.SourceSHA256() || environment != binding.Environment ||
		targetKind != binding.TargetKind || targetProfile != binding.TargetProfile || sourceMode != "empty" ||
		script != "printf 'catalog release maintenance probe\\n'" || timeoutSeconds != 5 {
		return ErrCatalogProbeAdmission
	}
	return nil
}

// CatalogCohortAdmissionHeld reports whether a durable local cohort receipt
// requires this lane to remain fenced. A corrupt or ambiguous receipt is an
// error so a service can fail closed during startup.
func CatalogCohortAdmissionHeld(plan Plan) (bool, string, error) {
	if validateCatalogPlan(plan) != nil {
		return false, "", ErrCatalogOperationInvalid
	}
	root := filepath.Join(plan.Namespace.Staging, "catalog-cohorts")
	return catalogCohortAdmissionHeldAt(plan, root)
}

// CatalogCohortAdmissionHoldMatches requires exactly one valid admission hold
// and requires that hold to belong to the expected cohort operation. Callers
// use it immediately before a selector mutation so an unrelated retained hold
// cannot remain undiscovered until a service restart.
func CatalogCohortAdmissionHoldMatches(plan Plan, expectedOperationID string) error {
	if validateCatalogPlan(plan) != nil {
		return ErrCatalogOperationInvalid
	}
	return catalogCohortAdmissionHoldMatchesAt(plan, filepath.Join(plan.Namespace.Staging, "catalog-cohorts"), expectedOperationID)
}

func catalogCohortAdmissionHoldMatchesAt(plan Plan, root, expectedOperationID string) error {
	if !validCatalogOperationID(expectedOperationID) {
		return ErrCatalogOperationInvalid
	}
	held, operationID, err := catalogCohortAdmissionHeldAt(plan, root)
	if err != nil || !held || operationID != expectedOperationID {
		return ErrCatalogOperationInvalid
	}
	return nil
}

func catalogCohortAdmissionHeldAt(plan Plan, root string) (bool, string, error) {
	if validateCatalogPlan(plan) != nil || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return false, "", ErrCatalogOperationInvalid
	}
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return false, "", nil
	} else if err != nil || validateExistingPrivateDirectory(root) != nil {
		return false, "", ErrCatalogOperationInvalid
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return false, "", err
	}
	var active []CatalogCohortReceipt
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		operationID := strings.TrimSuffix(entry.Name(), ".json")
		if !validCatalogOperationID(operationID) {
			return false, "", ErrCatalogOperationInvalid
		}
		receipt, readErr := readCatalogCohortReceipt(filepath.Join(root, operationID+".json"))
		if readErr != nil || receipt.Lane != plan.Lane || !cohortContainsHost(receipt, plan.HostID) {
			return false, "", ErrCatalogOperationInvalid
		}
		if catalogCohortStageHoldsAdmissions(receipt.Stage) {
			active = append(active, receipt)
		}
	}
	if len(active) == 0 {
		return false, "", nil
	}
	if len(active) == 1 {
		return true, active[0].OperationID, nil
	}
	if len(active) == 2 {
		for _, predecessor := range active {
			for _, successor := range active {
				if catalogCohortCompletedSupersessionHoldPair(plan, predecessor, successor, time.Now().UTC()) ||
					catalogCohortReplacementHoldPair(predecessor, successor, time.Now().UTC()) {
					return true, successor.OperationID, nil
				}
			}
		}
	}
	return false, "", ErrCatalogOperationInvalid
}

// catalogCohortCompletedSupersessionHoldPair recognizes the brief, guarded
// overlap between one completed predecessor hold and its fresh held
// successor. The predecessor continues to hold admissions until supported
// per-host evidence has been synchronized and its supersede transition lands.
func catalogCohortCompletedSupersessionHoldPair(plan Plan, predecessor, successor CatalogCohortReceipt, now time.Time) bool {
	if validateCatalogPlan(plan) != nil || ValidateCatalogCohortReceipt(predecessor) != nil ||
		ValidateCatalogCohortReceipt(successor) != nil || predecessor.Lane != plan.Lane || successor.Lane != plan.Lane ||
		predecessor.OperationID == successor.OperationID || predecessor.Stage != CatalogCohortComplete ||
		successor.Stage != CatalogCohortHeld || predecessor.ReleaseID == successor.ReleaseID ||
		predecessor.ReleaseSHA256 == successor.ReleaseSHA256 ||
		!catalogCohortReceiptSupersessionEligible(predecessor) ||
		!cohortEvidenceFresh(successor, now.UTC().Format(time.RFC3339Nano)) {
		return false
	}
	currentSuccessor := currentSupersessionSuccessor(predecessor.Transitions)
	if currentSuccessor != "" && currentSuccessor != successor.OperationID {
		return false
	}
	index := cohortHostIndex(predecessor, plan.HostID)
	if index < 0 || index != cohortHostIndex(successor, plan.HostID) {
		return false
	}
	oldHost, nextHost := predecessor.Hosts[index], successor.Hosts[index]
	root, err := DefaultCatalogRootForPlan(plan)
	if err != nil || oldHost.Target.ReleaseID != predecessor.ReleaseID ||
		oldHost.Target.ReleaseSHA256 != predecessor.ReleaseSHA256 ||
		nextHost.Target.ReleaseID != successor.ReleaseID || nextHost.Target.ReleaseSHA256 != successor.ReleaseSHA256 ||
		nextHost.PreviousTarget != filepath.Join(root, predecessor.ReleaseID, plan.HostID) ||
		nextHost.PreviousBundleSHA256 != oldHost.Target.BundleSHA256 ||
		nextHost.ConfigurationSHA256 != oldHost.ConfigurationSHA256 || nextHost.SchemaVersion != oldHost.SchemaVersion ||
		nextHost.WorkInventoryEmpty != true {
		return false
	}
	for _, transition := range predecessor.Transitions {
		if transition.Action == "supersede-host" && transition.Supersession != nil &&
			transition.Supersession.SuccessorOperationID != successor.OperationID {
			return false
		}
	}
	return true
}

func catalogCohortReplacementHoldPair(predecessor, successor CatalogCohortReceipt, now time.Time) bool {
	if ValidateCatalogCohortReceipt(predecessor) != nil || ValidateCatalogCohortReceipt(successor) != nil ||
		predecessor.Lane != config.ReleaseLanePRD || successor.Lane != config.ReleaseLanePRD ||
		predecessor.OperationID == successor.OperationID || predecessor.ReleaseID == successor.ReleaseID ||
		predecessor.ReleaseSHA256 == successor.ReleaseSHA256 || predecessor.Stage != CatalogCohortVerifying ||
		successor.Stage != CatalogCohortHeld || !catalogCohortReceiptSupersessionEligible(predecessor) ||
		!cohortEvidenceFresh(successor, now.Format(time.RFC3339Nano)) ||
		(currentSupersessionSuccessor(predecessor.Transitions) != "" && currentSupersessionSuccessor(predecessor.Transitions) != successor.OperationID) {
		return false
	}
	for index, hostID := range catalogCohortSwitchOrder {
		oldHost, nextHost := predecessor.Hosts[index], successor.Hosts[index]
		if oldHost.HostID != hostID || nextHost.HostID != hostID || !cohortHasSwitched(predecessor, hostID) ||
			nextHost.ServiceActiveAtPreflight ||
			nextHost.PreviousBundleSHA256 != oldHost.Target.BundleSHA256 || nextHost.ConfigurationSHA256 != oldHost.ConfigurationSHA256 ||
			nextHost.SchemaVersion != oldHost.SchemaVersion || nextHost.WorkInventorySHA256 != oldHost.WorkInventorySHA256 ||
			!nextHost.WorkInventoryEmpty || nextHost.BackupDirectory == "" || !bundleDigestPattern.MatchString(nextHost.BackupManifestSHA256) {
			return false
		}
		plan, err := BuildCatalogPlan(hostID, config.ReleaseLanePRD)
		if err != nil {
			return false
		}
		root, err := DefaultCatalogRootForPlan(plan)
		if err != nil || nextHost.PreviousTarget != filepath.Join(root, predecessor.ReleaseID, hostID) {
			return false
		}
	}
	return true
}

func catalogCohortStageHoldsAdmissions(stage CatalogCohortStage) bool {
	switch stage {
	case CatalogCohortHeld, CatalogCohortSwitching, CatalogCohortVerifying, CatalogCohortComplete,
		CatalogCohortBlocked, CatalogCohortRollingBack, CatalogCohortRolledBack:
		return true
	default:
		return false
	}
}

func NewCatalogMaintenanceProbeGuard(plan Plan, operationID string) (*CatalogMaintenanceProbeGuard, error) {
	return NewCatalogMaintenanceProbeGuardForTransport(plan, operationID, CatalogProbeDirectMTLS)
}

func NewCatalogMaintenanceProbeGuardForTransport(plan Plan, operationID string, transport CatalogProbeTransport) (*CatalogMaintenanceProbeGuard, error) {
	if validateCatalogPlan(plan) != nil || !validCatalogOperationID(operationID) || !validCatalogProbeTransport(plan, transport) {
		return nil, ErrCatalogProbeAdmission
	}
	return &CatalogMaintenanceProbeGuard{plan: plan, operationID: operationID, transport: transport}, nil
}

// NewCatalogMaintenanceProbeGuardForTransportWithConfig binds the guard to
// the exact configuration snapshot already loaded by the service.
func NewCatalogMaintenanceProbeGuardForTransportWithConfig(plan Plan, operationID string, transport CatalogProbeTransport,
	loaded config.Config) (*CatalogMaintenanceProbeGuard, error) {
	if validateCatalogPlan(plan) != nil || !validCatalogOperationID(operationID) {
		return nil, ErrCatalogProbeAdmission
	}
	if !validCatalogProbeTransport(plan, transport) || loaded.SourceSHA256() == "" || loaded.ReleaseLane() != plan.Lane {
		return nil, ErrCatalogProbeAdmission
	}
	return &CatalogMaintenanceProbeGuard{plan: plan, operationID: operationID, transport: transport, configuration: loaded}, nil
}

// CatalogMaintenanceProbeRequestBody returns the only command body that the
// bounded cohort maintenance scope may admit. It is deliberately fixed and
// harmless; caller supplied scripts are never accepted by the scope.
func CatalogMaintenanceProbeRequestBody(plan Plan) ([]byte, error) {
	return CatalogMaintenanceProbeRequestBodyForTransport(plan, CatalogProbeDirectMTLS)
}

func CatalogMaintenanceProbeRequestBodyForTransport(plan Plan, transport CatalogProbeTransport) ([]byte, error) {
	if validateCatalogPlan(plan) != nil || !validCatalogProbeTransport(plan, transport) ||
		(plan.Lane != config.ReleaseLaneDev && plan.Lane != config.ReleaseLaneQAS && plan.Lane != config.ReleaseLanePRD) {
		return nil, ErrCatalogProbeAdmission
	}
	request := catalogMaintenanceProbeRequest{
		Environment: string(plan.HostID) + "-" + string(plan.Lane),
		Script:      "printf 'catalog release maintenance probe\\n'",
	}
	request.Target.Kind = "remote"
	request.Target.Profile = plan.HostID
	request.Source.Mode = "empty"
	switch transport {
	case CatalogProbeQueuedSSH:
		request.Limits = map[string]int{"command_timeout_seconds": 5}
	case CatalogProbeMacLocal:
		request.Environment = "mac-workstation-" + string(plan.Lane)
		request.Target.Kind = "local"
		request.Target.Profile = "mac-workstation"
		request.TimeoutSeconds = 5
	default:
		request.TimeoutSeconds = 5
	}
	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	return body, nil
}

// CatalogMaintenanceProbeRequestBodyForBinding creates the fixed harmless
// request with the exact environment resolved from the host configuration.
func CatalogMaintenanceProbeRequestBodyForBinding(plan Plan, transport CatalogProbeTransport,
	binding CatalogMaintenanceProbeBinding) ([]byte, error) {
	if validateCatalogPlan(plan) != nil || !validCatalogProbeTransport(plan, transport) || binding.Environment == "" ||
		!bundleDigestPattern.MatchString(binding.ConfigurationSHA) {
		return nil, ErrCatalogProbeAdmission
	}
	body, err := CatalogMaintenanceProbeRequestBodyForTransport(plan, transport)
	if err != nil {
		return nil, err
	}
	var request catalogMaintenanceProbeRequest
	if json.Unmarshal(body, &request) != nil {
		return nil, ErrCatalogProbeAdmission
	}
	request.Environment = binding.Environment
	return json.Marshal(request)
}

// PrepareCatalogMaintenanceProbe creates an owner-only scope and returns its
// one-time bearer token. The request body itself is not stored; only its hash,
// exact idempotency key, authenticated controller and operation identity are
// retained. The selected host must already be in whole-cohort verification.
func PrepareCatalogMaintenanceProbe(plan Plan, operationID, controllerID, idempotencyKey string, requestBody []byte, expiresAt time.Time) (string, error) {
	return PrepareCatalogMaintenanceProbeForTransport(plan, operationID, CatalogProbeDirectMTLS, domain.ControllerTypeDirectMTLS, controllerID, idempotencyKey, requestBody, expiresAt)
}

func PrepareCatalogMaintenanceProbeForTransport(plan Plan, operationID string, transport CatalogProbeTransport, controllerType domain.ControllerType, controllerID, idempotencyKey string, requestBody []byte, expiresAt time.Time) (string, error) {
	loaded, loadErr := config.LoadFile(plan.Namespace.ConfigFile)
	if loadErr != nil {
		return "", ErrCatalogProbeAdmission
	}
	return PrepareCatalogMaintenanceProbeForTransportWithConfig(plan, operationID, transport, controllerType, controllerID,
		idempotencyKey, requestBody, expiresAt, loaded)
}

// PrepareCatalogMaintenanceProbeForTransportWithConfig creates a capability
// bound to the caller's exact validated configuration snapshot.
func PrepareCatalogMaintenanceProbeForTransportWithConfig(plan Plan, operationID string, transport CatalogProbeTransport,
	controllerType domain.ControllerType, controllerID, idempotencyKey string, requestBody []byte, expiresAt time.Time,
	loaded config.Config) (string, error) {
	guard, err := NewCatalogMaintenanceProbeGuardForTransportWithConfig(plan, operationID, transport, loaded)
	binding, bindingErr := ResolveCatalogMaintenanceProbeBinding(loaded, plan, transport, controllerType, controllerID)
	wantBody, bodyErr := CatalogMaintenanceProbeRequestBodyForBinding(plan, transport, binding)
	wantKey := catalogProbeIdempotencyKey(operationID, plan.HostID, transport)
	if err != nil || bindingErr != nil || bodyErr != nil || !bytes.Equal(requestBody, wantBody) || idempotencyKey != wantKey || expiresAt.IsZero() {
		return "", ErrCatalogProbeAdmission
	}
	if !catalogProbeControllerAllowed(transport, controllerType) {
		return "", ErrCatalogProbeAdmission
	}
	if _, err := domain.NewControllerIdentity(controllerType, domain.ControllerID(controllerID)); err != nil {
		return "", ErrCatalogProbeAdmission
	}
	if err := guard.validateCurrentCohortForProbe(); err != nil {
		return "", err
	}
	now := time.Now().UTC()
	expiresAt = expiresAt.UTC()
	if !expiresAt.After(now) || expiresAt.Sub(now) > 10*time.Minute {
		return "", ErrCatalogProbeAdmission
	}
	var tokenBytes [32]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes[:])
	tokenDigest := sha256.Sum256([]byte(token))
	bodyDigest := sha256.Sum256(requestBody)
	scope := catalogProbeScope{
		FormatVersion: catalogProbeScopeFormat, OperationID: operationID, HostID: plan.HostID, Lane: plan.Lane,
		Transport: transport, ControllerType: controllerType, ControllerID: controllerID,
		Environment: binding.Environment, ConfigurationSHA: binding.ConfigurationSHA,
		CreatedAt: now.Format(time.RFC3339Nano), ExpiresAt: expiresAt.Format(time.RFC3339Nano),
		TokenSHA256: hex.EncodeToString(tokenDigest[:]), IdempotencyKey: idempotencyKey,
		RequestBodySHA256: hex.EncodeToString(bodyDigest[:]), State: "ready",
	}
	paths, err := guard.paths()
	if err != nil {
		return "", err
	}
	root := paths.scopeRoot
	if err := ensureCatalogPrivateDirectory(root); err != nil {
		return "", err
	}
	lock, err := acquireCohortReceiptLock(root)
	if err != nil {
		return "", err
	}
	defer releaseCatalogRootPublicationLock(lock)
	path := guard.scopePath(root)
	if _, err := os.Lstat(path); err == nil || !errors.Is(err, os.ErrNotExist) {
		return "", ErrCatalogProbeAdmission
	}
	if err := writeCatalogProbeScope(path, root, scope); err != nil {
		return "", err
	}
	return token, nil
}

// ReserveCatalogMaintenanceProbe consumes the sole mutation opportunity
// before the request can enter execution. A crash after reservation remains
// fail-closed; the request is never silently made available for a second job.
func (g *CatalogMaintenanceProbeGuard) ReserveCatalogMaintenanceProbe(controllerID, token, idempotencyKey string, body []byte, now time.Time) error {
	return g.ReserveCatalogMaintenanceProbeForController(domain.ControllerTypeDirectMTLS, controllerID, token, idempotencyKey, body, now)
}

func (g *CatalogMaintenanceProbeGuard) ReserveCatalogMaintenanceProbeForController(controllerType domain.ControllerType, controllerID, token, idempotencyKey string, body []byte, now time.Time) error {
	if g == nil || now.IsZero() || len(body) == 0 || len(body) > 1<<20 {
		return ErrCatalogProbeAdmission
	}
	if !catalogProbeControllerAllowed(g.transport, controllerType) {
		return ErrCatalogProbeAdmission
	}
	var request catalogMaintenanceProbeRequest
	if json.Unmarshal(body, &request) != nil || g.ValidateFixedProbeRequestForController(request.Environment,
		request.Target.Kind, request.Target.Profile, request.Source.Mode, request.Script,
		int64(request.TimeoutSeconds), controllerType, controllerID) != nil {
		return ErrCatalogProbeAdmission
	}
	bodyDigest := sha256.Sum256(body)
	return g.reserveDigest(controllerType, controllerID, token, idempotencyKey, hex.EncodeToString(bodyDigest[:]), now)
}

func (g *CatalogMaintenanceProbeGuard) ReserveCatalogMaintenanceProbeDigest(controllerType domain.ControllerType, controllerID, token, idempotencyKey, requestDigest string, now time.Time) error {
	if g == nil || now.IsZero() || !bundleDigestPattern.MatchString(requestDigest) || !catalogProbeControllerAllowed(g.transport, controllerType) {
		return ErrCatalogProbeAdmission
	}
	return g.reserveDigest(controllerType, controllerID, token, idempotencyKey, requestDigest, now)
}

func (g *CatalogMaintenanceProbeGuard) reserveDigest(controllerType domain.ControllerType, controllerID, token, idempotencyKey, requestDigest string, now time.Time) error {
	if err := g.validateCurrentCohortForProbe(); err != nil {
		return err
	}
	paths, err := g.paths()
	if err != nil {
		return ErrCatalogProbeAdmission
	}
	root := paths.scopeRoot
	lock, err := acquireCohortReceiptLock(root)
	if err != nil {
		return ErrCatalogProbeAdmission
	}
	defer releaseCatalogRootPublicationLock(lock)
	path := g.scopePath(root)
	scope, err := readCatalogProbeScope(path)
	if err != nil || scope.OperationID != g.operationID || scope.HostID != g.plan.HostID || scope.Lane != g.plan.Lane ||
		scope.Transport != g.transport || scope.ControllerType != controllerType || scope.ControllerID != controllerID ||
		scope.IdempotencyKey != idempotencyKey || scope.State != "ready" {
		return ErrCatalogProbeAdmission
	}
	if err := g.validateScopeConfiguration(scope, controllerType, controllerID); err != nil {
		return ErrCatalogProbeAdmission
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, scope.ExpiresAt)
	if err != nil || !now.UTC().Before(expiresAt) {
		return ErrCatalogProbeAdmission
	}
	tokenDigest := sha256.Sum256([]byte(token))
	if !constantDigestEqual(scope.TokenSHA256, hex.EncodeToString(tokenDigest[:])) ||
		!constantDigestEqual(scope.RequestBodySHA256, requestDigest) {
		return ErrCatalogProbeAdmission
	}
	scope.State = "reserved"
	return writeCatalogProbeScope(path, root, scope)
}

func (g *CatalogMaintenanceProbeGuard) validateScopeConfiguration(scope catalogProbeScope, controllerType domain.ControllerType, controllerID string) error {
	paths, err := g.paths()
	if err != nil {
		return ErrCatalogProbeAdmission
	}
	receipt, err := readCatalogCohortReceipt(filepath.Join(paths.cohortRoot, g.operationID+".json"))
	if err != nil {
		return ErrCatalogProbeAdmission
	}
	index := cohortHostIndex(receipt, g.plan.HostID)
	if index < 0 || scope.ConfigurationSHA != receipt.Hosts[index].ConfigurationSHA256 {
		return ErrCatalogProbeAdmission
	}
	if g.configuration.SourceSHA256() == "" {
		return nil // Isolated state-machine tests use a synthetic config digest.
	}
	current, err := config.LoadFile(g.plan.Namespace.ConfigFile)
	if err != nil || current.SourceSHA256() != g.configuration.SourceSHA256() {
		return ErrCatalogProbeAdmission
	}
	binding, err := ResolveCatalogMaintenanceProbeBinding(current, g.plan, g.transport, controllerType, controllerID)
	if err != nil || validateProbeScopeBinding(scope, binding) != nil {
		return ErrCatalogProbeAdmission
	}
	return g.validateConfigurationAgainstCohort(current)
}

func validateProbeScopeBinding(scope catalogProbeScope, binding CatalogMaintenanceProbeBinding) error {
	if scope.ConfigurationSHA != binding.ConfigurationSHA || scope.Environment != binding.Environment {
		return ErrCatalogProbeAdmission
	}
	return nil
}

// BindCatalogMaintenanceProbeJob records the generated job identity before
// the durable acceptance call. If the process exits between acceptance and
// the final receipt update, the exact job remains readable and no new job can
// be admitted.
func (g *CatalogMaintenanceProbeGuard) BindCatalogMaintenanceProbeJob(jobID string) error {
	if g == nil || !validProbeJobID(jobID) {
		return ErrCatalogProbeAdmission
	}
	return g.updateScope(func(scope *catalogProbeScope) error {
		if scope.State != "reserved" || scope.JobID != "" {
			return ErrCatalogProbeAdmission
		}
		scope.JobID = jobID
		return nil
	})
}

func (g *CatalogMaintenanceProbeGuard) MarkCatalogMaintenanceProbeAccepted(jobID string) error {
	if g == nil || !validProbeJobID(jobID) {
		return ErrCatalogProbeAdmission
	}
	return g.updateScope(func(scope *catalogProbeScope) error {
		if scope.State != "reserved" || scope.JobID != jobID {
			return ErrCatalogProbeAdmission
		}
		scope.State = "accepted"
		return nil
	})
}

func (g *CatalogMaintenanceProbeGuard) BindCatalogMaintenanceProbeIntent(jobID, intentID string) error {
	if g == nil || !validProbeJobID(jobID) || strings.TrimSpace(intentID) == "" || len(intentID) > 128 || strings.IndexByte(intentID, 0) >= 0 {
		return ErrCatalogProbeAdmission
	}
	return g.updateScope(func(scope *catalogProbeScope) error {
		if scope.State != "reserved" || scope.JobID != jobID || scope.IntentID != "" {
			return ErrCatalogProbeAdmission
		}
		scope.IntentID = intentID
		return nil
	})
}

func (g *CatalogMaintenanceProbeGuard) AllowsReservedIntent(controllerType domain.ControllerType, controllerID, token, intentID string, now time.Time) bool {
	return g.allowsReservedIntent(controllerType, controllerID, token, intentID, "", false, now)
}

// AllowsReservedJobIntent additionally binds a Mac-local maintenance intent
// to the exact job reserved by the probe scope. This prevents another local
// intent from reusing the token after the one permitted job was created.
func (g *CatalogMaintenanceProbeGuard) AllowsReservedJobIntent(controllerType domain.ControllerType, controllerID, token, intentID, jobID string, now time.Time) bool {
	if !validProbeJobID(jobID) {
		return false
	}
	return g.allowsReservedIntent(controllerType, controllerID, token, intentID, jobID, true, now)
}

func (g *CatalogMaintenanceProbeGuard) allowsReservedIntent(controllerType domain.ControllerType, controllerID, token, intentID, jobID string, requireJob bool, now time.Time) bool {
	if g == nil || controllerID == "" || token == "" || intentID == "" || now.IsZero() || g.validateCurrentCohortForProbe() != nil {
		return false
	}
	paths, pathErr := g.paths()
	if pathErr != nil {
		return false
	}
	scope, err := readCatalogProbeScope(g.scopePath(paths.scopeRoot))
	if err != nil || scope.Transport != g.transport || scope.ControllerType != controllerType || scope.ControllerID != controllerID ||
		scope.IntentID != intentID || scope.State != "reserved" || (requireJob && scope.JobID != jobID) {
		return false
	}
	tokenDigest := sha256.Sum256([]byte(token))
	if !constantDigestEqual(scope.TokenSHA256, hex.EncodeToString(tokenDigest[:])) {
		return false
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, scope.ExpiresAt)
	return err == nil && now.UTC().Before(expiresAt)
}

func (g *CatalogMaintenanceProbeGuard) AllowsJobRead(controllerID, jobID string, now time.Time) bool {
	return g.allowsJobRead(controllerID, "", jobID, now, false)
}

func (g *CatalogMaintenanceProbeGuard) AllowsJobReadWithToken(controllerID, token, jobID string, now time.Time) bool {
	return g.allowsJobRead(controllerID, token, jobID, now, true)
}

func (g *CatalogMaintenanceProbeGuard) allowsJobRead(controllerID, token, jobID string, now time.Time, requireToken bool) bool {
	if g == nil || !validProbeJobID(jobID) || now.IsZero() || g.validateCurrentCohortForProbe() != nil {
		return false
	}
	paths, pathErr := g.paths()
	if pathErr != nil {
		return false
	}
	scope, err := readCatalogProbeScope(g.scopePath(paths.scopeRoot))
	if err != nil || scope.Transport != g.transport || scope.ControllerID != controllerID || scope.JobID != jobID || (scope.State != "reserved" && scope.State != "accepted") {
		return false
	}
	if requireToken {
		tokenDigest := sha256.Sum256([]byte(token))
		if token == "" || !constantDigestEqual(scope.TokenSHA256, hex.EncodeToString(tokenDigest[:])) {
			return false
		}
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, scope.ExpiresAt)
	return err == nil && now.UTC().Before(expiresAt)
}

func (g *CatalogMaintenanceProbeGuard) updateScope(update func(*catalogProbeScope) error) error {
	if err := g.validateCurrentCohortForProbe(); err != nil {
		return err
	}
	paths, err := g.paths()
	if err != nil {
		return ErrCatalogProbeAdmission
	}
	root := paths.scopeRoot
	lock, err := acquireCohortReceiptLock(root)
	if err != nil {
		return ErrCatalogProbeAdmission
	}
	defer releaseCatalogRootPublicationLock(lock)
	path := g.scopePath(root)
	scope, err := readCatalogProbeScope(path)
	if err != nil || scope.OperationID != g.operationID || scope.HostID != g.plan.HostID || scope.Lane != g.plan.Lane {
		return ErrCatalogProbeAdmission
	}
	if err := update(&scope); err != nil {
		return err
	}
	return writeCatalogProbeScope(path, root, scope)
}

func (g *CatalogMaintenanceProbeGuard) validateCurrentCohortForProbe() error {
	if g == nil || validateCatalogPlan(g.plan) != nil {
		return ErrCatalogProbeAdmission
	}
	paths, err := g.paths()
	if err != nil {
		return ErrCatalogProbeAdmission
	}
	receipt, err := readCatalogCohortReceipt(filepath.Join(paths.cohortRoot, g.operationID+".json"))
	if err != nil || receipt.Stage != CatalogCohortVerifying {
		return ErrCatalogProbeAdmission
	}
	if receipt.Lane != g.plan.Lane || !cohortContainsHost(receipt, g.plan.HostID) {
		return ErrCatalogProbeAdmission
	}
	if g.configuration.SourceSHA256() != "" && g.validateConfigurationAgainstCohort(g.configuration) != nil {
		return ErrCatalogProbeAdmission
	}
	status, err := inspectCatalogSelector(paths.layout)
	if err != nil || status.Kind != "catalog" || status.Release == nil || status.Release.Identity.ReleaseID != receipt.ReleaseID ||
		status.Release.Identity.ReleaseSHA256 != receipt.ReleaseSHA256 {
		return ErrCatalogProbeAdmission
	}
	return nil
}

func (g *CatalogMaintenanceProbeGuard) validateConfigurationAgainstCohort(loaded config.Config) error {
	if g == nil || loaded.SourceSHA256() == "" {
		return ErrCatalogProbeAdmission
	}
	paths, err := g.paths()
	if err != nil {
		return ErrCatalogProbeAdmission
	}
	receipt, err := readCatalogCohortReceipt(filepath.Join(paths.cohortRoot, g.operationID+".json"))
	if err != nil || receipt.Lane != g.plan.Lane {
		return ErrCatalogProbeAdmission
	}
	index := cohortHostIndex(receipt, g.plan.HostID)
	if index < 0 || receipt.Hosts[index].ConfigurationSHA256 != loaded.SourceSHA256() {
		return ErrCatalogProbeAdmission
	}
	return nil
}

func (g *CatalogMaintenanceProbeGuard) paths() (catalogProbeTestPaths, error) {
	if g == nil || validateCatalogPlan(g.plan) != nil {
		return catalogProbeTestPaths{}, ErrCatalogProbeAdmission
	}
	if g.testPaths != nil {
		paths := *g.testPaths
		if !filepath.IsAbs(paths.cohortRoot) || filepath.Clean(paths.cohortRoot) != paths.cohortRoot ||
			!filepath.IsAbs(paths.scopeRoot) || filepath.Clean(paths.scopeRoot) != paths.scopeRoot || validateCatalogSelectorLayout(paths.layout) != nil ||
			paths.layout.hostID != g.plan.HostID || paths.layout.lane != g.plan.Lane {
			return catalogProbeTestPaths{}, ErrCatalogProbeAdmission
		}
		return paths, nil
	}
	root, err := DefaultCatalogRootForPlan(g.plan)
	if err != nil {
		return catalogProbeTestPaths{}, err
	}
	layout, err := catalogSelectorLayoutForPlan(g.plan, root)
	if err != nil {
		return catalogProbeTestPaths{}, err
	}
	return catalogProbeTestPaths{
		cohortRoot: filepath.Join(g.plan.Namespace.Staging, "catalog-cohorts"),
		scopeRoot:  filepath.Join(g.plan.Namespace.Staging, "catalog-probes"), layout: layout,
	}, nil
}

func (g *CatalogMaintenanceProbeGuard) scopePath(root string) string {
	name := g.operationID + ".json"
	if g.transport != "" && g.transport != CatalogProbeDirectMTLS {
		name = g.operationID + "-" + string(g.transport) + ".json"
	}
	return filepath.Join(root, name)
}

func catalogProbeIdempotencyKey(operationID, hostID string, transport CatalogProbeTransport) string {
	key := "catalog-probe-" + operationID + "-" + hostID
	if transport != "" && transport != CatalogProbeDirectMTLS {
		key += "-" + string(transport)
	}
	return key
}

func validCatalogProbeTransport(plan Plan, transport CatalogProbeTransport) bool {
	switch transport {
	case CatalogProbeDirectMTLS, CatalogProbeQueuedSSH:
		return plan.Namespace.Host == config.HostKindLinux && plan.HostID != "mac-control-plane"
	case CatalogProbeMacLocal:
		return plan.Namespace.Host == config.HostKindMac && plan.HostID == "mac-control-plane"
	default:
		return false
	}
}

func catalogProbeControllerAllowed(transport CatalogProbeTransport, controllerType domain.ControllerType) bool {
	switch transport {
	case CatalogProbeDirectMTLS:
		return controllerType == domain.ControllerTypeDirectMTLS
	case CatalogProbeQueuedSSH:
		return controllerType == domain.ControllerTypeQueuedMac
	case CatalogProbeMacLocal:
		return controllerType == domain.ControllerTypeLocalUser
	default:
		return false
	}
}

func validProbeJobID(jobID string) bool {
	return strings.HasPrefix(jobID, "job-") && len(jobID) == 36 && strings.Trim(jobID[4:], "0123456789abcdef") == ""
}

func constantDigestEqual(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	return subtleDigestCompare(left, right)
}

func subtleDigestCompare(left, right string) bool {
	var difference byte
	for index := range left {
		difference |= left[index] ^ right[index]
	}
	return difference == 0
}

func writeCatalogProbeScope(path, root string, scope catalogProbeScope) error {
	if validateCatalogProbeScope(scope) != nil || filepath.Dir(path) != root {
		return ErrCatalogProbeAdmission
	}
	data, err := json.MarshalIndent(scope, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(root, ".probe-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	return syncSimpleDirectory(root)
}

func readCatalogProbeScope(path string) (catalogProbeScope, error) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > 1<<16 || !catalogFileHasSingleLink(info) {
		return catalogProbeScope{}, ErrCatalogProbeAdmission
	}
	var stat unix.Stat_t
	if unix.Lstat(path, &stat) != nil || stat.Uid != uint32(os.Getuid()) {
		return catalogProbeScope{}, ErrCatalogProbeAdmission
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return catalogProbeScope{}, err
	}
	var scope catalogProbeScope
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&scope) != nil || decoder.Decode(&struct{}{}) != io.EOF || validateCatalogProbeScope(scope) != nil {
		return catalogProbeScope{}, ErrCatalogProbeAdmission
	}
	canonical, err := json.MarshalIndent(scope, "", "  ")
	if err != nil || !bytes.Equal(data, append(canonical, '\n')) {
		return catalogProbeScope{}, ErrCatalogProbeAdmission
	}
	return scope, nil
}

func validateCatalogProbeScope(scope catalogProbeScope) error {
	createdAt, createdErr := time.Parse(time.RFC3339Nano, scope.CreatedAt)
	expiresAt, expiresErr := time.Parse(time.RFC3339Nano, scope.ExpiresAt)
	if scope.FormatVersion != catalogProbeScopeFormat || !validCatalogOperationID(scope.OperationID) || !containsCatalogHost(scope.HostID) ||
		(scope.Lane != config.ReleaseLaneDev && scope.Lane != config.ReleaseLaneQAS && scope.Lane != config.ReleaseLanePRD) || scope.ControllerID == "" ||
		scope.Environment == "" || !probeEnvironmentName.MatchString(scope.Environment) || !bundleDigestPattern.MatchString(scope.ConfigurationSHA) ||
		(scope.Transport != CatalogProbeDirectMTLS && scope.Transport != CatalogProbeQueuedSSH && scope.Transport != CatalogProbeMacLocal) ||
		!validCohortTimestamp(scope.CreatedAt) || !validCohortTimestamp(scope.ExpiresAt) || !bundleDigestPattern.MatchString(scope.TokenSHA256) ||
		createdErr != nil || expiresErr != nil || !expiresAt.After(createdAt) || expiresAt.Sub(createdAt) > 10*time.Minute ||
		!bundleDigestPattern.MatchString(scope.RequestBodySHA256) || scope.IdempotencyKey == "" || len(scope.IdempotencyKey) > 256 || strings.IndexByte(scope.IdempotencyKey, 0) >= 0 {
		return ErrCatalogProbeAdmission
	}
	if _, err := domain.NewControllerIdentity(scope.ControllerType, domain.ControllerID(scope.ControllerID)); err != nil ||
		!catalogProbeControllerAllowed(scope.Transport, scope.ControllerType) {
		return ErrCatalogProbeAdmission
	}
	switch scope.State {
	case "ready", "reserved", "accepted":
	default:
		return ErrCatalogProbeAdmission
	}
	if (scope.JobID == "") != (scope.State == "ready") && scope.State != "reserved" {
		return ErrCatalogProbeAdmission
	}
	if scope.IntentID != "" && (scope.Transport != CatalogProbeMacLocal || scope.State != "reserved" && scope.State != "accepted") {
		return ErrCatalogProbeAdmission
	}
	if scope.JobID != "" && !validProbeJobID(scope.JobID) {
		return ErrCatalogProbeAdmission
	}
	return nil
}
