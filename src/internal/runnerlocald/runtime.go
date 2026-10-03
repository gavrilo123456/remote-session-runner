package runnerlocald

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	hostruntime "remote-session-runner/src/internal/runtime"
	"remote-session-runner/src/internal/store"
)

// MacSessionRuntime adapts the shared execution service to the selected Mac
// account. It mirrors the Linux adapter shape; all authoritative state remains
// in the shared AuthorityStore.
type MacSessionRuntime struct {
	adapter   *hostruntime.MacProcessAdapter
	mu        sync.Mutex
	prepared  map[string]hostruntime.MacPrepared
	restartMu sync.Mutex
}

var _ execution.LostRuntimeRecoverer = (*MacSessionRuntime)(nil)

// ErrControlledRestartRehydration reports that the deliberately narrow
// controlled-restart runtime path cannot safely rebuild its one queued shell.
// The later durable restart-plan boundary decides whether that condition stays
// pending or becomes terminal; this runtime seam never executes the command.
var ErrControlledRestartRehydration = errors.New("controlled restart rehydration is unavailable")

// ControlledRestartRehydrator is the Mac-only runtime seam used by a future
// durable restart plan. It is intentionally separate from normal startup
// reconciliation: it builds a new shell and never reattaches the old one.
type ControlledRestartRehydrator interface {
	RebuildQueuedOneOff(context.Context, store.SessionRecord) (execution.RuntimeStarted, error)
}

var _ ControlledRestartRehydrator = (*MacSessionRuntime)(nil)

func NewMacSessionRuntime(adapter *hostruntime.MacProcessAdapter) (*MacSessionRuntime, error) {
	if adapter == nil {
		return nil, fmt.Errorf("%w: Mac adapter is nil", execution.ErrRuntimeUnavailable)
	}
	return &MacSessionRuntime{adapter: adapter, prepared: make(map[string]hostruntime.MacPrepared)}, nil
}

func (r *MacSessionRuntime) Prepare(ctx context.Context, request execution.RuntimePrepareRequest) (execution.RuntimePrepared, error) {
	if r == nil || r.adapter == nil {
		return execution.RuntimePrepared{}, execution.ErrRuntimeUnavailable
	}
	generation, err := newRuntimeGeneration()
	if err != nil {
		return execution.RuntimePrepared{}, err
	}
	prepared, err := r.adapter.PrepareSource(ctx, string(request.Session.SessionID), generation, hostruntime.MacSourceRequest{
		Mode:              hostruntime.MacSourceMode(request.Session.Source.Mode()),
		RepositoryAlias:   request.Session.Source.RepositoryAlias(),
		RequestedRevision: request.Session.Source.RequestedRevision(),
		Path:              request.Session.Source.Path(),
	})
	if err != nil {
		return execution.RuntimePrepared{}, err
	}
	r.mu.Lock()
	r.prepared[string(request.Session.SessionID)] = prepared
	r.mu.Unlock()
	return execution.RuntimePrepared{RuntimeGeneration: generation, ResolvedRevision: prepared.Source.ResolvedRevision}, nil
}

func (r *MacSessionRuntime) StartAgent(ctx context.Context, request execution.RuntimeStartRequest) (execution.RuntimeStarted, error) {
	if r == nil || r.adapter == nil {
		return execution.RuntimeStarted{}, execution.ErrRuntimeUnavailable
	}
	r.mu.Lock()
	prepared, ok := r.prepared[string(request.Session.SessionID)]
	r.mu.Unlock()
	if !ok || prepared.Generation != request.RuntimeGeneration || prepared.Generation != request.Prepared.RuntimeGeneration {
		return execution.RuntimeStarted{}, fmt.Errorf("%w: prepared runtime generation mismatch", execution.ErrRuntimeHandshake)
	}
	if err := r.adapter.StartAgent(context.WithoutCancel(ctx), prepared); err != nil {
		return execution.RuntimeStarted{}, err
	}
	return execution.RuntimeStarted{RuntimeGeneration: prepared.Generation}, nil
}

// RebuildQueuedOneOff replaces an already-gone local empty-source shell after
// a controlled executor restart. It requires the exact durable generation,
// reconciles the old process group without adopting it, and creates one fresh
// Bash process with the same durable generation. It deliberately accepts no
// command bytes and cannot execute the queued command.
//
// The caller must first prove the durable queued-one-off and restart-plan
// boundaries. This method only owns the Mac runtime part of that operation.
func (r *MacSessionRuntime) RebuildQueuedOneOff(ctx context.Context, session store.SessionRecord) (execution.RuntimeStarted, error) {
	if r == nil || r.adapter == nil {
		return execution.RuntimeStarted{}, execution.ErrRuntimeUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if session.SessionID == "" || session.RuntimeGeneration == "" || session.State != domain.SessionStateReady ||
		session.Target.Kind() != domain.TargetKindLocal || session.Target.Profile() == "" ||
		session.Source.Mode() != domain.SourceModeEmpty {
		return execution.RuntimeStarted{}, fmt.Errorf("%w: expected a ready local empty-source session with a durable runtime generation", ErrControlledRestartRehydration)
	}

	r.restartMu.Lock()
	defer r.restartMu.Unlock()

	sessionID := string(session.SessionID)
	r.mu.Lock()
	_, alreadyPrepared := r.prepared[sessionID]
	r.mu.Unlock()
	if alreadyPrepared {
		return execution.RuntimeStarted{}, fmt.Errorf("%w: replacement shell is already prepared", ErrControlledRestartRehydration)
	}

	reconciled, err := r.adapter.ReconcileExactSession(ctx, sessionID, session.RuntimeGeneration, 500*time.Millisecond)
	if err != nil {
		return execution.RuntimeStarted{}, fmt.Errorf("%w: reconcile prior Mac shell: %v", ErrControlledRestartRehydration, err)
	}
	if !reconciled.CleanupConfirmed || reconciled.Generation != session.RuntimeGeneration {
		return execution.RuntimeStarted{}, fmt.Errorf("%w: prior Mac shell cleanup is not proven for the durable generation", ErrControlledRestartRehydration)
	}

	prepared, err := r.adapter.Prepare(ctx, sessionID, session.RuntimeGeneration)
	if err != nil {
		return execution.RuntimeStarted{}, fmt.Errorf("%w: prepare replacement Mac shell: %v", ErrControlledRestartRehydration, err)
	}
	r.mu.Lock()
	r.prepared[sessionID] = prepared
	r.mu.Unlock()
	if err := r.adapter.StartExactReplacementAgent(context.WithoutCancel(ctx), prepared); err != nil {
		r.mu.Lock()
		delete(r.prepared, sessionID)
		r.mu.Unlock()
		discardErr := r.adapter.DiscardExactReplacementPreparation(sessionID, session.RuntimeGeneration)
		if discardErr != nil {
			return execution.RuntimeStarted{}, fmt.Errorf("%w: start replacement Mac shell: %v; discard replacement preparation: %v", ErrControlledRestartRehydration, err, discardErr)
		}
		return execution.RuntimeStarted{}, fmt.Errorf("%w: start replacement Mac shell: %v", ErrControlledRestartRehydration, err)
	}
	return execution.RuntimeStarted{RuntimeGeneration: prepared.Generation}, nil
}

func (r *MacSessionRuntime) Cleanup(_ context.Context, request execution.RuntimeCleanupRequest) error {
	if r == nil || r.adapter == nil {
		return execution.ErrRuntimeUnavailable
	}
	err := r.adapter.Cleanup(string(request.Session.SessionID))
	if err == nil {
		r.mu.Lock()
		delete(r.prepared, string(request.Session.SessionID))
		r.mu.Unlock()
	}
	return err
}

func (r *MacSessionRuntime) ExecuteCommand(ctx context.Context, request execution.RuntimeCommandRequest) (execution.RuntimeCommandResult, error) {
	return r.ExecuteCommandStream(ctx, request, nil)
}

func (r *MacSessionRuntime) ExecuteCommandStream(ctx context.Context, request execution.RuntimeCommandRequest, onOutput func(stream string, payload []byte) error) (execution.RuntimeCommandResult, error) {
	if r == nil || r.adapter == nil {
		return execution.RuntimeCommandResult{}, execution.ErrRuntimeUnavailable
	}
	shell, err := r.adapter.Shell(string(request.Session.SessionID))
	if err != nil {
		return execution.RuntimeCommandResult{}, err
	}
	result, err := shell.RunScriptWithOutput(ctx, string(request.Command.CommandID), request.Command.ScriptBytes, func(chunk hostruntime.OutputChunk) error {
		if onOutput == nil {
			return nil
		}
		return onOutput(string(chunk.Stream), chunk.Data)
	})
	if err != nil {
		return execution.RuntimeCommandResult{}, err
	}
	exitCode := 0
	if result.CommandComplete.ExitCode != nil {
		exitCode = *result.CommandComplete.ExitCode
	}
	return execution.RuntimeCommandResult{Stdout: result.Stdout, Stderr: result.Stderr, ExitCode: exitCode}, nil
}

func (r *MacSessionRuntime) CancelCommand(ctx context.Context, request execution.RuntimeCommandRequest) (execution.RuntimeCommandStopResult, error) {
	if r == nil || r.adapter == nil {
		return execution.RuntimeCommandStopResult{}, execution.ErrRuntimeUnavailable
	}
	shell, err := r.adapter.Shell(string(request.Session.SessionID))
	if err != nil {
		return execution.RuntimeCommandStopResult{}, err
	}
	stopped, err := shell.CancelCurrentCommand(ctx, 500*time.Millisecond)
	return execution.RuntimeCommandStopResult{Confirmed: stopped.Confirmed}, err
}

func (r *MacSessionRuntime) StopSession(_ context.Context, session store.SessionRecord) (bool, error) {
	if r == nil || r.adapter == nil {
		return false, execution.ErrRuntimeUnavailable
	}
	if err := r.adapter.Cleanup(string(session.SessionID)); err != nil {
		return false, err
	}
	r.mu.Lock()
	delete(r.prepared, string(session.SessionID))
	r.mu.Unlock()
	return true, nil
}

// Reconcile quarantines and stops the prior process group. A successful
// result never restores the old in-memory shell handle.
func (r *MacSessionRuntime) Reconcile(ctx context.Context, request execution.RuntimeReconcileRequest) (execution.RuntimeReconcileResult, error) {
	if r == nil || r.adapter == nil {
		return execution.RuntimeReconcileResult{}, execution.ErrRuntimeUnavailable
	}
	result, err := r.adapter.ReconcileSession(ctx, string(request.Session.SessionID), request.Session.RuntimeGeneration, 500*time.Millisecond)
	return execution.RuntimeReconcileResult{RuntimeGeneration: result.Generation, CleanupConfirmed: result.CleanupConfirmed}, err
}

// ReconcileLostRuntime proves an explicitly selected lost local runtime while
// retaining its owner record. The shared execution service records the paired
// SQLite release before FinalizeLostRuntime may remove that record.
func (r *MacSessionRuntime) ReconcileLostRuntime(ctx context.Context, request execution.RuntimeReconcileRequest) (execution.RuntimeReconcileResult, error) {
	if r == nil || r.adapter == nil {
		return execution.RuntimeReconcileResult{}, execution.ErrRuntimeUnavailable
	}
	result, err := r.adapter.ConfirmLostRecoveryCleanup(ctx, string(request.Session.SessionID), request.Session.RuntimeGeneration, 500*time.Millisecond)
	return execution.RuntimeReconcileResult{RuntimeGeneration: result.Generation, CleanupConfirmed: result.CleanupConfirmed}, err
}

// FinalizeLostRuntime removes the retained local owner record only after the
// shared service has durably released both matching capacity records. It does
// not inspect or signal the old process identity.
func (r *MacSessionRuntime) FinalizeLostRuntime(ctx context.Context, request execution.RuntimeReconcileRequest) (execution.RuntimeReconcileResult, error) {
	if r == nil || r.adapter == nil {
		return execution.RuntimeReconcileResult{}, execution.ErrRuntimeUnavailable
	}
	result, err := r.adapter.FinalizeLostRecoveryCleanup(ctx, string(request.Session.SessionID), request.Session.RuntimeGeneration)
	return execution.RuntimeReconcileResult{RuntimeGeneration: result.Generation, CleanupConfirmed: result.CleanupConfirmed}, err
}

func (r *MacSessionRuntime) AuditOwnership(ctx context.Context, attributable map[string]struct{}) error {
	if r == nil || r.adapter == nil {
		return execution.ErrRuntimeUnavailable
	}
	return r.adapter.AuditOwnership(ctx, attributable)
}

func newRuntimeGeneration() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate runtime generation: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}
