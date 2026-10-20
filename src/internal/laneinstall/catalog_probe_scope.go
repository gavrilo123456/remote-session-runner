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
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
)

const catalogProbeScopeFormat = 1

var ErrCatalogProbeAdmission = errors.New("catalog maintenance probe admission refused")

type catalogMaintenanceProbeRequest struct {
	Environment string `json:"environment"`
	Target      struct {
		Kind    string `json:"kind"`
		Profile string `json:"profile"`
	} `json:"execution_target"`
	Source struct {
		Mode string `json:"mode"`
	} `json:"source"`
	Script         string `json:"script"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

type catalogProbeScope struct {
	FormatVersion     int                `json:"format_version"`
	OperationID       string             `json:"operation_id"`
	HostID            string             `json:"host_id"`
	Lane              config.ReleaseLane `json:"lane"`
	ControllerID      string             `json:"controller_id"`
	CreatedAt         string             `json:"created_at"`
	ExpiresAt         string             `json:"expires_at"`
	TokenSHA256       string             `json:"token_sha256"`
	IdempotencyKey    string             `json:"idempotency_key"`
	RequestBodySHA256 string             `json:"request_body_sha256"`
	State             string             `json:"state"`
	JobID             string             `json:"job_id,omitempty"`
}

// CatalogMaintenanceProbeGuard admits only one exact direct-mTLS one-off job
// while this host's selected cohort is in its verification stage. The
// ephemeral token supplements, and never replaces, the normal client
// certificate and controller mapping.
type CatalogMaintenanceProbeGuard struct {
	plan        Plan
	operationID string
	testPaths   *catalogProbeTestPaths
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
	activeID := ""
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
			if activeID != "" {
				return false, "", ErrCatalogOperationInvalid
			}
			activeID = operationID
		}
	}
	return activeID != "", activeID, nil
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
	if validateCatalogPlan(plan) != nil || !validCatalogOperationID(operationID) {
		return nil, ErrCatalogProbeAdmission
	}
	return &CatalogMaintenanceProbeGuard{plan: plan, operationID: operationID}, nil
}

// CatalogMaintenanceProbeRequestBody returns the only command body that the
// bounded cohort maintenance scope may admit. It is deliberately fixed and
// harmless; caller supplied scripts are never accepted by the scope.
func CatalogMaintenanceProbeRequestBody(plan Plan) ([]byte, error) {
	if validateCatalogPlan(plan) != nil || plan.Namespace.Host != config.HostKindLinux ||
		(plan.Lane != config.ReleaseLaneDev && plan.Lane != config.ReleaseLaneQAS && plan.Lane != config.ReleaseLanePRD) {
		return nil, ErrCatalogProbeAdmission
	}
	request := catalogMaintenanceProbeRequest{
		Environment: "linux-" + string(plan.Lane), Script: "printf 'catalog release maintenance probe\\n'", TimeoutSeconds: 5,
	}
	request.Target.Kind = "remote"
	request.Target.Profile = plan.HostID
	request.Source.Mode = "empty"
	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	return body, nil
}

// PrepareCatalogMaintenanceProbe creates an owner-only scope and returns its
// one-time bearer token. The request body itself is not stored; only its hash,
// exact idempotency key, authenticated controller and operation identity are
// retained. The selected host must already be in whole-cohort verification.
func PrepareCatalogMaintenanceProbe(plan Plan, operationID, controllerID, idempotencyKey string, requestBody []byte, expiresAt time.Time) (string, error) {
	guard, err := NewCatalogMaintenanceProbeGuard(plan, operationID)
	wantBody, bodyErr := CatalogMaintenanceProbeRequestBody(plan)
	wantKey := "catalog-probe-" + operationID + "-" + plan.HostID
	if err != nil || bodyErr != nil || !bytes.Equal(requestBody, wantBody) || idempotencyKey != wantKey || expiresAt.IsZero() {
		return "", ErrCatalogProbeAdmission
	}
	if _, err := domain.NewControllerIdentity(domain.ControllerTypeDirectMTLS, domain.ControllerID(controllerID)); err != nil {
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
		ControllerID: controllerID, CreatedAt: now.Format(time.RFC3339Nano), ExpiresAt: expiresAt.Format(time.RFC3339Nano),
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
	path := filepath.Join(root, operationID+".json")
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
	if g == nil || now.IsZero() || len(body) == 0 || len(body) > 1<<20 {
		return ErrCatalogProbeAdmission
	}
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
	scope, err := readCatalogProbeScope(filepath.Join(root, g.operationID+".json"))
	if err != nil || scope.OperationID != g.operationID || scope.HostID != g.plan.HostID || scope.Lane != g.plan.Lane ||
		scope.ControllerID != controllerID || scope.IdempotencyKey != idempotencyKey || scope.State != "ready" {
		return ErrCatalogProbeAdmission
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, scope.ExpiresAt)
	if err != nil || !now.UTC().Before(expiresAt) {
		return ErrCatalogProbeAdmission
	}
	tokenDigest := sha256.Sum256([]byte(token))
	bodyDigest := sha256.Sum256(body)
	if !constantDigestEqual(scope.TokenSHA256, hex.EncodeToString(tokenDigest[:])) ||
		!constantDigestEqual(scope.RequestBodySHA256, hex.EncodeToString(bodyDigest[:])) {
		return ErrCatalogProbeAdmission
	}
	scope.State = "reserved"
	return writeCatalogProbeScope(filepath.Join(root, g.operationID+".json"), root, scope)
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

func (g *CatalogMaintenanceProbeGuard) AllowsJobRead(controllerID, jobID string, now time.Time) bool {
	if g == nil || !validProbeJobID(jobID) || now.IsZero() || g.validateCurrentCohortForProbe() != nil {
		return false
	}
	paths, pathErr := g.paths()
	if pathErr != nil {
		return false
	}
	scope, err := readCatalogProbeScope(filepath.Join(paths.scopeRoot, g.operationID+".json"))
	if err != nil || scope.ControllerID != controllerID || scope.JobID != jobID || (scope.State != "reserved" && scope.State != "accepted") {
		return false
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
	path := filepath.Join(root, g.operationID+".json")
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
	status, err := inspectCatalogSelector(paths.layout)
	if err != nil || status.Kind != "catalog" || status.Release == nil || status.Release.Identity.ReleaseID != receipt.ReleaseID ||
		status.Release.Identity.ReleaseSHA256 != receipt.ReleaseSHA256 {
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
		!validCohortTimestamp(scope.CreatedAt) || !validCohortTimestamp(scope.ExpiresAt) || !bundleDigestPattern.MatchString(scope.TokenSHA256) ||
		createdErr != nil || expiresErr != nil || !expiresAt.After(createdAt) || expiresAt.Sub(createdAt) > 10*time.Minute ||
		!bundleDigestPattern.MatchString(scope.RequestBodySHA256) || scope.IdempotencyKey == "" || len(scope.IdempotencyKey) > 256 || strings.IndexByte(scope.IdempotencyKey, 0) >= 0 {
		return ErrCatalogProbeAdmission
	}
	if _, err := domain.NewControllerIdentity(domain.ControllerTypeDirectMTLS, domain.ControllerID(scope.ControllerID)); err != nil {
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
	if scope.JobID != "" && !validProbeJobID(scope.JobID) {
		return ErrCatalogProbeAdmission
	}
	return nil
}
