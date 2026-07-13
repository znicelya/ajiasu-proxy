package httpserver

import (
	"encoding/json"
	"net/http"

	"github.com/dnomd343/ajiasu-proxy/internal/platform/requestctx"
)

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	RequestID string         `json:"request_id"`
	Details   map[string]any `json:"details"`
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string, details map[string]any) {
	if details == nil {
		details = map[string]any{}
	}
	writeJSON(w, status, errorEnvelope{Error: errorBody{
		Code:      code,
		Message:   message,
		RequestID: requestctx.RequestID(r.Context()),
		Details:   details,
	}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
