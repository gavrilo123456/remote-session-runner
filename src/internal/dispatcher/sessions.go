package dispatcher

import (
	"context"
	"encoding/json"
	"fmt"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

// RefreshSessionProjection reads the current remote session state and persists
// it for local API and mailbox readers. The create response is only a snapshot
// of the state at acceptance time, so callers use this read while a remote
// session is preparing or changing state.
func (d *RemoteDriver) RefreshSessionProjection(ctx context.Context, sessionID domain.SessionID, controller domain.ControllerIdentity) (store.RemoteSessionProjection, error) {
	if d == nil || d.authority == nil || d.caller == nil {
		return store.RemoteSessionProjection{}, ErrRemoteDriverConfiguration
	}
	validatedSession, err := domain.NewSessionID(string(sessionID))
	if err != nil {
		return store.RemoteSessionProjection{}, err
	}
	intent, err := d.authority.GetLocalIntentByResource(ctx, operationCreateSession, string(validatedSession), controller)
	if err != nil {
		return store.RemoteSessionProjection{}, err
	}
	if intent.Target.Kind() != domain.TargetKindRemote || (intent.DeliveryState != store.LocalIntentAccepted && intent.DeliveryState != store.LocalIntentReconciled) {
		return store.RemoteSessionProjection{}, fmt.Errorf("%w: session is not an accepted remote intent", ErrRemoteResponse)
	}
	request, err := readinessFrameForCreateIntent(intent)
	if err != nil {
		return store.RemoteSessionProjection{}, err
	}
	reply, err := d.caller.Call(ctx, request)
	if err != nil {
		return store.RemoteSessionProjection{}, fmt.Errorf("%w: session state read: %v", ErrRemoteResponse, err)
	}
	state, err := validateRemoteSessionReadReply(intent, request, reply)
	if err != nil {
		return store.RemoteSessionProjection{}, err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(reply.Payload, &object); err != nil || object == nil {
		return store.RemoteSessionProjection{}, fmt.Errorf("%w: session state result object", ErrRemoteResponse)
	}
	projection, present, err := remoteSessionProjectionFromReply(intent, object, d.now())
	if err != nil {
		return store.RemoteSessionProjection{}, err
	}
	if !present || projection.State != state {
		return store.RemoteSessionProjection{}, fmt.Errorf("%w: session state result is incomplete", ErrRemoteResponse)
	}
	if _, err := d.authority.UpsertRemoteSessionProjection(ctx, projection); err != nil {
		return store.RemoteSessionProjection{}, err
	}
	return d.authority.GetRemoteSessionProjection(ctx, validatedSession)
}
