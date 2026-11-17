package dispatcher

import (
	"context"
	"errors"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/sshbridge"
	"remote-session-runner/src/internal/store"
)

// ReconcileUncertainRemoteIntents observes a rotating, bounded set of uncertain
// remote one-off runs with expired delivery leases. Abandoned dispatching
// attempts are excluded. Every transport operation is a GET: missing resources
// remain uncertain and are never submitted again by this automatic path.
func (d *RemoteDriver) ReconcileUncertainRemoteIntents(ctx context.Context, limit int) error {
	if !d.configured() {
		return ErrRemoteDriverConfiguration
	}
	d.reconcileMu.Lock()
	defer d.reconcileMu.Unlock()
	intents, err := d.authority.ListRecoverableRemoteIntentsAfter(ctx, limit, d.uncertainCursor)
	if err != nil {
		return err
	}
	if len(intents) == 0 && d.uncertainCursor != nil {
		d.uncertainCursor = nil
		intents, err = d.authority.ListRecoverableRemoteIntentsAfter(ctx, limit, nil)
		if err != nil {
			return err
		}
	}
	deadline := time.Now().Add(d.observationBatchTimeout())
	observationCtx := withRemoteObservationDeadline(ctx, deadline)
	var result error
	for _, intent := range intents {
		if err := ctx.Err(); err != nil {
			return errors.Join(result, err)
		}
		if !time.Now().Before(deadline) {
			break
		}
		d.uncertainCursor = &store.RemoteIntentCursor{CreatedAt: intent.CreatedAt, IntentID: intent.IntentID}
		if _, _, err := d.ObserveUncertainIntent(observationCtx, intent.IntentID); err != nil {
			result = errors.Join(result, remoteReconciliationIssueFor(intent, intent.Operation, err))
		}
	}
	return result
}

// ObserveUncertainIntent reads one exact uncertain remote run with an expired
// delivery lease, without replay. Abandoned dispatching attempts are excluded. A
// matching result establishes only acceptance; normal strict accepted-result
// reconciliation must still prove terminal events, output and teardown.
func (d *RemoteDriver) ObserveUncertainIntent(ctx context.Context, id domain.IntentID) (store.LocalIntentRecord, sshbridge.ReplyFrame, error) {
	if !d.configured() {
		return store.LocalIntentRecord{}, sshbridge.ReplyFrame{}, ErrRemoteDriverConfiguration
	}
	intent, err := d.authority.GetLocalIntent(ctx, id)
	if err != nil {
		return store.LocalIntentRecord{}, sshbridge.ReplyFrame{}, err
	}
	if intent.Target.Kind() != domain.TargetKindRemote || intent.Operation != "run" ||
		intent.DeliveryState != store.LocalIntentUncertain {
		return intent, sshbridge.ReplyFrame{}, nil
	}
	if intent.LeaseExpiresAt == nil || d.now().UTC().Before(*intent.LeaseExpiresAt) {
		return intent, sshbridge.ReplyFrame{}, store.ErrLocalIntentLeaseHeld
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
	caller, err := d.remoteCallerForTarget(intent.Target)
	if err != nil {
		return intent, sshbridge.ReplyFrame{}, err
	}
	reply, err := d.observeRemoteCall(ctx, caller, frame)
	if err != nil {
		return intent, sshbridge.ReplyFrame{}, err
	}
	outcome, err := reconcileRemoteReply(intent, frame, reply)
	if err != nil {
		return intent, reply, err
	}
	if outcome != reconcileAccepted {
		return intent, reply, ErrRemoteNotReconciled
	}
	if !d.now().UTC().Before(deadline) {
		return intent, reply, ErrRemoteUncertaintyDeadline
	}
	// An intervening lease renewal or transition invalidates this observation.
	accepted, err := d.authority.AcceptObservedRemoteIntent(ctx, intent)
	if err != nil {
		return intent, reply, err
	}
	return accepted, reply, nil
}
