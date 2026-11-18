package dispatcher

import (
	"context"
	"errors"
	"time"

	"remote-session-runner/src/internal/sshbridge"
	"remote-session-runner/src/internal/sshclient"
	"remote-session-runner/src/internal/store"
)

// Read-only observations and each rotating reconciliation batch have their own
// budgets. BatchTimeout limits starting new attempts; an admitted proof gets
// enough time for its finite sequence of individually bounded reads. These
// budgets never replace command limits or mutation-delivery contexts.
const (
	DefaultRemoteObservationTimeout      = 15 * time.Second
	DefaultRemoteObservationBatchTimeout = 15 * time.Second
)

// RemoteObservationOptions bounds read-only status/event work, independently
// of command execution and mutation delivery. Zero values select the defaults.
// These limits are fixed when the driver is constructed, not changed live.
type RemoteObservationOptions struct {
	Timeout      time.Duration
	BatchTimeout time.Duration
}

var ErrRemoteObservationTimeout = errors.New("remote observation timed out")

func remoteObservationOptions(options []RemoteObservationOptions) (RemoteObservationOptions, error) {
	if len(options) > 1 {
		return RemoteObservationOptions{}, ErrRemoteDriverConfiguration
	}
	var limits RemoteObservationOptions
	if len(options) == 1 {
		limits = options[0]
	}
	if limits.Timeout < 0 || limits.BatchTimeout < 0 {
		return RemoteObservationOptions{}, ErrRemoteDriverConfiguration
	}
	if limits.Timeout == 0 {
		limits.Timeout = DefaultRemoteObservationTimeout
	}
	if limits.BatchTimeout == 0 {
		limits.BatchTimeout = DefaultRemoteObservationBatchTimeout
	}
	return limits, nil
}

type remoteObservationDeadlineKey struct{}

// remoteObservationStageError keeps transport details in the error chain for
// errors.Is/As without exposing them through an operational error string.
type remoteObservationStageError struct {
	stage string
	cause error
}

func (e *remoteObservationStageError) Error() string { return "remote observation failed" }

func (e *remoteObservationStageError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func remoteObservationStageFailure(stage string, err error) error {
	if err == nil {
		return nil
	}
	return &remoteObservationStageError{stage: safeRemoteObservationStage(stage), cause: err}
}

func safeRemoteObservationStage(stage string) string {
	switch stage {
	case "initial_job", "command", "events", "gap_command_refresh", "final_job":
		return stage
	default:
		return "not_applicable"
	}
}

func remoteObservationErrorStage(err error) string {
	var observation *remoteObservationStageError
	if errors.As(err, &observation) && observation != nil {
		return safeRemoteObservationStage(observation.stage)
	}
	return "not_applicable"
}

func remoteObservationErrorCategory(err error) string {
	switch {
	case errors.Is(err, ErrRemoteObservationTimeout):
		return "timeout"
	case errors.Is(err, sshclient.ErrTransport):
		return "transport"
	case errors.Is(err, ErrRemoteRouteUnavailable):
		return "route_unavailable"
	case errors.Is(err, store.ErrRemoteProjectionInvalid), errors.Is(err, store.ErrRemoteProjectionConflict):
		return "projection_invalid"
	case errors.Is(err, ErrRemoteEventHistoryUnavailable):
		return "event_history_unavailable"
	case errors.Is(err, ErrRemoteEventCursor):
		return "event_cursor_invalid"
	case errors.Is(err, ErrRemoteTerminalUnconfirmed):
		return "terminal_unconfirmed"
	case errors.Is(err, ErrRemoteEventStream), errors.Is(err, ErrRemoteEventCallerUnavailable):
		return "event_stream"
	case errors.Is(err, ErrRemoteDriverConfiguration):
		return "configuration"
	case errors.Is(err, ErrRemoteResponse):
		return "invalid_response"
	default:
		return "observation_failed"
	}
}

func safeRemoteObservationCategory(category string) string {
	switch category {
	case "timeout", "transport", "route_unavailable", "projection_invalid", "event_history_unavailable",
		"event_cursor_invalid", "terminal_unconfirmed", "event_stream", "configuration", "invalid_response", "observation_failed":
		return category
	default:
		return "not_applicable"
	}
}

// The aggregate deadline is metadata, not a cancellation of the caller's
// context. Only read-only transport calls derive a cancellable child from it;
// local failure/staleness bookkeeping must remain possible after a read times
// out. The original caller's cancellation still propagates normally.
func withRemoteObservationDeadline(ctx context.Context, deadline time.Time) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if current, ok := ctx.Value(remoteObservationDeadlineKey{}).(time.Time); ok && current.Before(deadline) {
		deadline = current
	}
	return context.WithValue(ctx, remoteObservationDeadlineKey{}, deadline)
}

func (d *RemoteDriver) observationTimeout() time.Duration {
	if d.observations.Timeout > 0 {
		return d.observations.Timeout
	}
	return DefaultRemoteObservationTimeout
}

func (d *RemoteDriver) observationBatchTimeout() time.Duration {
	if d.observations.BatchTimeout > 0 {
		return d.observations.BatchTimeout
	}
	return DefaultRemoteObservationBatchTimeout
}

// A run proof needs at most six reads (job, command, events, gap terminal
// confirmation and normalization, final job); a submit proof needs at most
// four. Keep this aggregate finite without spending every read's allowance on
// the first SSH handshake. With defaults a run proof has 90s and a submit proof
// 60s. A batch may finish one admitted proof after its 15s scheduling deadline,
// at most 105s for runs plus local bookkeeping. Parent cancellation still
// bounds every child read.
func (d *RemoteDriver) withObservationProofBudget(ctx context.Context, maximumReads int) context.Context {
	const maximumDuration = time.Duration(1<<63 - 1)
	budget := maximumDuration
	if d.observationTimeout() <= maximumDuration/time.Duration(maximumReads) {
		budget = d.observationTimeout() * time.Duration(maximumReads)
	}
	return withRemoteObservationDeadline(ctx, time.Now().Add(budget))
}

func (d *RemoteDriver) observationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	deadline := time.Now().Add(d.observationTimeout())
	if aggregate, ok := ctx.Value(remoteObservationDeadlineKey{}).(time.Time); ok && aggregate.Before(deadline) {
		deadline = aggregate
	}
	return context.WithDeadline(ctx, deadline)
}

func remoteObservationParentError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return nil
}

func remoteObservationResult(ctx, readCtx context.Context, err error) error {
	if parentErr := remoteObservationParentError(ctx); parentErr != nil {
		return parentErr
	}
	deadline, hasDeadline := readCtx.Deadline()
	if errors.Is(readCtx.Err(), context.DeadlineExceeded) || (hasDeadline && !time.Now().Before(deadline)) {
		return errors.Join(ErrRemoteObservationTimeout, context.DeadlineExceeded, err)
	}
	return err
}

func (d *RemoteDriver) observeRemoteCall(ctx context.Context, caller RemoteCaller, request sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
	switch request.Operation {
	case sshbridge.OperationPing, sshbridge.OperationGetJob, sshbridge.OperationGetSession, sshbridge.OperationGetCommand:
	default:
		return sshbridge.ReplyFrame{}, ErrRemoteDriverConfiguration
	}
	if request.ResourceID != "" || request.IdempotencyKey != "" {
		return sshbridge.ReplyFrame{}, ErrRemoteDriverConfiguration
	}
	readCtx, cancel := d.observationContext(ctx)
	defer cancel()
	reply, err := caller.Call(readCtx, request)
	if err = remoteObservationResult(ctx, readCtx, err); err != nil {
		return sshbridge.ReplyFrame{}, err
	}
	return reply, nil
}

func (d *RemoteDriver) observeRemoteStream(ctx context.Context, streamer RemoteEventStreamer, request sshbridge.RequestFrame, receive func(context.Context, sshbridge.ReplyFrame) error) error {
	if request.Operation != sshbridge.OperationStreamCommandEvents || request.ResourceID != "" || request.IdempotencyKey != "" {
		return ErrRemoteDriverConfiguration
	}
	readCtx, cancel := d.observationContext(ctx)
	defer cancel()
	err := streamer.Stream(readCtx, request, func(reply sshbridge.ReplyFrame) error {
		return receive(readCtx, reply)
	})
	return remoteObservationResult(ctx, readCtx, err)
}
