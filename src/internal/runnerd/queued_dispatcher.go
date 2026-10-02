package runnerd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	"remote-session-runner/src/internal/lifecycle"
	"remote-session-runner/src/internal/store"
)

const (
	defaultRunnerDispatcherRecoveryInterval                     = time.Second
	defaultRunnerDispatcherRetainedLostCapacityRecoveryInterval = time.Minute
)

var ErrRunnerDispatcherConfiguration = errors.New("runnerd queued dispatcher configuration is incomplete")

// RunnerDispatcherOptions supplies the Linux-owned queue worker with the
// authority and lifecycle boundary that already govern runnerd command work.
// It never owns an ingress listener or a second scheduler transaction.
type RunnerDispatcherOptions struct {
	Service                              *execution.Service
	Authority                            *store.AuthorityStore
	DispatchGate                         *lifecycle.Gate
	RecoveryInterval                     time.Duration
	RetainedLostCapacityRecoveryInterval time.Duration
	Logger                               *slog.Logger
}

// RunnerDispatcher turns durable queued commands into bounded runtime workers.
// A durable claim always belongs to exactly one worker, while the store remains
// the source of truth for ordering, capacity, and terminal outcomes.
type RunnerDispatcher struct {
	service                              *execution.Service
	authority                            *store.AuthorityStore
	dispatchGate                         *lifecycle.Gate
	recoveryInterval                     time.Duration
	retainedLostCapacityRecoveryInterval time.Duration
	logger                               *slog.Logger

	wake chan struct{}
	done chan struct{}

	mu         sync.Mutex
	started    bool
	cancel     context.CancelFunc
	activeJobs map[domain.JobID]struct{}
	deferred   map[domain.JobID]struct{}
	// passDeferred throttles a retry after a store-wide scheduler read or
	// claim failure. Per-job retry failures use deferred above; this flag
	// prevents unrelated ingress wakes from turning an unavailable authority
	// into an unbounded scheduler loop.
	passDeferred bool
	// nextRetainedLostCapacityRecoveryAttempt throttles only runtime proof
	// attempts after the read-only candidate discovery has found a complete
	// retained set. A failed proof records a durable cleanup audit, so trying
	// again on every one-second scheduler tick would cause avoidable audit
	// growth while the process state is unchanged.
	nextRetainedLostCapacityRecoveryAttempt time.Time
	// nextLostRuntimeFinalizationAttempt is deliberately independent from
	// retained-capacity recovery. A transient ownership-marker cleanup failure
	// must not postpone a later proof that can release every occupied slot.
	nextLostRuntimeFinalizationAttempt time.Time
	jobWorkers                         chan struct{}
}

// NewRunnerDispatcher validates a process-local dispatcher. The durable store
// provides the cross-process claim boundary; this worker is deliberately small
// enough that a restart leaves no in-memory claim to recover.
func NewRunnerDispatcher(options RunnerDispatcherOptions) (*RunnerDispatcher, error) {
	if options.Service == nil || options.Authority == nil || options.DispatchGate == nil {
		return nil, ErrRunnerDispatcherConfiguration
	}
	if options.RecoveryInterval <= 0 {
		options.RecoveryInterval = defaultRunnerDispatcherRecoveryInterval
	}
	if options.RetainedLostCapacityRecoveryInterval <= 0 {
		options.RetainedLostCapacityRecoveryInterval = defaultRunnerDispatcherRetainedLostCapacityRecoveryInterval
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	return &RunnerDispatcher{
		service:                              options.Service,
		authority:                            options.Authority,
		dispatchGate:                         options.DispatchGate,
		recoveryInterval:                     options.RecoveryInterval,
		retainedLostCapacityRecoveryInterval: options.RetainedLostCapacityRecoveryInterval,
		logger:                               options.Logger,
		wake:                                 make(chan struct{}, 1),
		done:                                 make(chan struct{}),
		activeJobs:                           make(map[domain.JobID]struct{}),
		deferred:                             make(map[domain.JobID]struct{}),
		jobWorkers:                           make(chan struct{}, store.DefaultRunningCommandLimit),
	}, nil
}

// Start begins the coalesced wake loop. Starting it twice is harmless. The
// caller must run ReconcileStartup before Start so prior shell ownership is
// settled before any job recovery can enter a runtime path.
func (d *RunnerDispatcher) Start(ctx context.Context) {
	if d == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	d.mu.Lock()
	if d.started {
		d.mu.Unlock()
		return
	}
	runContext, cancel := context.WithCancel(ctx)
	d.started, d.cancel = true, cancel
	d.mu.Unlock()
	go func() {
		defer close(d.done)
		d.run(runContext)
	}()
	d.Wake()
}

// Stop ends the wake loop. Already admitted workers retain their dispatch-gate
// lease and are drained by the normal runnerd shutdown coordinator.
func (d *RunnerDispatcher) Stop() {
	if d == nil {
		return
	}
	d.mu.Lock()
	cancel := d.cancel
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Wait waits only for the wake loop. Command and job workers are accounted for
// by DispatchGate and are therefore drained by lifecycle.Coordinator.
func (d *RunnerDispatcher) Wait(ctx context.Context) error {
	if d == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	d.mu.Lock()
	started := d.started
	done := d.done
	d.mu.Unlock()
	if !started {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Wake requests a scheduling pass without retaining unbounded notifications.
// A full channel means a pass is already pending, which is sufficient because
// each pass drains all currently available command capacity.
func (d *RunnerDispatcher) Wake() {
	if d == nil {
		return
	}
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// RecoverNonterminalJobs schedules durable coordinator records that predate
// this runnerd process. It is called only after ReconcileStartup. A missing
// session row at that point proves the accepted job never crossed the durable
// session-creation boundary, so its stored canonical request can be resumed.
// Existing terminal child records are also settled into a terminal job result.
func (d *RunnerDispatcher) RecoverNonterminalJobs(ctx context.Context) error {
	if d == nil || d.authority == nil {
		return ErrRunnerDispatcherConfiguration
	}
	return d.settleNonterminalJobs(ctx, true, true)
}

func (d *RunnerDispatcher) run(ctx context.Context) {
	ticker := time.NewTicker(d.recoveryInterval)
	defer ticker.Stop()
	for {
		retryDeferred := false
		select {
		case <-ctx.Done():
			return
		case <-d.wake:
		case <-ticker.C:
			retryDeferred = true
		}
		if ctx.Err() != nil {
			return
		}
		// Fresh accepted jobs without a session row have not crossed a runtime
		// boundary and receive an immediate attempt. A retryable failure is
		// deferred until the bounded recovery tick so unrelated wakes cannot
		// turn it into a hot loop.
		if d.schedulingPassDeferred() && !retryDeferred {
			continue
		}
		if err := d.settleNonterminalJobs(ctx, true, retryDeferred); err != nil {
			d.logger.Warn("runnerd queued dispatcher could not inspect jobs", "lifecycle_phase", "job_settlement", "reason", queuedDispatcherFailureReason(err))
			d.deferSchedulingPass()
			continue
		}
		d.clearSchedulingPassDeferral()
		if retryDeferred {
			if err := d.finalizePendingLostRuntimeRecoveries(ctx); err != nil {
				d.logger.Warn("runnerd queued dispatcher could not finalize recovered lost runtime ownership", "lifecycle_phase", "lost_capacity_finalization", "reason", queuedDispatcherFailureReason(err))
				d.deferSchedulingPass()
				continue
			}
		}
		if err := d.dispatch(ctx, retryDeferred); err != nil {
			d.logger.Warn("runnerd queued dispatcher could not claim command", "lifecycle_phase", "command_claim", "reason", queuedDispatcherFailureReason(err))
			d.deferSchedulingPass()
		}
	}
}

func (d *RunnerDispatcher) dispatch(ctx context.Context, recoveryTick bool) error {
	for {
		if ctx != nil && ctx.Err() != nil {
			return nil
		}
		release, err := d.dispatchGate.Enter()
		if err != nil {
			return nil
		}
		// The gate is the shutdown claim barrier. Check context again after
		// admission so an already-observed process stop cannot write a new
		// running record while the coordinator is freezing dispatch.
		if ctx != nil && ctx.Err() != nil {
			release()
			return nil
		}
		claim, err := d.service.ClaimNextEligibleCommand(context.Background())
		if err != nil {
			if errors.Is(err, store.ErrCommandSlotsFull) {
				if recoveryTick {
					recovered, recoveryErr := d.recoverRetainedLostCapacity(ctx)
					release()
					if recoveryErr != nil {
						return recoveryErr
					}
					if recovered {
						d.Wake()
					}
					return nil
				}
				release()
				return nil
			}
			release()
			if errors.Is(err, store.ErrCommandNotEligible) {
				return nil
			}
			return fmt.Errorf("claim next eligible command: %w", err)
		}
		go d.executeClaim(claim, release)
	}
}

// finalizePendingLostRuntimeRecoveries retries only post-release ownership
// finalization. It runs on the bounded tick before ordinary dispatch so a
// crash after durable capacity release cannot leave an unattributed marker for
// the next runnerd startup. It cannot release capacity or execute a script.
func (d *RunnerDispatcher) finalizePendingLostRuntimeRecoveries(ctx context.Context) error {
	pairs, err := d.authority.ListPendingLostRuntimeRecoveryFinalizations(ctx)
	if err != nil {
		return fmt.Errorf("list pending lost runtime recovery finalizations: %w", err)
	}
	if len(pairs) == 0 || !d.beginLostRuntimeFinalizationAttempt(time.Now()) {
		return nil
	}
	release, err := d.dispatchGate.Enter()
	if err != nil {
		return nil
	}
	defer release()
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	requests := make([]execution.LostRuntimeRecoveryRequest, 0, len(pairs))
	for _, pair := range pairs {
		requests = append(requests, execution.LostRuntimeRecoveryRequest{SessionID: pair.SessionID, CommandID: pair.CommandID})
	}
	_, err = d.service.FinalizeReleasedLostRuntimeRecoveryBatch(ctx, requests)
	if err == nil {
		return nil
	}
	if errors.Is(err, execution.ErrLostRuntimeRecoveryFinalization) ||
		errors.Is(err, execution.ErrLostRuntimeRecoveryIneligible) ||
		errors.Is(err, store.ErrLostRuntimeRecoveryNotReleasable) {
		d.logger.Warn("runnerd retained lost capacity finalization remains pending", "lifecycle_phase", "lost_capacity_finalization", "reason", retainedCapacityRecoveryFailureReason(err))
		return nil
	}
	return fmt.Errorf("finalize recovered lost runtime ownership: %w", err)
}

// recoverRetainedLostCapacity is deliberately called only after the normal
// scheduler has established that all command slots are full on a bounded tick.
// It discovers candidates without reading scripts, then delegates process
// proof and the single durable paired release to the existing queue-preserving
// recovery service. It never claims or replays a lost command.
func (d *RunnerDispatcher) recoverRetainedLostCapacity(ctx context.Context) (bool, error) {
	pairs, err := d.authority.ListRetainedLostRuntimeRecoveryPairs(ctx)
	if err != nil {
		return false, fmt.Errorf("list retained lost runtime recovery pairs: %w", err)
	}
	if len(pairs) != store.DefaultRunningCommandLimit {
		return false, nil
	}
	if !d.beginRetainedLostCapacityRecoveryAttempt(time.Now()) {
		return false, nil
	}

	requests := make([]execution.LostRuntimeRecoveryRequest, 0, len(pairs))
	for _, pair := range pairs {
		requests = append(requests, execution.LostRuntimeRecoveryRequest{
			SessionID: pair.SessionID,
			CommandID: pair.CommandID,
		})
	}
	_, err = d.service.RecoverLostRuntimeBatchPreservingQueuedOneOffs(ctx, requests)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, execution.ErrLostRuntimeRecoveryFinalization) {
		// The paired durable release has already committed. Keep the separate
		// finalization warning truthful, but let the original queued command
		// proceed on the normal dispatcher path.
		d.logger.Warn("runnerd retained lost capacity recovery finalization is pending", "lifecycle_phase", "lost_capacity_recovery", "reason", retainedCapacityRecoveryFailureReason(err))
		return true, nil
	}
	if errors.Is(err, execution.ErrLostRuntimeRecoveryUnconfirmed) ||
		errors.Is(err, execution.ErrLostRuntimeRecoveryIneligible) ||
		errors.Is(err, store.ErrLostRuntimeRecoveryNotReleasable) {
		d.logger.Warn("runnerd retained lost capacity recovery remains pending", "lifecycle_phase", "lost_capacity_recovery", "reason", retainedCapacityRecoveryFailureReason(err))
		return false, nil
	}
	return false, fmt.Errorf("recover retained lost capacity: %w", err)
}

func (d *RunnerDispatcher) beginRetainedLostCapacityRecoveryAttempt(now time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if now.Before(d.nextRetainedLostCapacityRecoveryAttempt) {
		return false
	}
	d.nextRetainedLostCapacityRecoveryAttempt = now.Add(d.retainedLostCapacityRecoveryInterval)
	return true
}

func (d *RunnerDispatcher) beginLostRuntimeFinalizationAttempt(now time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if now.Before(d.nextLostRuntimeFinalizationAttempt) {
		return false
	}
	d.nextLostRuntimeFinalizationAttempt = now.Add(d.retainedLostCapacityRecoveryInterval)
	return true
}

func (d *RunnerDispatcher) executeClaim(claim store.CommandRecord, release func()) {
	defer release()
	if _, err := d.service.ExecuteClaimedCommand(context.Background(), claim); err != nil {
		d.logger.Warn("runnerd queued dispatcher command finished with service error", "command_id", claim.CommandID, "lifecycle_phase", "command_execution", "reason", queuedDispatcherFailureReason(err))
	}
	if job, err := d.authority.GetJobByCommandID(context.Background(), claim.CommandID); err == nil {
		d.resumeStoredJobNow(job.JobID)
	} else if !errors.Is(err, store.ErrJobNotFound) {
		d.logger.Warn("runnerd queued dispatcher could not find command job", "command_id", claim.CommandID, "lifecycle_phase", "job_lookup", "reason", queuedDispatcherFailureReason(err))
	}
	d.Wake()
}

func (d *RunnerDispatcher) settleNonterminalJobs(ctx context.Context, includeMissingSessions, retryDeferred bool) error {
	jobs, err := d.authority.ListNonterminalJobs(ctx)
	if err != nil {
		return err
	}
	for _, job := range jobs {
		needsSettlement, err := d.jobNeedsSettlement(ctx, job, includeMissingSessions)
		if err != nil {
			return err
		}
		if !needsSettlement {
			continue
		}
		d.launchStoredJob(ctx, job.JobID, retryDeferred)
	}
	return nil
}

func (d *RunnerDispatcher) jobNeedsSettlement(ctx context.Context, job store.JobRecord, includeMissingSessions bool) (bool, error) {
	switch job.Phase {
	case store.JobPhaseCreatingSession:
		session, err := d.authority.GetSession(ctx, job.SessionID)
		if errors.Is(err, store.ErrSessionNotFound) {
			return includeMissingSessions, nil
		}
		if err != nil {
			return false, queuedDispatcherJobReadError(job.JobID, "session")
		}
		// A session may already be durable while a transient error prevented
		// the coordinator checkpoint from advancing. ResumeStoredJob reuses the
		// stable session-create key, so a ready/busy/terminal child can be
		// checkpointed without constructing another runtime session. A creating
		// child is also safe to inspect on the bounded retry path; the service
		// will not start a second shell for an idempotent existing row.
		_ = session
		return true, nil
	case store.JobPhaseAcceptingCommand:
		session, err := d.authority.GetSession(ctx, job.SessionID)
		if err != nil {
			return false, queuedDispatcherJobReadError(job.JobID, "session")
		}
		// The session can be ready/busy after an interrupted command-acceptance
		// checkpoint. Resuming with the stored command key is idempotent; if it
		// is terminal, the same path records that final coordinator result.
		_ = session
		return true, nil
	case store.JobPhaseAwaitingCommand:
		command, err := d.authority.GetCommand(ctx, job.CommandID)
		if err != nil {
			return false, queuedDispatcherJobReadError(job.JobID, "command")
		}
		return command.State.IsTerminal(), nil
	case store.JobPhaseClosingSession:
		session, err := d.authority.GetSession(ctx, job.SessionID)
		if err != nil {
			return false, queuedDispatcherJobReadError(job.JobID, "session")
		}
		// A close retry uses the durable close key. It is required when the
		// command has completed but a transient stop/checkpoint error left the
		// session ready, busy, or closing.
		_ = session
		return true, nil
	default:
		return false, nil
	}
}

func queuedDispatcherJobReadError(id domain.JobID, resource string) error {
	return fmt.Errorf("inspect stored job %s %s: authority read failed", id, resource)
}

func (d *RunnerDispatcher) launchStoredJob(ctx context.Context, id domain.JobID, retryDeferred bool) {
	if ctx != nil && ctx.Err() != nil {
		return
	}
	if d.isDeferredStoredJob(id) && !retryDeferred {
		return
	}
	if !d.acquireStoredJob(id) {
		return
	}
	release, err := d.dispatchGate.Enter()
	if err != nil {
		d.finishStoredJob(id)
		return
	}
	// Match command dispatch: a shutdown observed before or immediately after
	// gate admission cannot launch a fresh one-off runtime worker.
	if ctx != nil && ctx.Err() != nil {
		release()
		d.finishStoredJob(id)
		return
	}
	go func() {
		defer release()
		defer d.finishStoredJob(id)
		// Only a successful coordinator pass can have exposed new eligible
		// command work. Retrying a transient pre-session failure immediately
		// would turn one accepted job into a hot loop; the bounded recovery tick
		// and later capacity/eligibility wakes own that retry instead.
		if d.resumeStoredJob(id) {
			d.Wake()
		}
	}()
}

func (d *RunnerDispatcher) resumeStoredJobNow(id domain.JobID) {
	if !d.acquireStoredJob(id) {
		return
	}
	defer d.finishStoredJob(id)
	d.resumeStoredJob(id)
}

func (d *RunnerDispatcher) resumeStoredJob(id domain.JobID) bool {
	if _, err := d.service.ResumeStoredJob(context.Background(), id); err != nil {
		if job, readErr := d.authority.GetJob(context.Background(), id); readErr == nil && terminalJobPhase(job.Phase) {
			d.clearDeferredStoredJob(id)
			return false
		}
		d.deferStoredJob(id)
		d.logger.Warn("runnerd queued dispatcher could not resume stored job", "job_id", id, "lifecycle_phase", "job_resume", "reason", queuedDispatcherFailureReason(err))
		return false
	}
	d.clearDeferredStoredJob(id)
	return true
}

func terminalJobPhase(phase store.JobPhase) bool {
	return phase == store.JobPhaseComplete || phase == store.JobPhaseFailed || phase == store.JobPhaseLost
}

func queuedDispatcherFailureReason(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "context_cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, lifecycle.ErrAdmissionClosed):
		return "dispatch_stopped"
	case errors.Is(err, store.ErrCommandSlotsFull):
		return "command_slots_full"
	case errors.Is(err, store.ErrCommandNotEligible):
		return "command_not_eligible"
	case errors.Is(err, store.ErrJobNotFound):
		return "job_not_found"
	default:
		return "operation_failed"
	}
}

func (d *RunnerDispatcher) acquireStoredJob(id domain.JobID) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, active := d.activeJobs[id]; active {
		return false
	}
	select {
	case d.jobWorkers <- struct{}{}:
		d.activeJobs[id] = struct{}{}
		return true
	default:
		return false
	}
}

func (d *RunnerDispatcher) isDeferredStoredJob(id domain.JobID) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, deferred := d.deferred[id]
	return deferred
}

func (d *RunnerDispatcher) deferStoredJob(id domain.JobID) {
	d.mu.Lock()
	d.deferred[id] = struct{}{}
	d.mu.Unlock()
}

func (d *RunnerDispatcher) clearDeferredStoredJob(id domain.JobID) {
	d.mu.Lock()
	delete(d.deferred, id)
	d.mu.Unlock()
}

func (d *RunnerDispatcher) schedulingPassDeferred() bool {
	d.mu.Lock()
	deferred := d.passDeferred
	d.mu.Unlock()
	return deferred
}

func (d *RunnerDispatcher) deferSchedulingPass() {
	d.mu.Lock()
	d.passDeferred = true
	d.mu.Unlock()
}

func (d *RunnerDispatcher) clearSchedulingPassDeferral() {
	d.mu.Lock()
	d.passDeferred = false
	d.mu.Unlock()
}

func (d *RunnerDispatcher) finishStoredJob(id domain.JobID) {
	d.mu.Lock()
	delete(d.activeJobs, id)
	d.mu.Unlock()
	select {
	case <-d.jobWorkers:
	default:
		d.logger.Error("runnerd queued dispatcher job-worker accounting mismatch", "job_id", id)
	}
}
