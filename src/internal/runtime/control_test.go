package runtime

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
)

func TestP030D01HandshakeRoundTripAndGenerationBinding(t *testing.T) {
	frame, err := NewHandshakeFrame("session-p030", "generation-p030")
	if err != nil {
		t.Fatal(err)
	}
	var wire bytes.Buffer
	if err := WriteControlFrame(&wire, frame); err != nil {
		t.Fatal(err)
	}
	parser, err := NewControlParser("session-p030", "generation-p030")
	if err != nil {
		t.Fatal(err)
	}
	got, err := parser.ReadHandshake(&wire)
	if err != nil {
		t.Fatal(err)
	}
	if got != frame {
		t.Fatalf("handshake = %+v, want %+v", got, frame)
	}

	wrongGeneration, _ := NewHandshakeFrame("session-p030", "generation-old")
	wire.Reset()
	if err := WriteControlFrame(&wire, wrongGeneration); err != nil {
		t.Fatal(err)
	}
	if _, err := parser.ReadHandshake(&wire); !errors.Is(err, ErrControlGeneration) {
		t.Fatalf("wrong generation error = %v, want ErrControlGeneration", err)
	}
}

func TestP030D02ControlFramesRoundTripAndValidation(t *testing.T) {
	exitCode := 17
	frames := []ControlFrame{
		{Version: ControlProtocolVersion, Type: FrameTypeCommandStarted, SessionID: "session", CommandID: "command", Generation: "generation"},
		{Version: ControlProtocolVersion, Type: FrameTypeCommandComplete, SessionID: "session", CommandID: "command", Generation: "generation", ExitCode: &exitCode},
		{Version: ControlProtocolVersion, Type: FrameTypeCommandLost, SessionID: "session", CommandID: "command", Generation: "generation", ExitCode: &exitCode},
		{Version: ControlProtocolVersion, Type: FrameTypeStopAck, SessionID: "session", Generation: "generation"},
	}
	for _, want := range frames {
		var wire bytes.Buffer
		if err := WriteControlFrame(&wire, want); err != nil {
			t.Fatalf("write %s: %v", want.Type, err)
		}
		got, err := ReadControlFrame(&wire)
		if err != nil {
			t.Fatalf("read %s: %v", want.Type, err)
		}
		if got.Version != want.Version || got.Type != want.Type || got.SessionID != want.SessionID || got.CommandID != want.CommandID || got.Generation != want.Generation || (got.ExitCode == nil) != (want.ExitCode == nil) || (got.ExitCode != nil && *got.ExitCode != *want.ExitCode) {
			t.Fatalf("round trip %s = %+v, want %+v", want.Type, got, want)
		}
	}
}

func TestP030D03PrintedMarkersCannotSpoofControl(t *testing.T) {
	stdout := strings.NewReader("SESSION_READY\nCOMMAND_COMPLETE command-p030 0\n")
	if _, err := ReadControlFrame(stdout); err == nil {
		t.Fatal("printed stdout marker was accepted as a control frame")
	}

	frame, err := NewHandshakeFrame("session-p030", "generation-p030")
	if err != nil {
		t.Fatal(err)
	}
	var control bytes.Buffer
	if err := WriteControlFrame(&control, frame); err != nil {
		t.Fatal(err)
	}
	parser, err := NewControlParser("session-p030", "generation-p030")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parser.ReadHandshake(&control); err != nil {
		t.Fatalf("dedicated control channel handshake = %v", err)
	}
}

func TestP030D04FrameBoundsAndStrictJSON(t *testing.T) {
	var oversized [4]byte
	binary.BigEndian.PutUint32(oversized[:], MaxControlFrameBytes+1)
	if _, err := ReadControlFrame(bytes.NewReader(oversized[:])); !errors.Is(err, ErrControlFrameTooLarge) {
		t.Fatalf("oversized prefix error = %v, want ErrControlFrameTooLarge", err)
	}

	for _, body := range []string{
		`{"version":1,"type":"ready","session_id":"s","generation":"g"} {"version":1}`,
		`{"version":1,"type":"ready","session_id":"s","generation":"g","unexpected":true}`,
	} {
		var wire bytes.Buffer
		var header [4]byte
		binary.BigEndian.PutUint32(header[:], uint32(len(body)))
		wire.Write(header[:])
		wire.WriteString(body)
		if _, err := ReadControlFrame(&wire); !errors.Is(err, ErrControlFrameMalformed) {
			t.Fatalf("strict JSON body %q error = %v, want ErrControlFrameMalformed", body, err)
		}
	}
}

func TestP030D05ReservedDescriptors(t *testing.T) {
	if err := DefaultReservedDescriptors().Validate(); err != nil {
		t.Fatal(err)
	}
	for _, descriptors := range []ReservedDescriptors{{0, 4}, {3, 1}, {3, 3}} {
		if err := descriptors.Validate(); !errors.Is(err, ErrReservedDescriptor) {
			t.Fatalf("descriptors %+v error = %v, want ErrReservedDescriptor", descriptors, err)
		}
	}
}

func FuzzP030ReadControlFrameBounds(f *testing.F) {
	for _, length := range []uint32{0, 1, MaxControlFrameBytes, MaxControlFrameBytes + 1, ^uint32(0)} {
		var prefix [4]byte
		binary.BigEndian.PutUint32(prefix[:], length)
		f.Add(prefix[:])
	}
	f.Fuzz(func(t *testing.T, prefix []byte) {
		if len(prefix) > 4 {
			prefix = prefix[:4]
		}
		for len(prefix) < 4 {
			prefix = append(prefix, 0)
		}
		_, err := ReadControlFrame(bytes.NewReader(prefix))
		if err == nil || errors.Is(err, ErrControlFrameTooLarge) || errors.Is(err, ErrControlFrameMalformed) || errors.Is(err, ErrControlProtocol) {
			return
		}
		t.Fatalf("unexpected bounds error: %v", err)
	})
}
