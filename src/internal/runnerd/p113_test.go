package runnerd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/runtime"
	"remote-session-runner/src/internal/store"
)

func TestP113DirectEventReplayUsesBoundedPublicNDJSONAndCursor(t *testing.T) {
	runtimeAdapter := &p109Runtime{generation: "p113-replay-generation"}
	service, authority, _ := p112NewService(t, runtimeAdapter)
	handler, err := NewDirectHTTPSAPIHandler(service)
	if err != nil {
		t.Fatal(err)
	}
	controller := p107DirectController(t, "tomasz.walczuk")
	commandID := p113CompleteCommand(t, handler, authority, controller)

	response := p107Do(handler, controller, true, http.MethodGet, "/v1/commands/"+string(commandID)+"/events", nil, "")
	if response.Code != http.StatusOK {
		t.Fatalf("event replay status = %d, body = %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Type") != "application/x-ndjson" || response.Header().Get("X-Runner-View") != "authority" || response.Header().Get("X-Runner-Stale") != "false" || response.Header().Get(directEventsLastSequenceHeader) == "" {
		t.Fatalf("event replay headers = %#v", response.Header())
	}
	allEvents := p113DecodeEventLines(t, response.Body.Bytes())
	if len(allEvents) < 4 || allEvents[0].Sequence != 1 || allEvents[0].Type != "command_queued" || allEvents[0].Ordinal != 1 {
		t.Fatalf("event replay did not start with queued sequence one: %#v", allEvents)
	}
	for index, event := range allEvents {
		if event.CommandID != string(commandID) || event.Sequence != int64(index+1) {
			t.Fatalf("event[%d] identity/sequence = %#v", index, event)
		}
	}
	var outputFound bool
	for _, event := range allEvents {
		if event.Type == "stdout" {
			decoded, err := base64.StdEncoding.DecodeString(event.DataBase64)
			if err != nil || string(decoded) != "p109-output\n" || event.Encoding != "base64" || event.ByteCount != int64(len(decoded)) {
				t.Fatalf("stdout event = %#v decoded=%q err=%v", event, decoded, err)
			}
			outputFound = true
		}
	}
	if !outputFound {
		t.Fatalf("event replay omitted output: %#v", allEvents)
	}

	after := p107Do(handler, controller, true, http.MethodGet, "/v1/commands/"+string(commandID)+"/events?after=2", nil, "")
	if after.Code != http.StatusOK {
		t.Fatalf("cursor replay status = %d, body = %s", after.Code, after.Body.String())
	}
	suffix := p113DecodeEventLines(t, after.Body.Bytes())
	if len(suffix) != len(allEvents)-2 || suffix[0].Sequence != 3 || suffix[len(suffix)-1].Sequence != allEvents[len(allEvents)-1].Sequence {
		t.Fatalf("after=2 replay = %#v, full history = %#v", suffix, allEvents)
	}
	beyondTail := p107Do(handler, controller, true, http.MethodGet, "/v1/commands/"+string(commandID)+"/events?after=999", nil, "")
	if beyondTail.Code != http.StatusOK || len(p113DecodeEventLines(t, beyondTail.Body.Bytes())) != 0 {
		t.Fatalf("high-water cursor replay status=%d body=%q, want empty successful range", beyondTail.Code, beyondTail.Body.String())
	}

	for _, path := range []string{
		"/v1/commands/" + string(commandID) + "/events?after=-1",
		"/v1/commands/" + string(commandID) + "/events?after=not-a-number",
		"/v1/commands/" + string(commandID) + "/events?after=9223372036854775808",
		"/v1/commands/" + string(commandID) + "/events?after=0&after=1",
		"/v1/commands/" + string(commandID) + "/events?unsupported=1",
		"/v1/commands/" + string(commandID) + "/events?follow=yes",
		"/v1/commands/" + string(commandID) + "/events?follow=",
		"/v1/commands/" + string(commandID) + "/events?follow=true&follow=false",
	} {
		invalid := p107Do(handler, controller, true, http.MethodGet, path, nil, "")
		if invalid.Code != http.StatusBadRequest {
			t.Errorf("invalid event query %q status = %d, body = %s", path, invalid.Code, invalid.Body.String())
		}
	}

	wrongController := p107DirectController(t, "other-controller")
	missingPrincipal := p107Do(handler, controller, false, http.MethodGet, "/v1/commands/"+string(commandID)+"/events", nil, "")
	if missingPrincipal.Code != http.StatusForbidden {
		t.Fatalf("unmapped replay status = %d, body = %s", missingPrincipal.Code, missingPrincipal.Body.String())
	}
	denied := p107Do(handler, wrongController, true, http.MethodGet, "/v1/commands/"+string(commandID)+"/events", nil, "")
	if denied.Code != http.StatusForbidden {
		t.Fatalf("cross-controller replay status = %d, body = %s", denied.Code, denied.Body.String())
	}
	var deniedError directAPIError
	p107Decode(t, denied, &deniedError)
	if deniedError.Code != "controller_mismatch" {
		t.Fatalf("cross-controller replay error = %#v", deniedError)
	}
}

func TestP113DirectEventReplayRejectsGapAndExpiredHistoryWithoutPartialSuccess(t *testing.T) {
	t.Run("gap", func(t *testing.T) {
		runtimeAdapter := &p109Runtime{generation: "p113-gap-generation"}
		service, authority, db := p112NewService(t, runtimeAdapter)
		handler, err := NewDirectHTTPSAPIHandler(service)
		if err != nil {
			t.Fatal(err)
		}
		controller := p107DirectController(t, "tomasz.walczuk")
		commandID := p113CompleteCommand(t, handler, authority, controller)
		if _, err := db.ExecContext(context.Background(), `DELETE FROM exec_command_events WHERE command_id = ? AND sequence = 3`, string(commandID)); err != nil {
			t.Fatal(err)
		}

		response := p107Do(handler, controller, true, http.MethodGet, "/v1/commands/"+string(commandID)+"/events", nil, "")
		p113RequireHistoryUnavailable(t, response, commandID, "remote_event_gap")
	})

	t.Run("retention expired even when cursor is past tail", func(t *testing.T) {
		runtimeAdapter := &p109Runtime{generation: "p113-expired-generation"}
		service, authority, db := p112NewService(t, runtimeAdapter)
		handler, err := NewDirectHTTPSAPIHandler(service)
		if err != nil {
			t.Fatal(err)
		}
		controller := p107DirectController(t, "tomasz.walczuk")
		commandID := p113CompleteCommand(t, handler, authority, controller)
		if _, err := db.ExecContext(context.Background(), `UPDATE exec_commands SET updated_at = '2000-01-01T00:00:00.000000000Z' WHERE command_id = ?`, string(commandID)); err != nil {
			t.Fatal(err)
		}
		report, err := authority.CollectGarbage(context.Background(), store.GarbageCollectionOptions{})
		if err != nil || report.CommandsOutputExpired != 1 || report.CommandEventsDeleted == 0 {
			t.Fatalf("retention GC report = %+v, err = %v", report, err)
		}

		response := p107Do(handler, controller, true, http.MethodGet, "/v1/commands/"+string(commandID)+"/events?after=999", nil, "")
		p113RequireHistoryUnavailable(t, response, commandID, "retention_expired")
		if _, err := authority.ReplayCommandEvents(context.Background(), commandID, 0); !errors.Is(err, store.ErrCommandReplayExpired) {
			t.Fatalf("store replay error = %v, want ErrCommandReplayExpired", err)
		}
	})
}

func TestP113DirectEventFrameBoundsOutputChunks(t *testing.T) {
	commandID, err := domain.NewCommandID("cmd-p113-frame")
	if err != nil {
		t.Fatal(err)
	}
	event := store.CommandEventRecord{
		CommandID: commandID, Sequence: 2, Type: "stdout", Payload: bytes.Repeat([]byte{'x'}, runtime.MaxOutputChunkBytes),
		ByteCount: runtime.MaxOutputChunkBytes, OccurredAt: time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC),
	}
	frame, err := encodeDirectCommandEvent(event, 1)
	if err != nil {
		t.Fatalf("maximum-size output event did not encode: %v", err)
	}
	if len(bytes.TrimSuffix(frame, []byte{'\n'})) > domain.MaxSerializedFrameBytes {
		t.Fatalf("encoded event frame has %d bytes, maximum is %d", len(frame)-1, domain.MaxSerializedFrameBytes)
	}
	event.Payload = append(event.Payload, 'x')
	event.ByteCount++
	if _, err := encodeDirectCommandEvent(event, 1); !errors.Is(err, domain.ErrSerializedInputTooLarge) {
		t.Fatalf("oversized output event error = %v, want bounded-frame error", err)
	}
}

func p113CompleteCommand(t *testing.T, handler http.Handler, authority *store.AuthorityStore, controller domain.ControllerIdentity) domain.CommandID {
	t.Helper()
	created := p107Do(handler, controller, true, http.MethodPost, "/v1/sessions", []byte(p107CreateBody), "p113-create-key")
	if created.Code != http.StatusAccepted {
		t.Fatalf("create session status = %d, body = %s", created.Code, created.Body.String())
	}
	var session directSessionAcceptance
	p107Decode(t, created, &session)
	submitted := p107Do(handler, controller, true, http.MethodPost, "/v1/sessions/"+session.SessionID+"/commands", []byte(`{"script":"printf p113"}`), "p113-submit-key")
	if submitted.Code != http.StatusAccepted {
		t.Fatalf("submit command status = %d, body = %s", submitted.Code, submitted.Body.String())
	}
	var command directCommandAcceptance
	p107Decode(t, submitted, &command)
	commandID := domain.CommandID(command.CommandID)
	completed := p108WaitForCommandState(t, authority, commandID, domain.CommandStateSucceeded)
	if completed.FinalEventSequence == nil || *completed.FinalEventSequence < 4 {
		t.Fatalf("completed command final sequence = %#v", completed.FinalEventSequence)
	}
	return commandID
}

func p113DecodeEventLines(t *testing.T, body []byte) []directCommandEvent {
	t.Helper()
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 1024), domain.MaxSerializedFrameBytes+1)
	var events []directCommandEvent
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		if len(line) > domain.MaxSerializedFrameBytes {
			t.Fatalf("event line has %d bytes, maximum is %d", len(line), domain.MaxSerializedFrameBytes)
		}
		var event directCommandEvent
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatalf("decode event line %q: %v", line, err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}

func p113RequireHistoryUnavailable(t *testing.T, response *httptest.ResponseRecorder, commandID domain.CommandID, reason string) {
	t.Helper()
	if response.Code != http.StatusGone || !strings.HasPrefix(response.Header().Get("Content-Type"), "application/json") || strings.Contains(response.Body.String(), "application/x-ndjson") {
		t.Fatalf("unavailable event history response status=%d headers=%#v body=%s", response.Code, response.Header(), response.Body.String())
	}
	var value directEventHistoryError
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode event history error %q: %v", response.Body.String(), err)
	}
	if value.Code != "event_history_unavailable" || value.Retryable || value.ResourceID != string(commandID) || value.Details.OutputComplete || value.Details.OutputUnavailableReason != reason {
		t.Fatalf("event history error = %#v, want reason %q", value, reason)
	}
}
