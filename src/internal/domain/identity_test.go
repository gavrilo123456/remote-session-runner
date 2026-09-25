package domain

import (
	"errors"
	"fmt"
	"testing"
)

func TestP005OpaqueIDConstructors(t *testing.T) {
	tests := []struct {
		name string
		new  func(string) (any, error)
	}{
		{name: "session", new: func(v string) (any, error) { return NewSessionID(v) }},
		{name: "command", new: func(v string) (any, error) { return NewCommandID(v) }},
		{name: "job", new: func(v string) (any, error) { return NewJobID(v) }},
		{name: "request", new: func(v string) (any, error) { return NewRequestID(v) }},
		{name: "intent", new: func(v string) (any, error) { return NewIntentID(v) }},
		{name: "controller", new: func(v string) (any, error) { return NewControllerID(v) }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const want = "opaque-id-17"
			got, err := test.new(want)
			if err != nil {
				t.Fatalf("construct ID: %v", err)
			}
			if got == nil || fmt.Sprint(got) != want {
				t.Fatalf("constructed ID = %v, want %q", got, want)
			}

			if _, err := test.new(""); !errors.Is(err, ErrEmptyID) {
				t.Fatalf("empty ID error = %v, want ErrEmptyID", err)
			}
		})
	}
}

func TestP005ControllerIdentityNamespaces(t *testing.T) {
	tests := []struct {
		controllerType ControllerType
	}{
		{controllerType: ControllerTypeLocalUser},
		{controllerType: ControllerTypeQueuedMac},
		{controllerType: ControllerTypeDirectMTLS},
	}

	for _, test := range tests {
		identity, err := NewControllerIdentity(test.controllerType, ControllerID("tomasz.walczuk"))
		if err != nil {
			t.Fatalf("construct %q identity: %v", test.controllerType, err)
		}
		if identity.Type() != test.controllerType || identity.ID() != "tomasz.walczuk" {
			t.Errorf("identity = (%q, %q), want (%q, %q)", identity.Type(), identity.ID(), test.controllerType, "tomasz.walczuk")
		}
	}

	if _, err := NewControllerIdentity(ControllerType("unknown"), ControllerID("caller")); !errors.Is(err, ErrInvalidControllerType) {
		t.Errorf("unknown controller type error = %v, want ErrInvalidControllerType", err)
	}
	if _, err := NewControllerIdentity(ControllerTypeLocalUser, ""); !errors.Is(err, ErrEmptyID) {
		t.Errorf("empty controller ID error = %v, want ErrEmptyID", err)
	}
}

func TestP005ExecutionTarget(t *testing.T) {
	tests := []struct {
		kind    TargetKind
		profile string
	}{
		{kind: TargetKindLocal, profile: "mac-workstation"},
		{kind: TargetKindRemote, profile: "linux-host"},
	}

	for _, test := range tests {
		target, err := NewExecutionTarget(test.kind, test.profile)
		if err != nil {
			t.Fatalf("construct %q target: %v", test.kind, err)
		}
		if target.Kind() != test.kind || target.Profile() != test.profile {
			t.Errorf("target = (%q, %q), want (%q, %q)", target.Kind(), target.Profile(), test.kind, test.profile)
		}
	}

	if _, err := NewExecutionTarget(TargetKind("container"), "linux-host"); !errors.Is(err, ErrInvalidTargetKind) {
		t.Errorf("unknown target kind error = %v, want ErrInvalidTargetKind", err)
	}
	if _, err := NewExecutionTarget(TargetKindLocal, ""); !errors.Is(err, ErrEmptyTargetProfile) {
		t.Errorf("empty target profile error = %v, want ErrEmptyTargetProfile", err)
	}
}
