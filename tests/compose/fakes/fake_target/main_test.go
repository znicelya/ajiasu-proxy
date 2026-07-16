package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFakeTargetIsDeterministicAndSecretFree(t *testing.T) {
	response := httptest.NewRecorder()
	handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	body, _ := io.ReadAll(response.Result().Body)
	if response.Code != http.StatusOK || string(body) != "ajiasu-compose-fake-target" {
		t.Fatalf("status=%d body=%q", response.Code, body)
	}
	if strings.Contains(strings.ToLower(string(body)), "password") {
		t.Fatal("fake target exposed credential-like output")
	}
}
