package execution

import (
	"context"
	"time"
)

// RunPolicyEnforcer makes the persisted command, idle, and lifetime limits
// active for one healthy executor process. It only settles durable work that
// already exists; it never accepts, claims, or executes a command body.
//
// The callback must use a fixed, sanitized message because EnforcePolicies
// errors can contain host implementation details.
func RunPolicyEnforcer(ctx context.Context, service *Service, interval time.Duration, reportFailure func(error)) {
	if ctx == nil || service == nil {
		return
	}
	if interval <= 0 {
		interval = time.Second
	}
	sweep := func() {
		if _, err := service.EnforcePolicies(ctx); err != nil && ctx.Err() == nil && reportFailure != nil {
			reportFailure(err)
		}
	}
	sweep()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweep()
		}
	}
}
