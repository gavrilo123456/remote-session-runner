// Package catalogprobe defines the narrow maintenance-probe contract shared
// by ingress adapters and the catalog authority. It owns no runtime or
// installation behavior.
package catalogprobe

import (
	"errors"
	"time"

	"remote-session-runner/src/internal/domain"
)

const (
	TokenHeader         = "X-Runner-Catalog-Maintenance-Token"
	RequestDigestHeader = "X-Runner-Catalog-Maintenance-Request-SHA256"
)

var ErrAdmission = errors.New("catalog maintenance probe admission refused")

// Guard reserves and binds the exact catalog-authorized probe. Adapters must
// keep ordinary admission closed; implementing this interface does not grant
// authority to create or modify a probe scope.
type Guard interface {
	ValidateFixedProbeRequestForController(environment, targetKind, targetProfile, sourceMode, script string,
		timeoutSeconds int64, controllerType domain.ControllerType, controllerID string) error
	ReserveCatalogMaintenanceProbeForController(controllerType domain.ControllerType, controllerID, token, idempotencyKey string, body []byte, now time.Time) error
	BindCatalogMaintenanceProbeJob(jobID string) error
	BindCatalogMaintenanceProbeIntent(jobID, intentID string) error
	MarkCatalogMaintenanceProbeAccepted(jobID string) error
}
