package dispatcher

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/sshbridge"
	"remote-session-runner/src/internal/sshclient"
	"remote-session-runner/src/internal/store"
)

var (
	ErrRemoteDriverConfiguration     = errors.New("remote dispatcher configuration is invalid")
	ErrNoRemoteDispatchWork          = errors.New("no eligible remote dispatch work")
	ErrRemoteRejected                = errors.New("remote authority rejected local intent")
	ErrRemoteUncertain               = errors.New("remote authority acceptance is uncertain")
	ErrRemoteResponse                = errors.New("remote bridge response is invalid")
	ErrRemotePayload                 = errors.New("remote bridge payload cannot be built")
	ErrRemoteNotReconciled           = errors.New("remote intent remains uncertain after reconciliation")
	ErrRemoteUncertaintyDeadline     = errors.New("remote uncertainty deadline expired")
	ErrRemoteIdempotencyDeadline     = errors.New("remote idempotency retry deadline expired")
	ErrRemoteSessionNotReady         = errors.New("remote session is not ready for command dispatch")
	ErrRemoteSessionCreateFailed     = errors.New("remote session creation failed")
	ErrRemoteCancelledBeforeDelivery = errors.New("remote intent was superseded before target delivery")
)

// RemoteUncertaintyWindow is the bounded period in which the dispatcher may
// query a target after an ambiguous mutation send. Once it expires, the
// intent remains indeterminate for a later durable/file-based decision.
const RemoteUncertaintyWindow = 24 * time.Hour

// RemoteCaller is the identity-preserving bridge operation used by the remote
// driver. sshclient.Client implements it; tests can inject a bounded caller.
type RemoteCaller interface {
	Call(context.Context, sshbridge.RequestFrame) (sshbridge.ReplyFrame, error)
}

// RemoteDriver claims remote intents and forwards them through the complete
// SSH bridge. It does not create IDs or send caller-supplied payloads: every
// frame is reconstructed from the committed immutable local intent.
type RemoteDriver struct {
	authority     *store.AuthorityStore
	caller        RemoteCaller
	owner         string
	leaseDuration time.Duration
	now           func() time.Time
	sessionMu     sync.RWMutex
	sessionStates map[domain.SessionID]domain.SessionState
}

// NewRemoteDriver constructs a lease-owning remote driver.
func NewRemoteDriver(authority *store.AuthorityStore, caller RemoteCaller, owner string, leaseDuration time.Duration) (*RemoteDriver, error) {
	return NewRemoteDriverWithClock(authority, caller, owner, leaseDuration, time.Now)
}

// NewRemoteDriverWithClock is the deterministic-clock constructor used by
// uncertainty deadline tests and controlled recovery harnesses.
func NewRemoteDriverWithClock(authority *store.AuthorityStore, caller RemoteCaller, owner string, leaseDuration time.Duration, now func() time.Time) (*RemoteDriver, error) {
	if authority == nil || caller == nil || owner == "" || len(owner) > 256 || strings.IndexByte(owner, 0) >= 0 || leaseDuration <= 0 || now == nil {
		return nil, ErrRemoteDriverConfiguration
	}
	return &RemoteDriver{authority: authority, caller: caller, owner: owner, leaseDuration: leaseDuration, now: now, sessionStates: make(map[domain.SessionID]domain.SessionState)}, nil
}

// DispatchNext selects the earliest eligible remote intent and delivers it
// with stable resource, request, idempotency, and command-ordinal identities.
func (d *RemoteDriver) DispatchNext(ctx context.Context) (store.LocalIntentRecord, sshbridge.ReplyFrame, error) {
	if d == nil || d.authority == nil || d.caller == nil {
		return store.LocalIntentRecord{}, sshbridge.ReplyFrame{}, ErrRemoteDriverConfiguration
	}
	candidates, err := d.authority.ListEligibleLocalIntents(ctx, 1000)
	if err != nil {
		return store.LocalIntentRecord{}, sshbridge.ReplyFrame{}, err
	}
	for _, candidate := range candidates {
		if candidate.Target.Kind() != domain.TargetKindRemote || !supportedRemoteOperation(candidate.Operation) {
			continue
		}
		record, reply, dispatchErr := d.dispatchIntent(ctx, candidate.IntentID)
		if errors.Is(dispatchErr, ErrRemoteSessionNotReady) {
			continue
		}
		if errors.Is(dispatchErr, ErrRemoteSessionCreateFailed) {
			continue
		}
		if errors.Is(dispatchErr, ErrRemoteCancelledBeforeDelivery) {
			continue
		}
		if errors.Is(dispatchErr, ErrRemoteIdempotencyDeadline) {
			continue
		}
		return record, reply, dispatchErr
	}
	return store.LocalIntentRecord{}, sshbridge.ReplyFrame{}, ErrNoRemoteDispatchWork
}

// DispatchIntent claims and delivers one remote intent by its stable ID.
func (d *RemoteDriver) DispatchIntent(ctx context.Context, id domain.IntentID) (store.LocalIntentRecord, sshbridge.ReplyFrame, error) {
	if d == nil || d.authority == nil || d.caller == nil {
		return store.LocalIntentRecord{}, sshbridge.ReplyFrame{}, ErrRemoteDriverConfiguration
	}
	intent, err := d.authority.GetLocalIntent(ctx, id)
	if err != nil {
		return store.LocalIntentRecord{}, sshbridge.ReplyFrame{}, err
	}
	if intent.Target.Kind() != domain.TargetKindRemote {
		return store.LocalIntentRecord{}, sshbridge.ReplyFrame{}, fmt.Errorf("%w: target %s", ErrRemotePayload, intent.Target.Kind())
	}
	if !supportedRemoteOperation(intent.Operation) {
		return store.LocalIntentRecord{}, sshbridge.ReplyFrame{}, fmt.Errorf("%w: operation %s", ErrRemotePayload, intent.Operation)
	}
	return d.dispatchIntent(ctx, intent.IntentID)
}

// ReconcileIntent queries the target authority for an intent that may have
// crossed the bridge before the response was lost. A matching target resource
// becomes accepted. If the resource is absent, the same immutable mutation is
// retried only while its persisted idempotency guarantee remains open; after
// expiry the intent remains uncertain because target metadata may have been
// collected.
func (d *RemoteDriver) ReconcileIntent(ctx context.Context, id domain.IntentID) (store.LocalIntentRecord, sshbridge.ReplyFrame, error) {
	if d == nil || d.authority == nil || d.caller == nil {
		return store.LocalIntentRecord{}, sshbridge.ReplyFrame{}, ErrRemoteDriverConfiguration
	}
	intent, err := d.authority.GetLocalIntent(ctx, id)
	if err != nil {
		return store.LocalIntentRecord{}, sshbridge.ReplyFrame{}, err
	}
	if intent.Target.Kind() != domain.TargetKindRemote || (intent.DeliveryState != store.LocalIntentDispatching && intent.DeliveryState != store.LocalIntentUncertain) {
		return intent, sshbridge.ReplyFrame{}, nil
	}
	deadline, err := d.uncertaintyDeadline(ctx, intent)
	if err != nil {
		return intent, sshbridge.ReplyFrame{}, err
	}
	if !d.now().UTC().Before(deadline) {
		return intent, sshbridge.ReplyFrame{}, ErrRemoteUncertaintyDeadline
	}
	frame, err := readFrameForRemoteIntent(intent)
	if err != nil {
		return intent, sshbridge.ReplyFrame{}, err
	}
	reply, callErr := d.caller.Call(ctx, frame)
	if callErr != nil {
		return intent, sshbridge.ReplyFrame{}, fmt.Errorf("%w: read transport: %v", ErrRemoteNotReconciled, callErr)
	}
	outcome, err := reconcileRemoteReply(intent, frame, reply)
	if err != nil {
		return intent, reply, err
	}
	switch outcome {
	case reconcileAccepted:
		if err := d.upsertRemoteProjectionFromReply(ctx, intent, reply); err != nil {
			return intent, reply, err
		}
		if intent.Operation == operationCreateSession {
			if err := d.observeRemoteSessionState(intent, reply); err != nil {
				return intent, reply, err
			}
		}
		accepted, transitionErr := d.authority.TransitionLocalIntent(ctx, intent.IntentID, store.LocalIntentAccepted, "remote_reconciled_target_found")
		if transitionErr != nil {
			return intent, reply, transitionErr
		}
		return accepted, reply, nil
	case reconcileNotDelivered:
		if _, deadlineErr := d.ensureRemoteRetryWindow(ctx, intent); deadlineErr != nil {
			return intent, reply, deadlineErr
		}
		return d.retryRemoteIntent(ctx, intent)
	default:
		return intent, reply, ErrRemoteNotReconciled
	}
}

func (d *RemoteDriver) ensureRemoteRetryWindow(ctx context.Context, intent store.LocalIntentRecord) (time.Time, error) {
	deadline, found, err := d.authority.LocalIntentIdempotencyExpiry(ctx, intent.IntentID)
	if err != nil {
		return time.Time{}, err
	}
	if !found {
		return time.Time{}, fmt.Errorf("%w: local idempotency expiry is unavailable", ErrRemoteIdempotencyDeadline)
	}
	if !d.now().UTC().Before(deadline) {
		return deadline, fmt.Errorf("%w: key guarantee expired at %s", ErrRemoteIdempotencyDeadline, deadline.UTC().Format(time.RFC3339Nano))
	}
	return deadline, nil
}

func (d *RemoteDriver) retryRemoteIntent(ctx context.Context, intent store.LocalIntentRecord) (store.LocalIntentRecord, sshbridge.ReplyFrame, error) {
	frame, err := frameForRemoteIntent(intent)
	if err != nil {
		return intent, sshbridge.ReplyFrame{}, err
	}
	reply, callErr := d.caller.Call(ctx, frame)
	if callErr != nil {
		return intent, sshbridge.ReplyFrame{}, fmt.Errorf("%w: same-key retry transport: %v", ErrRemoteNotReconciled, callErr)
	}
	if err := validateRemoteReply(intent, frame, reply); err != nil {
		if errors.Is(err, ErrRemoteRejected) {
			notDelivered, transitionErr := d.authority.TransitionLocalIntent(ctx, intent.IntentID, store.LocalIntentNotDelivered, "remote_retry_rejected")
			if transitionErr != nil {
				return intent, reply, transitionErr
			}
			return notDelivered, reply, err
		}
		return intent, reply, err
	}
	if intent.Operation == operationCreateSession {
		if err := d.observeRemoteSessionState(intent, reply); err != nil {
			return intent, reply, err
		}
	}
	if err := d.upsertRemoteProjectionFromReply(ctx, intent, reply); err != nil {
		return intent, reply, err
	}
	accepted, err := d.authority.TransitionLocalIntent(ctx, intent.IntentID, store.LocalIntentAccepted, "remote_target_accepted_after_retry")
	if err != nil {
		return intent, reply, err
	}
	return accepted, reply, nil
}

func (d *RemoteDriver) uncertaintyDeadline(ctx context.Context, intent store.LocalIntentRecord) (time.Time, error) {
	start := intent.UpdatedAt
	if intent.DeliveryState == store.LocalIntentUncertain {
		lifecycle, err := d.authority.ListLocalIntentLifecycle(ctx, intent.IntentID)
		if err != nil {
			return time.Time{}, err
		}
		for index := len(lifecycle) - 1; index >= 0; index-- {
			if lifecycle[index].NewState == store.LocalIntentUncertain {
				start = lifecycle[index].OccurredAt
				break
			}
		}
	}
	if start.IsZero() {
		return time.Time{}, fmt.Errorf("%w: missing uncertainty start", ErrRemoteNotReconciled)
	}
	return start.UTC().Add(RemoteUncertaintyWindow), nil
}

// ensureRemoteSubmitReady gates every remote submit mutation on the durable
// create intent and the target's current session state. A missing or
// in-progress create is a wait, never permission to send a command early.
func (d *RemoteDriver) ensureRemoteSubmitReady(ctx context.Context, intent store.LocalIntentRecord) (store.LocalIntentRecord, error) {
	create, err := d.authority.GetLocalIntentByResource(ctx, operationCreateSession, string(intent.SessionID), intent.Controller)
	if err != nil {
		if errors.Is(err, store.ErrLocalIntentNotFound) {
			// A command may target a pre-existing session whose create intent is
			// outside this authority store. Preserve that established path; the
			// early-create gate applies when this controller has a recorded
			// create intent to observe.
			return intent, nil
		}
		return intent, err
	}
	switch create.DeliveryState {
	case store.LocalIntentNotDelivered:
		return intent, fmt.Errorf("%w: create intent %s is not delivered", ErrRemoteSessionCreateFailed, create.IntentID)
	case store.LocalIntentAccepted, store.LocalIntentReconciled:
		// Continue with the target state check below.
	default:
		return intent, fmt.Errorf("%w: create intent state %s", ErrRemoteSessionNotReady, create.DeliveryState)
	}

	state, known := d.cachedRemoteSessionState(create.SessionID)
	if known && state.IsTerminal() {
		return intent, fmt.Errorf("%w: session %s is %s", ErrRemoteSessionCreateFailed, create.SessionID, state)
	}
	if !known || state != domain.SessionStateReady {
		refreshed, refreshErr := d.refreshRemoteSessionState(ctx, create)
		if refreshErr != nil {
			// A read that cannot prove readiness is still a wait. The command
			// remains recorded and no target mutation is attempted.
			if errors.Is(refreshErr, ErrRemoteSessionCreateFailed) {
				return intent, refreshErr
			}
			return intent, fmt.Errorf("%w: %v", ErrRemoteSessionNotReady, refreshErr)
		}
		state = refreshed
	}
	if state == domain.SessionStateReady {
		return intent, nil
	}
	if state.IsTerminal() {
		return intent, fmt.Errorf("%w: session %s is %s", ErrRemoteSessionCreateFailed, create.SessionID, state)
	}
	return intent, fmt.Errorf("%w: session %s is %s", ErrRemoteSessionNotReady, create.SessionID, state)
}

func (d *RemoteDriver) markRemoteSubmitNotDelivered(ctx context.Context, intent store.LocalIntentRecord, reason string, cause error) (store.LocalIntentRecord, sshbridge.ReplyFrame, error) {
	if intent.DeliveryState == store.LocalIntentRecorded {
		updated, err := d.authority.TransitionLocalIntent(ctx, intent.IntentID, store.LocalIntentNotDelivered, reason)
		if err != nil {
			return intent, sshbridge.ReplyFrame{}, err
		}
		return updated, sshbridge.ReplyFrame{}, cause
	}
	return intent, sshbridge.ReplyFrame{}, cause
}

func (d *RemoteDriver) cachedRemoteSessionState(sessionID domain.SessionID) (domain.SessionState, bool) {
	d.sessionMu.RLock()
	state, ok := d.sessionStates[sessionID]
	d.sessionMu.RUnlock()
	return state, ok
}

func (d *RemoteDriver) rememberRemoteSessionState(sessionID domain.SessionID, state domain.SessionState) {
	d.sessionMu.Lock()
	d.sessionStates[sessionID] = state
	d.sessionMu.Unlock()
}

func (d *RemoteDriver) observeRemoteSessionState(intent store.LocalIntentRecord, reply sshbridge.ReplyFrame) error {
	state, present, err := remoteSessionStateFromResult(reply.Payload)
	if err != nil {
		return err
	}
	if present {
		d.rememberRemoteSessionState(intent.SessionID, state)
	}
	return nil
}

func (d *RemoteDriver) refreshRemoteSessionState(ctx context.Context, create store.LocalIntentRecord) (domain.SessionState, error) {
	frame, err := readinessFrameForCreateIntent(create)
	if err != nil {
		return "", err
	}
	reply, callErr := d.caller.Call(ctx, frame)
	if callErr != nil {
		return "", callErr
	}
	state, err := validateRemoteSessionReadReply(create, frame, reply)
	if err != nil {
		return "", err
	}
	d.rememberRemoteSessionState(create.SessionID, state)
	return state, nil
}

func (d *RemoteDriver) dispatchIntent(ctx context.Context, id domain.IntentID) (store.LocalIntentRecord, sshbridge.ReplyFrame, error) {
	intent, err := d.authority.GetLocalIntent(ctx, id)
	if err != nil {
		return store.LocalIntentRecord{}, sshbridge.ReplyFrame{}, err
	}
	guarded, guardErr := d.guardRemoteIntentBeforeDelivery(ctx, intent)
	if guardErr != nil {
		return guarded, sshbridge.ReplyFrame{}, guardErr
	}
	if intent.DeliveryState == store.LocalIntentRecorded {
		if _, deadlineErr := d.ensureRemoteRetryWindow(ctx, intent); deadlineErr != nil {
			if !errors.Is(deadlineErr, ErrRemoteIdempotencyDeadline) {
				return intent, sshbridge.ReplyFrame{}, deadlineErr
			}
			updated, transitionErr := d.authority.TransitionLocalIntent(ctx, intent.IntentID, store.LocalIntentNotDelivered, "remote_idempotency_expired_before_dispatch")
			if transitionErr != nil {
				return intent, sshbridge.ReplyFrame{}, transitionErr
			}
			return updated, sshbridge.ReplyFrame{}, deadlineErr
		}
	}
	if intent.Operation == operationSubmitCommand {
		ready, readinessErr := d.ensureRemoteSubmitReady(ctx, intent)
		if readinessErr != nil {
			if errors.Is(readinessErr, ErrRemoteSessionCreateFailed) {
				return d.markRemoteSubmitNotDelivered(ctx, ready, "remote_session_create_failed", readinessErr)
			}
			return ready, sshbridge.ReplyFrame{}, readinessErr
		}
	}
	frame, err := frameForRemoteIntent(intent)
	if err != nil {
		return store.LocalIntentRecord{}, sshbridge.ReplyFrame{}, err
	}
	claimed, err := d.authority.ClaimLocalIntent(ctx, intent.IntentID, d.owner, d.leaseDuration)
	if err != nil {
		return store.LocalIntentRecord{}, sshbridge.ReplyFrame{}, err
	}
	reply, callErr := d.caller.Call(ctx, frame)
	if callErr != nil {
		next := store.LocalIntentUncertain
		reason := "remote_transport_uncertain"
		var transportErr *sshclient.TransportError
		if errors.As(callErr, &transportErr) && transportErr.Phase == sshclient.PhaseBeforeSend {
			next = store.LocalIntentNotDelivered
			reason = "remote_transport_before_send"
		}
		_, transitionErr := d.authority.TransitionLocalIntent(ctx, claimed.IntentID, next, reason)
		if transitionErr != nil {
			return store.LocalIntentRecord{}, sshbridge.ReplyFrame{}, fmt.Errorf("%w; transition: %v", callErr, transitionErr)
		}
		return claimed, sshbridge.ReplyFrame{}, callErr
	}
	if err := validateRemoteReply(intent, frame, reply); err != nil {
		next := store.LocalIntentUncertain
		reason := "remote_response_uncertain"
		if errors.Is(err, ErrRemoteRejected) {
			next = store.LocalIntentNotDelivered
			reason = "remote_rejected"
		}
		_, transitionErr := d.authority.TransitionLocalIntent(ctx, claimed.IntentID, next, reason)
		if transitionErr != nil {
			return store.LocalIntentRecord{}, sshbridge.ReplyFrame{}, fmt.Errorf("%w; transition: %v", err, transitionErr)
		}
		return claimed, reply, err
	}
	if intent.Operation == operationCreateSession {
		if err := d.observeRemoteSessionState(intent, reply); err != nil {
			_, transitionErr := d.authority.TransitionLocalIntent(ctx, claimed.IntentID, store.LocalIntentUncertain, "remote_response_uncertain")
			if transitionErr != nil {
				return store.LocalIntentRecord{}, sshbridge.ReplyFrame{}, fmt.Errorf("%w; transition: %v", err, transitionErr)
			}
			return claimed, reply, err
		}
	}
	if err := d.upsertRemoteProjectionFromReply(ctx, intent, reply); err != nil {
		_, transitionErr := d.authority.TransitionLocalIntent(ctx, claimed.IntentID, store.LocalIntentUncertain, "remote_response_uncertain")
		if transitionErr != nil {
			return store.LocalIntentRecord{}, sshbridge.ReplyFrame{}, fmt.Errorf("%w; transition: %v", err, transitionErr)
		}
		return claimed, reply, err
	}
	accepted, err := d.authority.TransitionLocalIntent(ctx, claimed.IntentID, store.LocalIntentAccepted, "remote_target_accepted")
	if err != nil {
		return store.LocalIntentRecord{}, sshbridge.ReplyFrame{}, err
	}
	return accepted, reply, nil
}

// guardRemoteIntentBeforeDelivery applies local desired-state controls before
// any target mutation. A recorded predecessor has never crossed the bridge,
// so a recorded cancel/close can prove that predecessor not-delivered and
// unblock later ordinals without inventing remote state.
func (d *RemoteDriver) guardRemoteIntentBeforeDelivery(ctx context.Context, intent store.LocalIntentRecord) (store.LocalIntentRecord, error) {
	if intent.DeliveryState != store.LocalIntentRecorded {
		return intent, nil
	}
	markNotDelivered := func(id domain.IntentID, reason string) (store.LocalIntentRecord, error) {
		return d.authority.TransitionLocalIntent(ctx, id, store.LocalIntentNotDelivered, reason)
	}
	activeControl := func(control store.LocalIntentRecord) bool {
		return control.DeliveryState != store.LocalIntentNotDelivered
	}

	switch intent.Operation {
	case operationCreateSession:
		closeIntent, err := d.authority.GetLocalIntentByResource(ctx, operationCloseSession, string(intent.SessionID), intent.Controller)
		if errors.Is(err, store.ErrLocalIntentNotFound) {
			return intent, nil
		}
		if err != nil {
			return intent, err
		}
		if activeControl(closeIntent) {
			updated, transitionErr := markNotDelivered(intent.IntentID, "closed_before_dispatch")
			if transitionErr != nil {
				return intent, transitionErr
			}
			return updated, fmt.Errorf("%w: close requested", ErrRemoteCancelledBeforeDelivery)
		}
	case operationSubmitCommand:
		controlFound := false
		controlReason := "cancelled_before_dispatch"
		cancelIntent, err := d.authority.GetLocalIntentByResource(ctx, operationCancelCommand, string(intent.CommandID), intent.Controller)
		if err == nil {
			controlFound = activeControl(cancelIntent)
		} else if !errors.Is(err, store.ErrLocalIntentNotFound) {
			return intent, err
		}
		if !controlFound {
			closeIntent, closeErr := d.authority.GetLocalIntentByResource(ctx, operationCloseSession, string(intent.SessionID), intent.Controller)
			if closeErr == nil {
				controlFound = activeControl(closeIntent)
				controlReason = "closed_before_dispatch"
			} else if !errors.Is(closeErr, store.ErrLocalIntentNotFound) {
				return intent, closeErr
			}
		}
		if controlFound {
			updated, transitionErr := markNotDelivered(intent.IntentID, controlReason)
			if transitionErr != nil {
				return intent, transitionErr
			}
			return updated, fmt.Errorf("%w: control intent exists", ErrRemoteCancelledBeforeDelivery)
		}
	case operationCancelCommand:
		submitIntent, err := d.authority.GetLocalIntentByResource(ctx, operationSubmitCommand, string(intent.CommandID), intent.Controller)
		if errors.Is(err, store.ErrLocalIntentNotFound) {
			updated, transitionErr := markNotDelivered(intent.IntentID, "command_not_delivered")
			if transitionErr != nil {
				return intent, transitionErr
			}
			return updated, fmt.Errorf("%w: submit intent missing", ErrRemoteCancelledBeforeDelivery)
		}
		if err != nil {
			return intent, err
		}
		if submitIntent.DeliveryState == store.LocalIntentRecorded {
			if _, transitionErr := markNotDelivered(submitIntent.IntentID, "cancelled_before_dispatch"); transitionErr != nil {
				return intent, transitionErr
			}
			updated, transitionErr := markNotDelivered(intent.IntentID, "command_not_delivered")
			if transitionErr != nil {
				return intent, transitionErr
			}
			return updated, fmt.Errorf("%w: submit intent was recorded only", ErrRemoteCancelledBeforeDelivery)
		}
		if submitIntent.DeliveryState == store.LocalIntentNotDelivered {
			updated, transitionErr := markNotDelivered(intent.IntentID, "command_not_delivered")
			if transitionErr != nil {
				return intent, transitionErr
			}
			return updated, fmt.Errorf("%w: submit intent was not delivered", ErrRemoteCancelledBeforeDelivery)
		}
	case operationCloseSession:
		createIntent, err := d.authority.GetLocalIntentByResource(ctx, operationCreateSession, string(intent.SessionID), intent.Controller)
		if errors.Is(err, store.ErrLocalIntentNotFound) {
			updated, transitionErr := markNotDelivered(intent.IntentID, "session_not_delivered")
			if transitionErr != nil {
				return intent, transitionErr
			}
			return updated, fmt.Errorf("%w: create intent missing", ErrRemoteCancelledBeforeDelivery)
		}
		if err != nil {
			return intent, err
		}
		if createIntent.DeliveryState == store.LocalIntentRecorded {
			if _, transitionErr := markNotDelivered(createIntent.IntentID, "closed_before_dispatch"); transitionErr != nil {
				return intent, transitionErr
			}
			updated, transitionErr := markNotDelivered(intent.IntentID, "session_not_delivered")
			if transitionErr != nil {
				return intent, transitionErr
			}
			return updated, fmt.Errorf("%w: create intent was recorded only", ErrRemoteCancelledBeforeDelivery)
		}
		if createIntent.DeliveryState == store.LocalIntentNotDelivered {
			updated, transitionErr := markNotDelivered(intent.IntentID, "session_not_delivered")
			if transitionErr != nil {
				return intent, transitionErr
			}
			return updated, fmt.Errorf("%w: create intent was not delivered", ErrRemoteCancelledBeforeDelivery)
		}
	}
	return intent, nil
}

func supportedRemoteOperation(operation string) bool {
	switch operation {
	case operationCreateSession, operationSubmitCommand, operationCancelCommand, operationCloseSession, "run":
		return true
	default:
		return false
	}
}

func frameForRemoteIntent(intent store.LocalIntentRecord) (sshbridge.RequestFrame, error) {
	payload, err := bridgePayloadForIntent(intent)
	if err != nil {
		return sshbridge.RequestFrame{}, err
	}
	var operation sshbridge.Operation
	switch intent.Operation {
	case operationCreateSession:
		operation = sshbridge.OperationCreateOrResumeSession
	case operationSubmitCommand:
		operation = sshbridge.OperationSubmitOrResumeCommand
	case operationCancelCommand:
		operation = sshbridge.OperationCancelCommand
	case operationCloseSession:
		operation = sshbridge.OperationCloseSession
	case "run":
		operation = sshbridge.OperationRunOrResumeJob
	default:
		return sshbridge.RequestFrame{}, fmt.Errorf("%w: unsupported operation %q", ErrRemotePayload, intent.Operation)
	}
	return sshbridge.RequestFrame{
		ProtocolVersion: sshbridge.ProtocolVersion,
		RequestID:       string(intent.IntentID),
		Operation:       operation,
		ResourceID:      intent.ResourceID,
		IdempotencyKey:  intent.IdempotencyKey,
		Payload:         payload,
	}, nil
}

func readFrameForRemoteIntent(intent store.LocalIntentRecord) (sshbridge.RequestFrame, error) {
	var operation sshbridge.Operation
	var resourceField string
	var resourceID string
	switch intent.Operation {
	case operationCreateSession, operationCloseSession:
		operation = sshbridge.OperationGetSession
		resourceField = "session_id"
		resourceID = string(intent.SessionID)
	case operationSubmitCommand, operationCancelCommand:
		operation = sshbridge.OperationGetCommand
		resourceField = "command_id"
		resourceID = string(intent.CommandID)
	case "run":
		operation = sshbridge.OperationGetJob
		resourceField = "job_id"
		resourceID = string(intent.JobID)
	default:
		return sshbridge.RequestFrame{}, fmt.Errorf("%w: unsupported reconciliation operation %q", ErrRemotePayload, intent.Operation)
	}
	value, _ := json.Marshal(resourceID)
	payload, _ := json.Marshal(map[string]json.RawMessage{resourceField: value})
	return sshbridge.RequestFrame{ProtocolVersion: sshbridge.ProtocolVersion, RequestID: string(intent.IntentID) + "/reconcile", Operation: operation, Payload: payload}, nil
}

func readinessFrameForCreateIntent(intent store.LocalIntentRecord) (sshbridge.RequestFrame, error) {
	frame, err := readFrameForRemoteIntent(intent)
	if err != nil {
		return sshbridge.RequestFrame{}, err
	}
	frame.RequestID = string(intent.IntentID) + "/readiness"
	frame.ResourceID = intent.ResourceID
	return frame, nil
}

func remoteSessionStateFromResult(payload []byte) (domain.SessionState, bool, error) {
	var object map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&object); err != nil || object == nil {
		return "", false, fmt.Errorf("%w: session result object", ErrRemoteResponse)
	}
	raw, ok := object["session_state"]
	if !ok {
		return "", false, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || !domain.SessionState(value).Valid() {
		return "", false, fmt.Errorf("%w: invalid session_state", ErrRemoteResponse)
	}
	return domain.SessionState(value), true, nil
}

func validateRemoteSessionReadReply(create store.LocalIntentRecord, frame sshbridge.RequestFrame, reply sshbridge.ReplyFrame) (domain.SessionState, error) {
	if reply.ProtocolVersion != sshbridge.ProtocolVersion || reply.RequestID != frame.RequestID {
		return "", fmt.Errorf("%w: readiness protocol or request identity mismatch", ErrRemoteSessionNotReady)
	}
	if reply.ResponseType == "error" {
		var payload sshbridge.ErrorPayload
		decoder := json.NewDecoder(bytes.NewReader(reply.Payload))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&payload); err != nil {
			return "", fmt.Errorf("%w: readiness error payload: %v", ErrRemoteSessionNotReady, err)
		}
		if payload.Code == "resource_not_found" {
			return "", fmt.Errorf("%w: session resource not found", ErrRemoteSessionCreateFailed)
		}
		return "", fmt.Errorf("%w: %s", ErrRemoteSessionNotReady, payload.Message)
	}
	if reply.ResponseType != "result" {
		return "", fmt.Errorf("%w: readiness response type %q", ErrRemoteSessionNotReady, reply.ResponseType)
	}
	var object map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(reply.Payload))
	if err := decoder.Decode(&object); err != nil || object == nil {
		return "", fmt.Errorf("%w: readiness result object", ErrRemoteSessionNotReady)
	}
	readString := func(name, expected string) error {
		var value string
		raw, ok := object[name]
		if !ok || json.Unmarshal(raw, &value) != nil || value != expected {
			return fmt.Errorf("%w: %s mismatch", ErrRemoteSessionNotReady, name)
		}
		return nil
	}
	if err := readString("session_id", string(create.SessionID)); err != nil {
		return "", err
	}
	if err := readString("environment", create.Environment); err != nil {
		return "", err
	}
	if err := validateRemoteTarget(object); err != nil {
		return "", fmt.Errorf("%w: %v", ErrRemoteSessionNotReady, err)
	}
	state, present, err := remoteSessionStateFromResult(reply.Payload)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrRemoteSessionNotReady, err)
	}
	if !present {
		return "", fmt.Errorf("%w: session_state missing", ErrRemoteSessionNotReady)
	}
	return state, nil
}

func bridgePayloadForIntent(intent store.LocalIntentRecord) (json.RawMessage, error) {
	var source map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(intent.PayloadJSON))
	if err := decoder.Decode(&source); err != nil || source == nil {
		return nil, fmt.Errorf("%w: payload object", ErrRemotePayload)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("%w: payload trailing JSON", ErrRemotePayload)
	}
	result := make(map[string]json.RawMessage)
	copyField := func(name string) error {
		if value, ok := source[name]; ok {
			result[name] = append(json.RawMessage(nil), value...)
		}
		return nil
	}
	required := func(name string) error {
		if _, ok := source[name]; !ok {
			return fmt.Errorf("%w: missing %s", ErrRemotePayload, name)
		}
		return copyField(name)
	}
	switch intent.Operation {
	case operationCreateSession:
		if err := required("environment"); err != nil {
			return nil, err
		}
		if err := required("execution_target"); err != nil {
			return nil, err
		}
		for _, name := range []string{"source", "limits", "isolation"} {
			if err := copyField(name); err != nil {
				return nil, err
			}
		}
	case operationSubmitCommand:
		if intent.IntentOrdinal == nil || *intent.IntentOrdinal <= 0 {
			return nil, fmt.Errorf("%w: submit intent ordinal", ErrRemotePayload)
		}
		result["session_id"] = json.RawMessage(strconvQuote(string(intent.SessionID)))
		ordinal, _ := json.Marshal(*intent.IntentOrdinal)
		result["intent_ordinal"] = ordinal
		script, _ := json.Marshal(string(intent.ScriptBytes))
		result["script"] = script
		if err := copyField("timeout_seconds"); err != nil {
			return nil, err
		}
	case operationCancelCommand:
		commandID, _ := json.Marshal(string(intent.CommandID))
		result["command_id"] = commandID
	case operationCloseSession:
		sessionID, _ := json.Marshal(string(intent.SessionID))
		result["session_id"] = sessionID
		if err := copyField("policy"); err != nil {
			return nil, err
		}
	case "run":
		for _, name := range []string{"environment", "execution_target"} {
			if err := required(name); err != nil {
				return nil, err
			}
		}
		for _, name := range []string{"source", "limits", "isolation", "policy"} {
			if err := copyField(name); err != nil {
				return nil, err
			}
		}
		sessionID, _ := json.Marshal(string(intent.SessionID))
		commandID, _ := json.Marshal(string(intent.CommandID))
		result["session_id"] = sessionID
		result["command_id"] = commandID
		script, _ := json.Marshal(string(intent.ScriptBytes))
		result["script"] = script
	default:
		return nil, fmt.Errorf("%w: unsupported operation %q", ErrRemotePayload, intent.Operation)
	}
	serialized, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("%w: encode: %v", ErrRemotePayload, err)
	}
	return serialized, nil
}

func strconvQuote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func validateRemoteReply(intent store.LocalIntentRecord, frame sshbridge.RequestFrame, reply sshbridge.ReplyFrame) error {
	if reply.ProtocolVersion != sshbridge.ProtocolVersion || reply.RequestID != frame.RequestID {
		return fmt.Errorf("%w: protocol or request identity mismatch", ErrRemoteResponse)
	}
	if reply.ResponseType == "error" {
		var payload sshbridge.ErrorPayload
		decoder := json.NewDecoder(bytes.NewReader(reply.Payload))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&payload); err != nil {
			return fmt.Errorf("%w: error payload: %v", ErrRemoteResponse, err)
		}
		if payload.Code == "transport_uncertain" {
			return fmt.Errorf("%w: %s", ErrRemoteUncertain, payload.Message)
		}
		return fmt.Errorf("%w: %s", ErrRemoteRejected, payload.Message)
	}
	if reply.ResponseType != "result" {
		return fmt.Errorf("%w: response type %q", ErrRemoteResponse, reply.ResponseType)
	}
	var object map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(reply.Payload))
	if err := decoder.Decode(&object); err != nil || object == nil {
		return fmt.Errorf("%w: result object", ErrRemoteResponse)
	}
	field := ""
	switch intent.Operation {
	case operationCreateSession:
		field = "session_id"
	case operationSubmitCommand, operationCancelCommand:
		field = "command_id"
	case operationCloseSession:
		field = "session_id"
	case "run":
		field = "job_id"
	}
	var resourceID string
	if err := json.Unmarshal(object[field], &resourceID); err != nil || resourceID != intent.ResourceID {
		return fmt.Errorf("%w: %s identity", ErrRemoteResponse, field)
	}
	if intent.Operation == operationSubmitCommand && intent.IntentOrdinal != nil {
		var ordinal int64
		if raw, ok := object["ordinal"]; !ok || json.Unmarshal(raw, &ordinal) != nil || ordinal != *intent.IntentOrdinal {
			return fmt.Errorf("%w: command ordinal mismatch", ErrRemoteResponse)
		}
	}
	return nil
}

type reconcileResult string

const (
	reconcileAccepted     reconcileResult = "accepted"
	reconcileNotDelivered reconcileResult = "not_delivered"
)

func reconcileRemoteReply(intent store.LocalIntentRecord, frame sshbridge.RequestFrame, reply sshbridge.ReplyFrame) (reconcileResult, error) {
	if reply.ProtocolVersion != sshbridge.ProtocolVersion || reply.RequestID != frame.RequestID {
		return "", fmt.Errorf("%w: reconciliation protocol or request identity mismatch", ErrRemoteNotReconciled)
	}
	if reply.ResponseType == "error" {
		var payload sshbridge.ErrorPayload
		decoder := json.NewDecoder(bytes.NewReader(reply.Payload))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&payload); err != nil {
			return "", fmt.Errorf("%w: reconciliation error payload: %v", ErrRemoteNotReconciled, err)
		}
		if payload.Code == "resource_not_found" {
			return reconcileNotDelivered, nil
		}
		return "", fmt.Errorf("%w: %s", ErrRemoteNotReconciled, payload.Message)
	}
	if reply.ResponseType != "result" {
		return "", fmt.Errorf("%w: reconciliation response type %q", ErrRemoteNotReconciled, reply.ResponseType)
	}
	var object map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(reply.Payload))
	if err := decoder.Decode(&object); err != nil || object == nil {
		return "", fmt.Errorf("%w: reconciliation result object", ErrRemoteNotReconciled)
	}
	if err := validateReconciledResource(intent, object); err != nil {
		return "", err
	}
	return reconcileAccepted, nil
}

func validateReconciledResource(intent store.LocalIntentRecord, object map[string]json.RawMessage) error {
	readString := func(name, expected string) error {
		var value string
		raw, ok := object[name]
		if !ok || json.Unmarshal(raw, &value) != nil || value != expected {
			return fmt.Errorf("%w: %s mismatch", ErrRemoteNotReconciled, name)
		}
		return nil
	}
	switch intent.Operation {
	case operationCreateSession:
		if err := readString("session_id", string(intent.SessionID)); err != nil {
			return err
		}
		if err := readString("environment", intent.Environment); err != nil {
			return err
		}
		return validateRemoteTarget(object)
	case operationSubmitCommand:
		if err := readString("command_id", string(intent.CommandID)); err != nil {
			return err
		}
		if err := readString("session_id", string(intent.SessionID)); err != nil {
			return err
		}
		if intent.IntentOrdinal == nil {
			return fmt.Errorf("%w: missing local command ordinal", ErrRemoteNotReconciled)
		}
		var ordinal int64
		if raw, ok := object["ordinal"]; !ok || json.Unmarshal(raw, &ordinal) != nil || ordinal != *intent.IntentOrdinal {
			return fmt.Errorf("%w: ordinal mismatch", ErrRemoteNotReconciled)
		}
		var scriptHash string
		digest := sha256.Sum256(intent.ScriptBytes)
		expectedHash := hex.EncodeToString(digest[:])
		if raw, ok := object["script_sha256"]; !ok || json.Unmarshal(raw, &scriptHash) != nil || scriptHash != expectedHash {
			return fmt.Errorf("%w: script hash mismatch", ErrRemoteNotReconciled)
		}
		return nil
	case operationCancelCommand:
		return readString("command_id", string(intent.CommandID))
	case operationCloseSession:
		return readString("session_id", string(intent.SessionID))
	case "run":
		if err := readString("job_id", string(intent.JobID)); err != nil {
			return err
		}
		if err := readString("session_id", string(intent.SessionID)); err != nil {
			return err
		}
		if err := readString("command_id", string(intent.CommandID)); err != nil {
			return err
		}
		return validateRemoteTarget(object)
	default:
		return fmt.Errorf("%w: operation %s", ErrRemoteNotReconciled, intent.Operation)
	}
}

func validateRemoteTarget(object map[string]json.RawMessage) error {
	var target struct {
		Kind    string `json:"kind"`
		Profile string `json:"profile"`
	}
	raw, ok := object["execution_target"]
	if !ok || json.Unmarshal(raw, &target) != nil || target.Kind != string(domain.TargetKindRemote) || target.Profile == "" {
		return fmt.Errorf("%w: execution target mismatch", ErrRemoteNotReconciled)
	}
	return nil
}
