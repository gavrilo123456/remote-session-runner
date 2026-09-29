package runnerlocald

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"remote-session-runner/src/internal/execution"
	hostruntime "remote-session-runner/src/internal/runtime"
	"remote-session-runner/src/internal/store"
)

// MacSessionRuntime adapts the shared execution service to the selected Mac
// account. It mirrors the Linux adapter shape; all authoritative state remains
// in the shared AuthorityStore.
type MacSessionRuntime struct {
	adapter  *hostruntime.MacProcessAdapter
	mu       sync.Mutex
	prepared map[string]hostruntime.MacPrepared
}

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
