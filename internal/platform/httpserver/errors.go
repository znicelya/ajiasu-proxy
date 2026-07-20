package httpserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/znicelya/ajiasu-proxy/internal/platform/requestctx"
)

type ErrorEnvelope struct {
	Error APIError `json:"error"`
}

type APIError struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	RequestID string         `json:"request_id"`
	Details   map[string]any `json:"details"`
}

func WriteError(w http.ResponseWriter, r *http.Request, status int, code, message string, details map[string]any) {
	if details == nil {
		details = map[string]any{}
	}
	WriteJSON(w, status, ErrorEnvelope{Error: APIError{
		Code:      code,
		Message:   message,
		RequestID: requestctx.RequestID(r.Context()),
		Details:   details,
	}})
}

func WriteJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func DecodeJSON(r *http.Request, target any) error {
	if r == nil || r.Body == nil || target == nil {
		return errors.New("request body is required")
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode JSON body: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain one JSON value")
		}
		return fmt.Errorf("decode trailing JSON data: %w", err)
	}
	return nil
}

func DecodeJSONBytes(r *http.Request, target any) ([]byte, error) {
	if r == nil || r.Body == nil {
		return nil, errors.New("request body is required")
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("read JSON body: %w", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	if err := DecodeJSON(r, target); err != nil {
		return nil, err
	}
	return body, nil
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string, details map[string]any) {
	WriteError(w, r, status, code, message, details)
}

func writeJSON(w http.ResponseWriter, status int, value any) { WriteJSON(w, status, value) }
