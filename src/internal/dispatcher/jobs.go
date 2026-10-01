package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/sshbridge"
	"remote-session-runner/src/internal/store"
)

var remoteProjectionReadSequence atomic.Uint64

// RefreshJobProjection reads and persists an authoritative remote one-off job
// view. It is read-only: it never carries a mutation resource or idempotency
// key and it never resumes a job.
func (d *RemoteDriver) RefreshJobProjection(ctx context.Context, jobID domain.JobID, controller domain.ControllerIdentity) (store.RemoteJobProjection, error) {
	if !d.configured() {
		return store.RemoteJobProjection{}, ErrRemoteDriverConfiguration
	}
	validatedJob, err := domain.NewJobID(string(jobID))
	if err != nil {
		return store.RemoteJobProjection{}, err
	}
	intent, err := d.authority.GetLocalIntentByResource(ctx, "run", string(validatedJob), controller)
	if err != nil {
		return store.RemoteJobProjection{}, err
	}
	return d.refreshJobProjectionForIntent(ctx, intent)
}

// ReconcileAcceptedRemoteRuns reconciles a bounded set of already accepted
// remote one-off runs. Every remote operation in this recovery path is a
// status/event read; an accepted mutation is never sent again.
func (d *RemoteDriver) ReconcileAcceptedRemoteRuns(ctx context.Context, limit int) error {
	if !d.configured() {
		return ErrRemoteDriverConfiguration
	}
	d.reconcileMu.Lock()
	defer d.reconcileMu.Unlock()
	intents, err := d.nextAcceptedRemoteRunPage(ctx, limit)
	if err != nil {
		return err
	}
	var result error
	for _, intent := range intents {
		if err := ctx.Err(); err != nil {
			return errors.Join(result, err)
		}
		if err := d.ReconcileAcceptedRun(ctx, intent.IntentID); err != nil {
			result = errors.Join(result, remoteReconciliationIssueFor(intent, "run", err))
		}
	}
	return result
}

// ReconcileAcceptedRemoteSubmits reconciles a bounded set of accepted remote
// session commands. It uses only strict target reads and event streams; it
// never repeats a submit mutation or sends the script again.
func (d *RemoteDriver) ReconcileAcceptedRemoteSubmits(ctx context.Context, limit int) error {
	if !d.configured() {
		return ErrRemoteDriverConfiguration
	}
	d.reconcileMu.Lock()
	defer d.reconcileMu.Unlock()
	intents, err := d.nextAcceptedRemoteSubmitPage(ctx, limit)
	if err != nil {
		return err
	}
	var result error
	for _, intent := range intents {
		if err := ctx.Err(); err != nil {
			return errors.Join(result, err)
		}
		if err := d.ReconcileAcceptedRemoteSubmit(ctx, intent.IntentID); err != nil {
			result = errors.Join(result, remoteReconciliationIssueFor(intent, "submit_command", err))
		}
	}
	return result
}

// nextAcceptedRemoteRunPage rotates a bounded recovery scan through the
// durable ordering. Once it reaches the end it starts a new sweep, which also
// picks up records inserted before the prior cursor while the scan was active.
// The cursor advances to selected work even when a strict read finds it active
// or fails, preventing a fixed prefix from starving a later terminal result.
func (d *RemoteDriver) nextAcceptedRemoteRunPage(ctx context.Context, limit int) ([]store.LocalIntentRecord, error) {
	intents, err := d.authority.ListAcceptedRemoteRunIntentsAfter(ctx, limit, d.runCursor)
	if err != nil {
		return nil, err
	}
	if len(intents) == 0 && d.runCursor != nil {
		d.runCursor = nil
		intents, err = d.authority.ListAcceptedRemoteRunIntentsAfter(ctx, limit, nil)
		if err != nil {
			return nil, err
		}
	}
	if len(intents) > 0 {
		last := intents[len(intents)-1]
		d.runCursor = &store.RemoteIntentCursor{CreatedAt: last.CreatedAt, IntentID: last.IntentID}
	}
	return intents, nil
}

// nextAcceptedRemoteSubmitPage applies the same rotating keyset scan to
// ordered remote session-command reconciliation.
func (d *RemoteDriver) nextAcceptedRemoteSubmitPage(ctx context.Context, limit int) ([]store.LocalIntentRecord, error) {
	intents, err := d.authority.ListAcceptedRemoteSubmitIntentsAfter(ctx, limit, d.submitCursor)
	if err != nil {
		return nil, err
	}
	if len(intents) == 0 && d.submitCursor != nil {
		d.submitCursor = nil
		intents, err = d.authority.ListAcceptedRemoteSubmitIntentsAfter(ctx, limit, nil)
		if err != nil {
			return nil, err
		}
	}
	if len(intents) > 0 {
		last := intents[len(intents)-1]
		d.submitCursor = &store.RemoteIntentCursor{CreatedAt: last.CreatedAt, IntentID: last.IntentID}
	}
	return intents, nil
}

// ReconcileAcceptedRemoteSubmit reconciles one already accepted remote
// session command after a Router restart or an earlier incomplete event read.
// It changes the delivery state only after a strict terminal projection and
// its durable event boundary agree.
func (d *RemoteDriver) ReconcileAcceptedRemoteSubmit(ctx context.Context, id domain.IntentID) error {
	if !d.configured() {
		return ErrRemoteDriverConfiguration
	}
	intent, err := d.authority.GetLocalIntent(ctx, id)
	if err != nil {
		return err
	}
	if intent.Operation != operationSubmitCommand || intent.Target.Kind() != domain.TargetKindRemote {
		return fmt.Errorf("%w: intent is not a remote session command", ErrRemoteResponse)
	}
	if intent.DeliveryState != store.LocalIntentAccepted && (intent.DeliveryState != store.LocalIntentReconciled || store.HasRemoteTerminalProof(intent)) {
		return nil
	}
	return d.reconcileAcceptedRemoteSubmit(ctx, intent)
}

func (d *RemoteDriver) reconcileAcceptedRemoteSubmit(ctx context.Context, intent store.LocalIntentRecord) error {
	command, err := d.refreshCommandProjectionForIntent(ctx, intent)
	if err != nil {
		return err
	}
	_, gapErr := d.authority.GetRemoteEventGap(ctx, command.CommandID)
	if gapErr != nil && !errors.Is(gapErr, store.ErrRemoteGapNotFound) {
		return gapErr
	}
	if errors.Is(gapErr, store.ErrRemoteGapNotFound) && command.OutputUnavailableReason != "retention_expired" {
		if _, err := d.mirrorCommandEventsForIntent(ctx, intent); err != nil {
			return err
		}
	}

	// The stream can race a target state transition. Re-read after it so the
	// stored projection and terminal boundary are tied to one target view.
	command, err = d.refreshCommandProjectionForIntent(ctx, intent)
	if err != nil {
		return err
	}
	if !command.State.IsTerminal() {
		return nil
	}
	if err := d.requireTerminalCommandEventBoundary(ctx, command); err != nil {
		return err
	}
	return d.transitionAcceptedIntentReconciled(ctx, intent, "remote_terminal_command_reconciled")
}

// ReconcileAcceptedRun refreshes one accepted remote one-off run after a
// Router restart or an earlier incomplete event stream. It only changes the
// local delivery state when the target job, command, and retained event prefix
// form one internally consistent terminal snapshot.
func (d *RemoteDriver) ReconcileAcceptedRun(ctx context.Context, id domain.IntentID) error {
	if !d.configured() {
		return ErrRemoteDriverConfiguration
	}
	intent, err := d.authority.GetLocalIntent(ctx, id)
	if err != nil {
		return err
	}
	if intent.Operation != "run" || intent.Target.Kind() != domain.TargetKindRemote {
		return fmt.Errorf("%w: intent is not a remote one-off run", ErrRemoteResponse)
	}
	if intent.DeliveryState != store.LocalIntentAccepted && (intent.DeliveryState != store.LocalIntentReconciled || store.HasRemoteTerminalProof(intent)) {
		return nil
	}
	// A prior strict read established neither a terminal result nor an active
	// status. Once that bounded diagnostic window has elapsed, the mailbox owns
	// the truthful indeterminate result. Do not keep polling and do not replay
	// the accepted mutation.
	if intent.DeliveryState == store.LocalIntentAccepted && intent.RemoteStatusFailureAt != nil &&
		!d.now().UTC().Before(intent.RemoteStatusFailureAt.Add(RemoteUncertaintyWindow)) {
		return nil
	}
	err = d.reconcileAcceptedRun(ctx, intent)
	if err != nil {
		if remoteRunStatusFailure(err) {
			marked, markErr := d.authority.MarkAcceptedRemoteRunStatusFailure(ctx, intent.IntentID, store.RemoteStatusFailureCodeUnavailable)
			if markErr != nil {
				return errors.Join(err, fmt.Errorf("record remote run status failure: %w", markErr))
			}
			return remoteReconciliationIssue(intent, "run", store.RemoteStatusFailureCodeUnavailable, marked.RemoteStatusFailureAttempts, err)
		}
		return err
	}
	// A coherent active status clears an earlier transient read failure. A
	// successful terminal proof clears it atomically in the proof transition.
	current, readErr := d.authority.GetLocalIntent(ctx, intent.IntentID)
	if readErr != nil {
		return readErr
	}
	if current.DeliveryState == store.LocalIntentAccepted && current.RemoteStatusFailureAt != nil {
		if _, clearErr := d.authority.ClearAcceptedRemoteRunStatusFailure(ctx, current.IntentID); clearErr != nil {
			return clearErr
		}
	}
	return nil
}

func (d *RemoteDriver) reconcileAcceptedRun(ctx context.Context, intent store.LocalIntentRecord) error {
	job, err := d.refreshJobProjectionForIntent(ctx, intent)
	if err != nil {
		return err
	}
	if remoteRunTerminalTeardownUnconfirmed(job) {
		return fmt.Errorf("%w: terminal one-off job has no confirmed teardown", ErrRemoteResponse)
	}
	if !terminalRemoteJob(job) {
		return nil
	}

	command, err := d.refreshCommandProjectionForIntent(ctx, intent)
	if err != nil {
		return err
	}
	_, gapErr := d.authority.GetRemoteEventGap(ctx, command.CommandID)
	if gapErr != nil && !errors.Is(gapErr, store.ErrRemoteGapNotFound) {
		return gapErr
	}
	if errors.Is(gapErr, store.ErrRemoteGapNotFound) && command.OutputUnavailableReason != "retention_expired" {
		mirror, err := d.mirrorCommandEventsForIntent(ctx, intent)
		if err != nil {
			return err
		}
		if mirror.Gap != nil {
			// The target's GET response does not know that the Mac lost an
			// irrecoverable event range. Read it again so the durable local gap
			// normalization becomes the projection consumed by the mailbox.
			command, err = d.refreshCommandProjectionForIntent(ctx, intent)
			if err != nil {
				return err
			}
		}
	}

	// Read the job once more after the event mirror. A target can move through
	// its final teardown concurrently with the first job read; this second read
	// prevents a stale job status from settling the local intent.
	job, err = d.refreshJobProjectionForIntent(ctx, intent)
	if err != nil {
		return err
	}
	if remoteRunTerminalTeardownUnconfirmed(job) {
		return fmt.Errorf("%w: terminal one-off job has no confirmed teardown", ErrRemoteResponse)
	}
	if !terminalRemoteJob(job) {
		return nil
	}
	if err := validateTerminalRunSnapshot(job, command); err != nil {
		return err
	}
	if err := d.requireTerminalCommandEventBoundary(ctx, command); err != nil {
		return err
	}
	return d.transitionAcceptedIntentReconciled(ctx, intent, "remote_terminal_run_reconciled")
}

// remoteRunStatusFailure identifies errors caused by target status or event
// evidence that cannot establish a truthful terminal result. Local storage,
// configuration, and cancellation errors remain ordinary retry failures.
func remoteRunStatusFailure(err error) bool {
	return errors.Is(err, ErrRemoteResponse) ||
		errors.Is(err, ErrRemoteEventCallerUnavailable) ||
		errors.Is(err, ErrRemoteEventStream) ||
		errors.Is(err, ErrRemoteEventHistoryUnavailable) ||
		errors.Is(err, ErrRemoteEventCursor) ||
		errors.Is(err, ErrRemoteTerminalUnconfirmed) ||
		errors.Is(err, store.ErrRemoteEventBatch) ||
		errors.Is(err, store.ErrRemoteProjectionInvalid) ||
		errors.Is(err, store.ErrRemoteProjectionConflict)
}

// remoteRunTerminalTeardownUnconfirmed detects a target job that has reached
// a command-bearing terminal phase but has not recorded the corresponding
// teardown boundary. A pre-command creation failure remains an active
// diagnostic state and is intentionally not treated as this BUG-002 case.
func remoteRunTerminalTeardownUnconfirmed(job store.RemoteJobProjection) bool {
	if job.CommandState == nil {
		return false
	}
	switch job.Phase {
	case store.JobPhaseComplete:
		return job.TeardownState != store.JobTeardownClosed
	case store.JobPhaseLost:
		return job.TeardownState != store.JobTeardownLost
	default:
		return false
	}
}

func (d *RemoteDriver) refreshJobProjectionForIntent(ctx context.Context, intent store.LocalIntentRecord) (store.RemoteJobProjection, error) {
	if intent.Operation != "run" || intent.Target.Kind() != domain.TargetKindRemote || intent.JobID == "" || intent.SessionID == "" || intent.CommandID == "" ||
		(intent.DeliveryState != store.LocalIntentAccepted && intent.DeliveryState != store.LocalIntentReconciled) {
		return store.RemoteJobProjection{}, fmt.Errorf("%w: job is not an accepted remote one-off intent", ErrRemoteResponse)
	}
	payload, err := json.Marshal(map[string]string{"job_id": string(intent.JobID)})
	if err != nil {
		return store.RemoteJobProjection{}, fmt.Errorf("%w: build job read payload", ErrRemotePayload)
	}
	request := sshbridge.RequestFrame{
		ProtocolVersion: sshbridge.ProtocolVersion,
		RequestID:       fmt.Sprintf("job-state/%s/%d", intent.JobID, remoteProjectionReadSequence.Add(1)),
		Operation:       sshbridge.OperationGetJob,
		Payload:         payload,
	}
	object, err := d.readRemoteProjectionObject(ctx, intent, request, "job")
	if err != nil {
		return store.RemoteJobProjection{}, err
	}
	projection, err := strictRemoteJobProjectionFromReadReply(intent, object, d.now())
	if err != nil {
		return store.RemoteJobProjection{}, err
	}
	stored, err := d.authority.UpsertRemoteJobProjection(ctx, projection)
	if err != nil {
		return store.RemoteJobProjection{}, err
	}
	if stored.IsStale || stored.ObservedAt.After(projection.ObservedAt) {
		return store.RemoteJobProjection{}, fmt.Errorf("%w: job read did not replace a stale or newer projection", ErrRemoteResponse)
	}
	return stored, nil
}

func (d *RemoteDriver) readRemoteProjectionObject(ctx context.Context, intent store.LocalIntentRecord, request sshbridge.RequestFrame, resource string) (map[string]json.RawMessage, error) {
	caller, err := d.remoteCallerForTarget(intent.Target)
	if err != nil {
		return nil, err
	}
	reply, err := caller.Call(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("%w: %s state read: %v", ErrRemoteResponse, resource, err)
	}
	if reply.ProtocolVersion != sshbridge.ProtocolVersion || reply.RequestID != request.RequestID {
		return nil, fmt.Errorf("%w: %s state read identity mismatch", ErrRemoteResponse, resource)
	}
	if reply.ResponseType != "result" {
		return nil, fmt.Errorf("%w: %s state response type %q", ErrRemoteResponse, resource, reply.ResponseType)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(reply.Payload, &object); err != nil || object == nil {
		return nil, fmt.Errorf("%w: %s state result object", ErrRemoteResponse, resource)
	}
	return object, nil
}

func terminalRemoteJob(job store.RemoteJobProjection) bool {
	if job.CommandState == nil {
		return false
	}
	switch job.Phase {
	case store.JobPhaseComplete:
		return job.TeardownState == store.JobTeardownClosed
	case store.JobPhaseLost:
		return job.TeardownState == store.JobTeardownLost
	default:
		return false
	}
}

func validateTerminalRunSnapshot(job store.RemoteJobProjection, command store.RemoteCommandProjection) error {
	if job.IsStale || command.IsStale || job.CommandState == nil || !command.State.IsTerminal() || *job.CommandState != command.State ||
		job.SessionID != command.SessionID || job.Target != command.Target || job.Controller != command.Controller ||
		job.Environment != command.Environment || job.Source != command.Source ||
		!sameOptionalRunInt(job.ExitCode, command.ExitCode) || !sameOptionalRunInt64(job.FinalEventSequence, command.FinalEventSequence) ||
		job.OutputComplete != command.OutputComplete || job.OutputTruncated != command.OutputTruncated ||
		job.OutputUnavailableReason != command.OutputUnavailableReason {
		return fmt.Errorf("%w: terminal job and command projection mismatch", ErrRemoteResponse)
	}
	return nil
}

func (d *RemoteDriver) requireTerminalCommandEventBoundary(ctx context.Context, command store.RemoteCommandProjection) error {
	if command.FinalEventSequence == nil || *command.FinalEventSequence <= 0 {
		return fmt.Errorf("%w: terminal command has no final event sequence", ErrRemoteResponse)
	}
	prefix, err := d.authority.InspectRemoteEventPrefix(ctx, command.CommandID)
	if err != nil {
		return err
	}
	cursor := prefix.Cursor
	lifecycle := prefix.Lifecycle
	if command.OutputUnavailableReason == "retention_expired" {
		if command.OutputComplete {
			return fmt.Errorf("%w: retention-expired command claims complete output", ErrRemoteResponse)
		}
		// The target no longer has a replayable event history, but the Mac may
		// already have an immutable prefix. Retention must not erase evidence
		// that contradicts the strict terminal status we just read.
		if lifecycle.TerminalState == nil {
			if cursor >= *command.FinalEventSequence {
				return fmt.Errorf("%w: retention-expired active prefix reaches final sequence %d", ErrRemoteResponse, *command.FinalEventSequence)
			}
			if lifecycle.OutputTruncated && !command.OutputTruncated {
				return fmt.Errorf("%w: retention-expired command loses retained output truncation", ErrRemoteResponse)
			}
			return nil
		}
		if cursor != *command.FinalEventSequence {
			return fmt.Errorf("%w: retention-expired terminal prefix cursor %d does not match final sequence %d", ErrRemoteResponse, cursor, *command.FinalEventSequence)
		}
		if *lifecycle.TerminalState != command.State {
			return fmt.Errorf("%w: retention-expired terminal event state %q does not match command state %q", ErrRemoteResponse, *lifecycle.TerminalState, command.State)
		}
		if lifecycle.OutputTruncated != command.OutputTruncated {
			return fmt.Errorf("%w: retention-expired terminal event truncation does not match command projection", ErrRemoteResponse)
		}
		return nil
	}
	if command.OutputUnavailableReason == "remote_event_gap" {
		if command.OutputComplete {
			return fmt.Errorf("%w: remote event gap claims complete output", ErrRemoteResponse)
		}
		if lifecycle.TerminalState != nil {
			return fmt.Errorf("%w: remote event gap retained prefix contains a terminal event", ErrRemoteResponse)
		}
		// The absent suffix may contain the only truncation event, so a target
		// true value is not contradicted by a false retained-prefix value. The
		// converse would claim a fuller answer than the immutable prefix allows.
		if lifecycle.OutputTruncated && !command.OutputTruncated {
			return fmt.Errorf("%w: remote event gap loses retained truncation", ErrRemoteResponse)
		}
		gap, err := d.authority.GetRemoteEventGap(ctx, command.CommandID)
		if err != nil {
			return err
		}
		if gap.FinalSequence != *command.FinalEventSequence || gap.AvailableSequence != cursor || gap.TerminalState != command.State ||
			gap.MissingFrom != cursor+1 || gap.MissingTo != *command.FinalEventSequence || cursor >= *command.FinalEventSequence {
			return fmt.Errorf("%w: remote event gap does not match terminal command", ErrRemoteResponse)
		}
		return nil
	}
	if cursor != *command.FinalEventSequence {
		return fmt.Errorf("%w: terminal event cursor %d does not reach final sequence %d", ErrRemoteResponse, cursor, *command.FinalEventSequence)
	}
	if lifecycle.TerminalState == nil {
		return fmt.Errorf("%w: terminal event is unavailable", ErrRemoteResponse)
	}
	if *lifecycle.TerminalState != command.State {
		return fmt.Errorf("%w: terminal event state %q does not match command state %q", ErrRemoteResponse, *lifecycle.TerminalState, command.State)
	}
	if lifecycle.OutputTruncated != command.OutputTruncated {
		return fmt.Errorf("%w: terminal event truncation does not match command projection", ErrRemoteResponse)
	}
	return nil
}

func (d *RemoteDriver) transitionAcceptedIntentReconciled(ctx context.Context, intent store.LocalIntentRecord, reason string) error {
	_, err := d.authority.MarkRemoteIntentTerminalProof(ctx, intent.IntentID, reason)
	if err == nil {
		return nil
	}
	current, readErr := d.authority.GetLocalIntent(ctx, intent.IntentID)
	if readErr == nil && store.HasRemoteTerminalProof(current) {
		return nil
	}
	return err
}

func sameOptionalRunInt(left, right *int) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && *left == *right)
}

func sameOptionalRunInt64(left, right *int64) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && *left == *right)
}
