package localapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"remote-session-runner/src/internal/mailbox"
)

// MailboxLifecycleStatusProvider is the narrow read-only dependency used by
// the Mac-local API. Its implementation must be built from configured inboxes
// and must not import, reconcile, publish, or execute mailbox work.
type MailboxLifecycleStatusProvider interface {
	MailboxLifecycleStatus(context.Context, string, string) (mailbox.LifecycleStatus, error)
}

type mailboxLifecycleStatusResponse struct {
	InboxID   string                           `json:"inbox_id"`
	Available bool                             `json:"available"`
	Counts    mailboxLifecycleCountsResponse   `json:"counts"`
	Request   *mailboxLifecycleRequestResponse `json:"request,omitempty"`
}

type mailboxLifecycleCountsResponse struct {
	RequestInputShapes         map[string]int `json:"request_input_shapes"`
	AcknowledgementInputShapes map[string]int `json:"acknowledgement_input_shapes"`
	DurableStates              map[string]int `json:"durable_states"`
	RequestActions             map[string]int `json:"request_actions"`
	AcknowledgementActions     map[string]int `json:"acknowledgement_actions"`
	ActionableUnacceptedPairs  int            `json:"actionable_unaccepted_pairs"`
}

type mailboxLifecycleRequestResponse struct {
	RequestID         string                           `json:"request_id"`
	Request           mailboxLifecycleArtifactResponse `json:"request"`
	Acknowledgement   mailboxLifecycleArtifactResponse `json:"acknowledgement"`
	Terminal          bool                             `json:"terminal"`
	Acknowledged      bool                             `json:"acknowledged"`
	IngressDiagnostic bool                             `json:"ingress_diagnostic"`
}

type mailboxLifecycleArtifactResponse struct {
	InputShape   string `json:"input_shape"`
	DurableState string `json:"durable_state"`
	Action       string `json:"action"`
}

// mailboxLifecycleUnavailableResponse is deliberately separate from the
// normal result: status callers must see that no lifecycle answer was made,
// rather than guessing from a marker or a partial filesystem read.
type mailboxLifecycleUnavailableResponse struct {
	InboxID   string `json:"inbox_id"`
	Available bool   `json:"available"`
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func (s *Server) handleMailboxLifecycleStatus(response http.ResponseWriter, request *http.Request, rawInboxID string) {
	inboxID, err := url.PathUnescape(rawInboxID)
	if err != nil || !validMailboxLifecycleIdentifier(inboxID) {
		writeError(response, http.StatusBadRequest, "invalid_request", "invalid inbox_id")
		return
	}
	query, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid_request", "invalid lifecycle query")
		return
	}
	requestID := ""
	for key, values := range query {
		if key != "request_id" || len(values) != 1 || values[0] == "" {
			writeError(response, http.StatusBadRequest, "invalid_request", "invalid lifecycle query")
			return
		}
		requestID = values[0]
	}
	if s == nil || s.mailboxLifecycleStatus == nil {
		writeMailboxLifecycleUnavailable(response, inboxID)
		return
	}
	status, err := s.mailboxLifecycleStatus.MailboxLifecycleStatus(request.Context(), inboxID, requestID)
	if err != nil {
		switch {
		case errors.Is(err, mailbox.ErrLifecycleMailboxNotConfigured):
			writeError(response, http.StatusNotFound, "mailbox_not_found", "configured inbox was not found")
		case errors.Is(err, mailbox.ErrMailboxInput):
			writeError(response, http.StatusBadRequest, "invalid_request", "invalid request_id")
		default:
			writeMailboxLifecycleUnavailable(response, inboxID)
		}
		return
	}
	answer := mailboxLifecycleStatusResponse{
		InboxID: inboxID, Available: true, Counts: mailboxLifecycleCountsResponse{
			RequestInputShapes:         lifecycleInputShapeCounts(status.Counts.RequestInputShapes),
			AcknowledgementInputShapes: lifecycleInputShapeCounts(status.Counts.AcknowledgementInputShapes),
			DurableStates:              lifecycleDurableStateCounts(status.Counts.DurableStates),
			RequestActions:             lifecycleActionCounts(status.Counts.RequestActions),
			AcknowledgementActions:     lifecycleActionCounts(status.Counts.AcknowledgementActions),
			ActionableUnacceptedPairs:  status.Counts.ActionableUnacceptedPairs,
		},
	}
	if status.Request != nil {
		answer.Request = lifecycleRequestResponse(*status.Request)
	}
	writeJSON(response, http.StatusOK, answer)
}

func writeMailboxLifecycleUnavailable(response http.ResponseWriter, inboxID string) {
	writeJSON(response, http.StatusServiceUnavailable, mailboxLifecycleUnavailableResponse{
		InboxID: inboxID, Available: false, Code: "mailbox_lifecycle_unavailable",
		Message: "mailbox lifecycle status is unavailable", Retryable: true,
	})
}

func lifecycleRequestResponse(inspection mailbox.LifecycleInspection) *mailboxLifecycleRequestResponse {
	durable := inspection.Request.DurableState
	return &mailboxLifecycleRequestResponse{
		RequestID: inspection.RequestID,
		Request: mailboxLifecycleArtifactResponse{
			InputShape: string(inspection.Request.InputShape), DurableState: string(durable), Action: string(inspection.Request.Action),
		},
		Acknowledgement: mailboxLifecycleArtifactResponse{
			InputShape: string(inspection.Acknowledgement.InputShape), DurableState: string(inspection.Acknowledgement.DurableState), Action: string(inspection.Acknowledgement.Action),
		},
		Terminal:          durable == mailbox.LifecycleDurableTerminalUnacknowledged || durable == mailbox.LifecycleDurableTerminalAcknowledged,
		Acknowledged:      durable == mailbox.LifecycleDurableTerminalAcknowledged,
		IngressDiagnostic: durable == mailbox.LifecycleDurableIngressDiagnostic,
	}
}

func lifecycleInputShapeCounts(values map[mailbox.LifecycleInputShape]int) map[string]int {
	result := make(map[string]int, len(values))
	for key, value := range values {
		result[string(key)] = value
	}
	return result
}

func lifecycleDurableStateCounts(values map[mailbox.LifecycleDurableState]int) map[string]int {
	result := make(map[string]int, len(values))
	for key, value := range values {
		result[string(key)] = value
	}
	return result
}

func lifecycleActionCounts(values map[mailbox.LifecycleAction]int) map[string]int {
	result := make(map[string]int, len(values))
	for key, value := range values {
		result[string(key)] = value
	}
	return result
}

func mailboxLifecycleStatusPath(path string) (string, bool) {
	const prefix = "/v1/mailboxes/"
	const suffix = "/lifecycle"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return "", false
	}
	rawInboxID := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	if rawInboxID == "" || strings.Contains(rawInboxID, "/") {
		return "", false
	}
	return rawInboxID, true
}

func validMailboxLifecycleIdentifier(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		letter := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z'
		digit := character >= '0' && character <= '9'
		if index == 0 {
			if !letter && !digit {
				return false
			}
			continue
		}
		if !letter && !digit && character != '.' && character != '_' && character != '-' {
			return false
		}
	}
	return true
}
