package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/dnomd343/ajiasu-proxy/internal/audit"
	"github.com/dnomd343/ajiasu-proxy/internal/identity"
	"github.com/dnomd343/ajiasu-proxy/internal/platform/config"
	"github.com/dnomd343/ajiasu-proxy/internal/platform/database"
	"github.com/dnomd343/ajiasu-proxy/internal/platform/keyring"
	"github.com/google/uuid"
	"golang.org/x/term"
)

const bootstrapHelp = `Usage:
  control-plane admin bootstrap

Interactively creates the one break-glass platform administrator.
Passwords, TOTP secrets, and recovery codes cannot be supplied as flags.`

type adminTerminal interface {
	Interactive() bool
	ReadLine(prompt string) (string, error)
	ReadSecret(prompt string) ([]byte, error)
	WriteLine(value string) error
}

type realAdminTerminal struct {
	input  *os.File
	output io.Writer
	reader *bufio.Reader
}

func newRealAdminTerminal(input *os.File, output io.Writer) *realAdminTerminal {
	return &realAdminTerminal{input: input, output: output, reader: bufio.NewReader(input)}
}

func (t *realAdminTerminal) Interactive() bool {
	output, ok := t.output.(*os.File)
	return ok && term.IsTerminal(int(t.input.Fd())) && term.IsTerminal(int(output.Fd()))
}

func (t *realAdminTerminal) ReadLine(prompt string) (string, error) {
	if _, err := fmt.Fprint(t.output, prompt); err != nil {
		return "", err
	}
	value, err := t.reader.ReadString('\n')
	return strings.TrimSpace(value), err
}

func (t *realAdminTerminal) ReadSecret(prompt string) ([]byte, error) {
	if _, err := fmt.Fprint(t.output, prompt); err != nil {
		return nil, err
	}
	value, err := term.ReadPassword(int(t.input.Fd()))
	_, _ = fmt.Fprintln(t.output)
	return value, err
}

func (t *realAdminTerminal) WriteLine(value string) error {
	_, err := fmt.Fprintln(t.output, value)
	return err
}

func runAdminCLI(args []string, lookup func(string) (string, bool), stdin *os.File, stdout, stderr io.Writer) (bool, int) {
	if len(args) == 0 || args[0] != "admin" {
		return false, 0
	}
	if len(args) == 1 || args[1] == "--help" || args[1] == "-h" {
		_, _ = fmt.Fprintln(stdout, bootstrapHelp)
		return true, 0
	}
	if args[1] != "bootstrap" {
		_, _ = fmt.Fprintln(stderr, "unknown admin command")
		return true, 2
	}
	if len(args) == 3 && (args[2] == "--help" || args[2] == "-h") {
		_, _ = fmt.Fprintln(stdout, bootstrapHelp)
		return true, 0
	}
	if len(args) != 2 {
		_, _ = fmt.Fprintln(stderr, "admin bootstrap does not accept secret or positional arguments")
		return true, 2
	}
	if stdin == nil {
		_, _ = fmt.Fprintln(stderr, "admin bootstrap requires terminal input")
		return true, 1
	}
	if err := executeAdminBootstrap(context.Background(), lookup, newRealAdminTerminal(stdin, stdout), time.Now); err != nil {
		_, _ = fmt.Fprintln(stderr, "admin bootstrap failed:", err)
		return true, 1
	}
	return true, 0
}

func executeAdminBootstrap(ctx context.Context, lookup func(string) (string, bool), terminal adminTerminal, now func() time.Time) error {
	if terminal == nil || !terminal.Interactive() {
		return errors.New("interactive terminal input and output are required")
	}
	cfg, err := config.Load(lookup)
	if err != nil {
		return errors.New("configuration is invalid")
	}
	keyMaterial := cfg.Keyring.Bytes()
	defer clear(keyMaterial)
	ring, err := keyring.NewAESGCM(keyMaterial)
	if err != nil {
		return errors.New("keyring file is invalid")
	}
	pools, err := database.OpenPools(ctx, cfg.Database)
	if err != nil {
		return errors.New("database is unavailable")
	}
	defer pools.Close()
	service, err := identity.NewLocalService(pools, ring, audit.NewService(), cfg.LocalAuth.Enabled, cfg.LocalAuth.AllowedCIDRs)
	if err != nil {
		return errors.New("local authentication service is unavailable")
	}
	bootstrapped, err := service.Bootstrapped(ctx)
	if err != nil {
		return errors.New("inspect local administrator state")
	}
	if bootstrapped {
		return errors.New("a local administrator already exists")
	}
	identifier, err := terminal.ReadLine("Administrator identifier: ")
	if err != nil {
		return errors.New("read administrator identifier")
	}
	displayName, err := terminal.ReadLine("Display name: ")
	if err != nil {
		return errors.New("read display name")
	}
	password, err := terminal.ReadSecret("Password: ")
	if err != nil {
		return errors.New("read password")
	}
	defer clear(password)
	confirmation, err := terminal.ReadSecret("Confirm password: ")
	if err != nil {
		return errors.New("read password confirmation")
	}
	defer clear(confirmation)
	if !bytes.Equal(password, confirmation) {
		return errors.New("password confirmation does not match")
	}
	secret, err := identity.GenerateTOTPSecret()
	if err != nil {
		return errors.New("generate TOTP secret")
	}
	issuer := url.QueryEscape("AJiaSu Control Plane")
	account := url.QueryEscape(strings.TrimSpace(identifier))
	if err := terminal.WriteLine("TOTP secret (store in your authenticator now): " + secret); err != nil {
		return errors.New("display TOTP secret")
	}
	if err := terminal.WriteLine("otpauth://totp/" + issuer + ":" + account + "?secret=" + secret + "&issuer=" + issuer + "&digits=6&period=30"); err != nil {
		return errors.New("display TOTP enrollment URI")
	}
	codeBytes, err := terminal.ReadSecret("Current TOTP code: ")
	if err != nil {
		return errors.New("read TOTP confirmation")
	}
	defer clear(codeBytes)
	verified, err := identity.VerifyTOTP(secret, strings.TrimSpace(string(codeBytes)), now().UTC())
	if err != nil || !verified {
		return errors.New("TOTP confirmation failed")
	}
	requestID, err := uuid.NewV7()
	if err != nil {
		return errors.New("generate bootstrap request ID")
	}
	result, err := service.Bootstrap(ctx, identity.BootstrapLocalAdmin{
		Identifier: identifier, DisplayName: displayName, Password: password, TOTPSecret: secret,
		Metadata: identity.AuthenticationMetadata{
			SourceIP: netip.MustParseAddr("127.0.0.1"), UserAgent: "control-plane-admin-bootstrap/1.0", RequestID: requestID,
		},
	})
	if err != nil {
		if errors.Is(err, identity.ErrAlreadyBootstrapped) {
			return errors.New("a local administrator already exists")
		}
		return errors.New("persist local administrator")
	}
	if err := terminal.WriteLine("Recovery codes (shown once):"); err != nil {
		return errors.New("display recovery codes")
	}
	for _, recoveryCode := range result.RecoveryCodes {
		if err := terminal.WriteLine(recoveryCode); err != nil {
			return errors.New("display recovery codes")
		}
	}
	return nil
}
