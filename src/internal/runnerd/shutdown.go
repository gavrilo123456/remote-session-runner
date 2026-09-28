package runnerd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"remote-session-runner/src/internal/audit"
	"remote-session-runner/src/internal/execution"
	"remote-session-runner/src/internal/lifecycle"
	"remote-session-runner/src/internal/store"
)

// linuxShutdownHooks adapts runnerd's two ingress paths and shared execution
// service to the process-independent shutdown coordinator.
type linuxShutdownHooks struct {
	private       *PrivateServer
	https         *DirectHTTPSServer
	service       *execution.Service
	authority     *store.AuthorityStore
	requestGate   *lifecycle.Gate
	dispatchGate  *lifecycle.Gate
	stopAcceptErr error
}

func (h *linuxShutdownHooks) StopAccepting() {
	if h == nil {
		return
	}
	if h.requestGate != nil {
		h.requestGate.Stop()
	}
	h.stopAcceptErr = errors.Join(h.private.StopAccepting(), h.https.StopAccepting())
}

func (h *linuxShutdownHooks) StopDispatch() {
	if h != nil && h.dispatchGate != nil {
		h.dispatchGate.Stop()
	}
}

func (h *linuxShutdownHooks) Drain(ctx context.Context) error {
	if h == nil || h.private == nil || h.https == nil || h.service == nil || h.authority == nil || h.requestGate == nil || h.dispatchGate == nil {
		return errors.New("runnerd shutdown is not configured")
	}
	if err := h.requestGate.Wait(ctx); err != nil {
		return errors.Join(h.stopAcceptErr, fmt.Errorf("drain accepted runnerd requests: %w", err))
	}
	if err := h.dispatchGate.Wait(ctx); err != nil {
		return errors.Join(h.stopAcceptErr, fmt.Errorf("drain accepted runnerd commands: %w", err))
	}
	return errors.Join(h.stopAcceptErr, h.closeActiveSessions(ctx))
}

func (h *linuxShutdownHooks) CancelRemaining(ctx context.Context) error {
	if h == nil || h.service == nil || h.authority == nil || h.requestGate == nil || h.dispatchGate == nil {
		return errors.New("runnerd shutdown is not configured")
	}
	closeErr := h.closeActiveSessions(ctx)
	requestErr := h.requestGate.Wait(ctx)
	dispatchErr := h.dispatchGate.Wait(ctx)
	return errors.Join(closeErr, requestErr, dispatchErr)
}

func (h *linuxShutdownHooks) Flush(ctx context.Context) error {
	if h == nil || h.authority == nil {
		return errors.New("runnerd shutdown authority is not configured")
	}
	if _, err := h.authority.ListAuditRecords(ctx, 1); err != nil {
		return fmt.Errorf("verify committed runnerd audit tail: %w", err)
	}
	if _, err := h.authority.ReadOperationalMetrics(ctx); err != nil {
		return fmt.Errorf("verify committed runnerd operational state: %w", err)
	}
	return nil
}

func (h *linuxShutdownHooks) CloseStreams(context.Context) error {
	if h == nil || h.private == nil || h.https == nil {
		return errors.New("runnerd shutdown listeners are not configured")
	}
	return errors.Join(h.private.CloseStreams(), h.https.CloseStreams())
}

func (h *linuxShutdownHooks) closeActiveSessions(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	sessions, err := h.authority.ListSessions(ctx)
	if err != nil {
		return fmt.Errorf("list runnerd sessions for shutdown: %w", err)
	}
	var closeErrors []error
	for _, session := range sessions {
		if session.State.IsTerminal() {
			continue
		}
		requestHash, err := directCloseRequestHash(session.SessionID, "graceful")
		if err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("hash shutdown close for session %s: %w", session.SessionID, err))
			continue
		}
		requestContext := audit.WithIngress(ctx, audit.IngressServiceLifecycle)
		_, err = h.service.CloseSession(requestContext, execution.CloseSessionRequest{
			SessionID: session.SessionID, Controller: session.Controller,
			IdempotencyKey: fmt.Sprintf("runnerd-shutdown-%d-%s", os.Getpid(), session.SessionID),
			RequestHash:    requestHash, Policy: "graceful", IdempotencyRetention: store.DefaultSessionIdempotencyRetention,
		})
		if err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("close runnerd session %s: %w", session.SessionID, err))
		}
	}
	return errors.Join(closeErrors...)
}

func admitRunnerRequest(gate *lifecycle.Gate, requestPath, method string, reject func()) (func(), bool) {
	if gate == nil {
		return func() {}, true
	}
	release, err := gate.Enter()
	if err != nil {
		if reject != nil {
			reject()
		}
		return func() {}, false
	}
	if method == "GET" && strings.HasSuffix(requestPath, "/events") {
		// Event followers are read-only and are closed only after the durable
		// tail is verified. They must not consume the bounded command drain.
		release()
		return func() {}, true
	}
	return release, true
}

func launchRunnerWork(gate *lifecycle.Gate, work func()) bool {
	if work == nil {
		return false
	}
	if gate == nil {
		go work()
		return true
	}
	release, err := gate.Enter()
	if err != nil {
		return false
	}
	go func() {
		defer release()
		work()
	}()
	return true
}

var _ lifecycle.Hooks = (*linuxShutdownHooks)(nil)
