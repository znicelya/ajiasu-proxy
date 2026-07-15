package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAdminBootstrapHelpDoesNotLoadConfiguration(t *testing.T) {
	var stdout, stderr bytes.Buffer
	handled, exitCode := runAdminCLI([]string{"admin", "bootstrap", "--help"}, func(string) (string, bool) {
		t.Fatal("help loaded configuration")
		return "", false
	}, nil, &stdout, &stderr)
	if !handled || exitCode != 0 {
		t.Fatalf("runAdminCLI(help) = handled %t exit %d", handled, exitCode)
	}
	if !strings.Contains(stdout.String(), "control-plane admin bootstrap") || stderr.Len() != 0 {
		t.Fatalf("help output stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestAdminBootstrapRejectsRedirectedInputBeforeConfiguration(t *testing.T) {
	err := executeAdminBootstrap(context.Background(), func(string) (string, bool) {
		t.Fatal("redirected input loaded configuration")
		return "", false
	}, fakeAdminTerminal{interactive: false}, time.Now)
	if err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("executeAdminBootstrap(redirected) error = %v", err)
	}
}

func TestAdminBootstrapRejectsSecretArgumentsWithoutEchoingThem(t *testing.T) {
	const canary = "password-canary-must-not-be-echoed"
	var stdout, stderr bytes.Buffer
	handled, exitCode := runAdminCLI([]string{"admin", "bootstrap", "--password=" + canary}, nil, nil, &stdout, &stderr)
	if !handled || exitCode != 2 {
		t.Fatalf("runAdminCLI(secret flag) = handled %t exit %d", handled, exitCode)
	}
	if strings.Contains(stdout.String(), canary) || strings.Contains(stderr.String(), canary) {
		t.Fatal("secret argument was echoed")
	}
}

type fakeAdminTerminal struct {
	interactive bool
}

func (f fakeAdminTerminal) Interactive() bool { return f.interactive }
func (fakeAdminTerminal) ReadLine(string) (string, error) {
	return "", errors.New("unexpected ReadLine")
}
func (fakeAdminTerminal) ReadSecret(string) ([]byte, error) {
	return nil, errors.New("unexpected ReadSecret")
}
func (fakeAdminTerminal) WriteLine(string) error { return errors.New("unexpected WriteLine") }
