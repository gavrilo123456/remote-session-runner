package sshbridge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const p104DispatcherFingerprint = "SHA256:FnYdf11ON1ZHh+eCnkZbHvjePKktcH7Z/rDjv3nWsms"

func TestP104LoadKeyControllerMapRequiresOwnerOnlyStrictFile(t *testing.T) {
	valid := fmt.Sprintf("version: 1\nkeys:\n  %q:\n    controller_type: queued_mac\n    controller_id: tomasz.walczuk\n", p104DispatcherFingerprint)
	directory := t.TempDir()

	path := filepath.Join(directory, "keys.yaml")
	if err := os.WriteFile(path, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	controllers, err := LoadKeyControllerMap(path)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := controllers.ControllerForKey(p104DispatcherFingerprint)
	if err != nil || controller.Type() != "queued_mac" || controller.ID() != "tomasz.walczuk" {
		t.Fatalf("controller=%+v err=%v", controller, err)
	}

	for _, fixture := range []struct {
		name string
		body string
	}{
		{name: "unknown field", body: strings.Replace(valid, "version: 1", "version: 1\nextra: true", 1)},
		{name: "second document", body: valid + "---\nversion: 1\nkeys: {}\n"},
		{name: "invalid fingerprint", body: strings.Replace(valid, p104DispatcherFingerprint, "SHA256:invalid", 1)},
		{name: "invalid controller", body: strings.Replace(valid, "queued_mac", "unknown", 1)},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "keys.yaml")
			if err := os.WriteFile(file, []byte(fixture.body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadKeyControllerMap(file); err == nil {
				t.Fatal("invalid controller map was accepted")
			}
		})
	}

	worldReadable := filepath.Join(t.TempDir(), "keys.yaml")
	if err := os.WriteFile(worldReadable, []byte(valid), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKeyControllerMap(worldReadable); err == nil {
		t.Fatal("group-readable controller map was accepted")
	}

	symlink := filepath.Join(t.TempDir(), "keys.yaml")
	if err := os.Symlink(path, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKeyControllerMap(symlink); err == nil {
		t.Fatal("symlinked controller map was accepted")
	}
}

func TestP104BridgeCommandServesPingWithMappedAuthenticatedKey(t *testing.T) {
	controllerMap := filepath.Join(t.TempDir(), "ssh-controller-map.yaml")
	body := fmt.Sprintf("version: 1\nkeys:\n  %q:\n    controller_type: queued_mac\n    controller_id: tomasz.walczuk\n", p104DispatcherFingerprint)
	if err := os.WriteFile(controllerMap, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	request, err := json.Marshal(RequestFrame{
		ProtocolVersion: ProtocolVersion, RequestID: "p104-ping", Operation: OperationPing, Payload: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	request = append(request, '\n')
	var stdout, stderr bytes.Buffer
	socket := filepath.Join(t.TempDir(), "runnerd.sock")
	code := Run([]string{
		"--stdio", "--authenticated-key", p104DispatcherFingerprint,
		"--controller-map", controllerMap, "--runnerd-socket", socket,
	}, bytes.NewReader(request), &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("bridge run code=%d stderr=%q", code, stderr.String())
	}
	var reply ReplyFrame
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &reply); err != nil {
		t.Fatalf("decode bridge ping reply %q: %v", stdout.String(), err)
	}
	if reply.ProtocolVersion != ProtocolVersion || reply.RequestID != "p104-ping" || reply.ResponseType != "result" {
		t.Fatalf("bridge ping reply=%+v", reply)
	}
}
