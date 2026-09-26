package runnerd

import (
	"context"
	"fmt"
	"sync"

	"remote-session-runner/src/internal/execution"
	hostruntime "remote-session-runner/src/internal/runtime"
)

// LinuxSessionRuntime adapts the real Linux host-process adapter to the
// shared execution.SessionRuntime interface. It keeps only the prepared
// handle in memory; the authoritative session state remains in SQLite.
type LinuxSessionRuntime struct {
	adapter  *hostruntime.LinuxProcessAdapter
	mu       sync.Mutex
	prepared map[string]hostruntime.LinuxPrepared
}

// NewLinuxSessionRuntime constructs the P046 Linux runtime seam.
func NewLinuxSessionRuntime(adapter *hostruntime.LinuxProcessAdapter) (*LinuxSessionRuntime, error) {
	if adapter == nil {
		return nil, fmt.Errorf("%w: Linux adapter is nil", execution.ErrRuntimeUnavailable)
	}
	return &LinuxSessionRuntime{adapter: adapter, prepared: make(map[string]hostruntime.LinuxPrepared)}, nil
}

// Prepare materializes the session source and records the host-owned handle.
func (r *LinuxSessionRuntime) Prepare(ctx context.Context, request execution.RuntimePrepareRequest) (execution.RuntimePrepared, error) {
	if r == nil || r.adapter == nil {
		return execution.RuntimePrepared{}, execution.ErrRuntimeUnavailable
	}
	generation, err := newRuntimeGeneration()
	if err != nil {
		return execution.RuntimePrepared{}, fmt.Errorf("generate runtime generation: %w", err)
	}
	source := hostruntime.LinuxSourceRequest{
		Mode:              request.Session.Source.Mode(),
		RepositoryAlias:   request.Session.Source.RepositoryAlias(),
		RequestedRevision: request.Session.Source.RequestedRevision(),
	}
	prepared, err := r.adapter.PrepareSource(ctx, string(request.Session.SessionID), generation, source)
	if err != nil {
		return execution.RuntimePrepared{}, err
	}
	r.mu.Lock()
	r.prepared[string(request.Session.SessionID)] = prepared
	r.mu.Unlock()
	return execution.RuntimePrepared{RuntimeGeneration: generation, ResolvedRevision: prepared.Source.ResolvedRevision}, nil
}

// StartAgent starts the one persistent Bash process for the prepared session.
func (r *LinuxSessionRuntime) StartAgent(ctx context.Context, request execution.RuntimeStartRequest) (execution.RuntimeStarted, error) {
	if r == nil || r.adapter == nil {
		return execution.RuntimeStarted{}, execution.ErrRuntimeUnavailable
	}
	r.mu.Lock()
	prepared, ok := r.prepared[string(request.Session.SessionID)]
	r.mu.Unlock()
	if !ok || prepared.Generation != request.RuntimeGeneration || prepared.Generation != request.Prepared.RuntimeGeneration {
		return execution.RuntimeStarted{}, fmt.Errorf("%w: prepared runtime generation mismatch", execution.ErrRuntimeHandshake)
	}
	// The Bash agent is a durable session resource. It must not inherit the
	// private HTTP request context, which is canceled as soon as create returns;
	// cleanup and later lifecycle phases own its shutdown explicitly.
	if err := r.adapter.StartAgent(context.WithoutCancel(ctx), prepared); err != nil {
		return execution.RuntimeStarted{}, err
	}
	return execution.RuntimeStarted{RuntimeGeneration: prepared.Generation}, nil
}

// Cleanup tears down the adapter-owned shell and workspace. A missing
// preparation remains an error so the shared service records an uncertain
// cleanup conservatively rather than claiming a clean failure.
func (r *LinuxSessionRuntime) Cleanup(_ context.Context, request execution.RuntimeCleanupRequest) error {
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
