package runnerd

import (
	"context"
	"net/http"
	"testing"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestP115O03DirectCommandStatusPreservesOutputUnavailableReason(t *testing.T) {
	for _, reason := range []string{"remote_event_gap", "capture_boundary_unconfirmed", "retention_expired"} {
		t.Run(reason, func(t *testing.T) {
			runtimeAdapter := &p109Runtime{generation: "p115-" + reason}
			service, authority, db := p112NewService(t, runtimeAdapter)
			handler, err := NewDirectHTTPSAPIHandler(service)
			if err != nil {
				t.Fatal(err)
			}
			controller := p107DirectController(t, "tomasz.walczuk")
			var commandID domain.CommandID

			if reason == "capture_boundary_unconfirmed" {
				commandID = p114RunningCommand(t, handler, authority, controller, "capture")
				if _, err := authority.AppendCommandEvent(context.Background(), store.CommandEventAppend{
					CommandID: commandID, Type: "stdout", Payload: []byte("captured prefix"), ByteCount: int64(len("captured prefix")),
				}); err != nil {
					t.Fatal(err)
				}
				_, err = authority.TransitionCommand(context.Background(), store.CommandTransition{
					CommandID: commandID, NextState: domain.CommandStateLost, OutputComplete: false,
				})
				if err != nil {
					t.Fatal(err)
				}
			} else {
				commandID = p113CompleteCommand(t, handler, authority, controller)
			}

			switch reason {
			case "remote_event_gap":
				if _, err := db.ExecContext(context.Background(), `DELETE FROM exec_command_events WHERE command_id = ? AND sequence = 3`, string(commandID)); err != nil {
					t.Fatal(err)
				}
				if _, err := db.ExecContext(context.Background(), `UPDATE exec_commands SET output_complete = 0, output_unavailable_reason = ? WHERE command_id = ?`, reason, string(commandID)); err != nil {
					t.Fatal(err)
				}
			case "capture_boundary_unconfirmed":
				if _, err := db.ExecContext(context.Background(), `UPDATE exec_commands SET output_unavailable_reason = ? WHERE command_id = ?`, reason, string(commandID)); err != nil {
					t.Fatal(err)
				}
			case "retention_expired":
				if _, err := db.ExecContext(context.Background(), `UPDATE exec_commands SET updated_at = '2000-01-01T00:00:00.000000000Z' WHERE command_id = ?`, string(commandID)); err != nil {
					t.Fatal(err)
				}
				report, err := authority.CollectGarbage(context.Background(), store.GarbageCollectionOptions{})
				if err != nil || report.CommandsOutputExpired != 1 {
					t.Fatalf("retention GC report=%+v err=%v", report, err)
				}
			}

			response := p107Do(handler, controller, true, http.MethodGet, "/v1/commands/"+string(commandID), nil, "")
			if response.Code != http.StatusOK {
				t.Fatalf("direct command status=%d body=%s", response.Code, response.Body.String())
			}
			var read directCommandReadResponse
			p107Decode(t, response, &read)
			if read.Resource.OutputComplete || read.Resource.OutputUnavailableReason != reason {
				t.Fatalf("direct command output status=%+v, want incomplete reason %q", read.Resource, reason)
			}

			if reason == "remote_event_gap" || reason == "retention_expired" {
				path := "/v1/commands/" + string(commandID) + "/events"
				if reason == "retention_expired" {
					path += "?after=999"
				}
				events := p107Do(handler, controller, true, http.MethodGet, path, nil, "")
				p113RequireHistoryUnavailable(t, events, commandID, reason)
			} else {
				events := p107Do(handler, controller, true, http.MethodGet, "/v1/commands/"+string(commandID)+"/events", nil, "")
				if events.Code != http.StatusOK {
					t.Fatalf("capture-loss event stream status=%d body=%s", events.Code, events.Body.String())
				}
				lines := p113DecodeEventLines(t, events.Body.Bytes())
				if len(lines) != 4 || lines[3].Type != "command_lost" || events.Header().Get(directEventsLastSequenceHeader) != "4" {
					t.Fatalf("capture-loss event history=%+v cursor=%q", lines, events.Header().Get(directEventsLastSequenceHeader))
				}
			}
		})
	}
}
