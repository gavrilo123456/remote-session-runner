package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"remote-session-runner/src/internal/sshbridge"
)

type p128ProbeCaller struct {
	request sshbridge.RequestFrame
	reply   sshbridge.ReplyFrame
	err     error
}

func (c *p128ProbeCaller) Call(_ context.Context, request sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
	c.request = request
	if c.err != nil {
		return sshbridge.ReplyFrame{}, c.err
	}
	if c.reply.RequestID == "" {
		c.reply = sshbridge.ReplyFrame{
			ProtocolVersion: sshbridge.ProtocolVersion,
			RequestID:       request.RequestID,
			ResponseType:    "result",
			Payload:         json.RawMessage(`{}`),
		}
	}
	return c.reply, nil
}

func TestP128RemoteHealthProbeUsesReadOnlyPingAndValidatesReply(t *testing.T) {
	caller := &p128ProbeCaller{}
	resolver, err := NewRemoteCallerResolver(map[string]RemoteCaller{"linux-host": caller})
	if err != nil {
		t.Fatal(err)
	}
	driver := &RemoteDriver{resolver: resolver}
	if err := driver.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if caller.request.Operation != sshbridge.OperationPing || caller.request.ResourceID != "" || caller.request.IdempotencyKey != "" || string(caller.request.Payload) != `{}` {
		t.Fatalf("probe frame=%+v", caller.request)
	}
	caller.reply.RequestID = "wrong-correlation"
	if err := driver.Probe(context.Background()); !errors.Is(err, ErrRemoteResponse) {
		t.Fatalf("invalid probe reply error=%v, want ErrRemoteResponse", err)
	}
	caller.err = errors.New("secret-bearing transport error")
	err = driver.Probe(context.Background())
	if !errors.Is(err, ErrRemoteResponse) || err.Error() == "" || containsP128(err.Error(), "secret-bearing") {
		t.Fatalf("transport failure leaked raw cause or lost classification: %v", err)
	}
}

func containsP128(value, substring string) bool {
	for i := 0; i+len(substring) <= len(value); i++ {
		if value[i:i+len(substring)] == substring {
			return true
		}
	}
	return false
}
