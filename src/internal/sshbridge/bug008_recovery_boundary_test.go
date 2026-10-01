package sshbridge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"remote-session-runner/src/internal/domain"
)

// The owner-only recovery command cannot travel through the SSH bridge. It is
// outside the bridge's frozen operation vocabulary and must fail before a
// forwarding handler can touch the private runnerd socket.
func TestBUG008RecoverRetainedCapacityIsNotAnSSHBridgeOperation(t *testing.T) {
	for _, operation := range []string{"recover_retained_capacity", "recover-retained-capacity"} {
		t.Run(operation, func(t *testing.T) {
			forwarded := false
			server, err := NewServer(ServerOptions{
				Controllers: KeyControllerMap{"bug008-owner": p049Controller(t)},
				Handler: requestHandlerFunc(func(context.Context, domain.ControllerIdentity, RequestFrame) (ReplyFrame, error) {
					forwarded = true
					return ReplyFrame{}, nil
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			raw := fmt.Sprintf(`{"protocol_version":1,"request_id":"req-bug008-boundary","operation":%q,"payload":{}}`, operation)
			err = server.Serve(context.Background(), "bug008-owner", strings.NewReader(raw+"\n"), &strings.Builder{})
			if !errors.Is(err, ErrInvalidFrame) {
				t.Fatalf("bridge unknown operation error=%v, want invalid frame", err)
			}
			if forwarded {
				t.Fatal("unknown maintenance operation reached SSH bridge forwarding")
			}
		})
	}
}
