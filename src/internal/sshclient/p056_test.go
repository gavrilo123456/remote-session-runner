package sshclient

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"remote-session-runner/src/internal/sshbridge"
)

func p056Config(t *testing.T, script string) Config {
	t.Helper()
	identity := filepath.Join(t.TempDir(), "identity key")
	knownHosts := filepath.Join(t.TempDir(), "known hosts")
	if err := os.WriteFile(identity, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(knownHosts, []byte("test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return Config{
		SSHPath:        script,
		User:           "ubuntu",
		Host:           "129.151.232.40",
		IdentityFile:   identity,
		KnownHostsFile: knownHosts,
	}
}

func p056Script(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-ssh.sh")
	contents := "#!/bin/sh\nset -eu\n" + body + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func p056PingRequest() sshbridge.RequestFrame {
	return sshbridge.RequestFrame{
		ProtocolVersion: sshbridge.ProtocolVersion,
		RequestID:       "p056-request-1",
		Operation:       sshbridge.OperationPing,
		Payload:         []byte(`{}`),
	}
}

func TestP056PinnedArgumentsAndFixedCommand(t *testing.T) {
	script := p056Script(t, "cat >/dev/null; printf '%s\\n' '{\"protocol_version\":1,\"request_id\":\"p056-request-1\",\"response_type\":\"result\",\"payload\":{}}'")
	config := p056Config(t, script)
	client, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	args := client.CommandArguments()
	joined := strings.Join(args, " ")
	for _, required := range []string{"-F", "/dev/null", "IdentitiesOnly=yes", "StrictHostKeyChecking=yes", "GlobalKnownHostsFile=/dev/null", "RequestTTY=no", "ClearAllForwardings=yes", "runner-ssh-bridge", "--stdio"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("SSH args %q do not contain %q", joined, required)
		}
	}
	if !strings.Contains(joined, "UserKnownHostsFile=\""+config.KnownHostsFile+"\"") {
		t.Fatalf("SSH args do not quote the spaced pin path: %q", joined)
	}
	if strings.Contains(joined, "remote-session-runner") && !strings.Contains(joined, config.KnownHostsFile) {
		t.Fatalf("SSH args unexpectedly use an implicit config: %q", joined)
	}
	reply, err := client.Call(context.Background(), p056PingRequest())
	if err != nil || reply.ResponseType != "result" {
		t.Fatalf("pinned call = %+v, %v", reply, err)
	}
}

func TestP056TransportPhasesAndBridgeErrorsAreDistinct(t *testing.T) {
	before, err := New(Config{
		SSHPath:        filepath.Join(t.TempDir(), "missing-ssh"),
		User:           "ubuntu",
		Host:           "host",
		IdentityFile:   "/tmp/identity",
		KnownHostsFile: "/tmp/known_hosts",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = before.Call(context.Background(), p056PingRequest())
	var transport *TransportError
	if !errors.As(err, &transport) || transport.Phase != PhaseBeforeSend || !errors.Is(err, ErrTransport) {
		t.Fatalf("before-send error = %v, want phase %s transport", err, PhaseBeforeSend)
	}

	afterScript := p056Script(t, "cat >/dev/null; exit 23")
	after, err := New(p056Config(t, afterScript))
	if err != nil {
		t.Fatal(err)
	}
	_, err = after.Call(context.Background(), p056PingRequest())
	if !errors.As(err, &transport) || transport.Phase != PhaseAfterSend {
		t.Fatalf("after-send error = %v, want phase %s", err, PhaseAfterSend)
	}

	errorScript := p056Script(t, "cat >/dev/null; printf '%s\\n' '{\"protocol_version\":1,\"request_id\":\"p056-request-1\",\"response_type\":\"error\",\"payload\":{\"code\":\"controller_mismatch\",\"message\":\"denied\",\"retryable\":false}}'")
	remoteError, err := New(p056Config(t, errorScript))
	if err != nil {
		t.Fatal(err)
	}
	reply, err := remoteError.Call(context.Background(), p056PingRequest())
	if err != nil || reply.ResponseType != "error" || errors.As(err, &transport) {
		t.Fatalf("bridge error reply = %+v, %v", reply, err)
	}
}

func TestP056BoundedFramesAndStableMutationFrame(t *testing.T) {
	script := p056Script(t, "cat >/dev/null; printf '%s\\n' '{\"protocol_version\":1,\"request_id\":\"mutation-1\",\"response_type\":\"result\",\"payload\":{\"accepted\":true}}'")
	config := p056Config(t, script)
	config.MaxFrameBytes = 128
	client, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	request := sshbridge.RequestFrame{
		ProtocolVersion: sshbridge.ProtocolVersion,
		RequestID:       "mutation-1",
		Operation:       sshbridge.OperationSubmitOrResumeCommand,
		ResourceID:      "command-stable-1",
		IdempotencyKey:  "idem-stable-1",
		Payload:         []byte(`{"session_id":"session-stable-1","intent_ordinal":1,"script":"printf stable"}`),
	}
	if _, err := client.Call(context.Background(), request); !errors.Is(err, ErrFrameLimit) {
		t.Fatalf("oversized frame error = %v", err)
	}

	config.MaxFrameBytes = 1 << 20
	client, err = New(config)
	if err != nil {
		t.Fatal(err)
	}
	first, err := client.Call(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.Call(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if string(first.Payload) != string(second.Payload) || first.RequestID != second.RequestID {
		t.Fatalf("stable retry replies changed: %+v / %+v", first, second)
	}
}

func TestP056StreamLossAndCursorReplies(t *testing.T) {
	script := p056Script(t, "cat >/dev/null; printf '%s\\n' '{\"protocol_version\":1,\"request_id\":\"stream-1\",\"response_type\":\"event\",\"payload\":{\"sequence\":1}}'; exit 23")
	client, err := New(p056Config(t, script))
	if err != nil {
		t.Fatal(err)
	}
	request := sshbridge.RequestFrame{ProtocolVersion: sshbridge.ProtocolVersion, RequestID: "stream-1", Operation: sshbridge.OperationStreamCommandEvents, Payload: []byte(`{"command_id":"command-1","after_sequence":0}`)}
	var replies []sshbridge.ReplyFrame
	err = client.Stream(context.Background(), request, func(reply sshbridge.ReplyFrame) error {
		replies = append(replies, reply)
		return nil
	})
	var transport *TransportError
	if len(replies) != 1 || !errors.As(err, &transport) || transport.Phase != PhaseAfterSend {
		t.Fatalf("stream loss replies=%+v err=%v", replies, err)
	}
}
