package vault

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/mitchellh/go-homedir"
)

func tokenTestHome(t *testing.T) string {
	t.Helper()
	t.Cleanup(homedir.Reset)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("VAULT_TOKEN", "")
	t.Setenv("VAULT_CONFIG_PATH", "")
	homedir.Reset()
	return home
}

func writeTokenTestFile(t *testing.T, path, contents string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
}

func TestNewClientTokenPrecedence(t *testing.T) {
	home := tokenTestHome(t)
	writeTokenTestFile(t, filepath.Join(home, ".vault-token"), "helper-token\n", 0o600)

	client, err := NewClient(Options{})
	if err != nil || client.API.Token() != "helper-token" {
		t.Fatalf("default helper token not used: %v", err)
	}

	t.Setenv("VAULT_TOKEN", "environment-token")
	client, err = NewClient(Options{})
	if err != nil || client.API.Token() != "environment-token" {
		t.Fatalf("environment token not used: %v", err)
	}

	// Explicit and environment tokens must bypass even invalid helper configuration.
	writeTokenTestFile(t, filepath.Join(home, ".vault"), "invalid = true", 0o600)
	client, err = NewClient(Options{Token: "explicit-token"})
	if err != nil || client.API.Token() != "explicit-token" {
		t.Fatalf("explicit token not used: %v", err)
	}
	client, err = NewClient(Options{})
	if err != nil || client.API.Token() != "environment-token" {
		t.Fatalf("environment token did not bypass helper: %v", err)
	}
}

func TestNewClientTokenHelperErrors(t *testing.T) {
	home := tokenTestHome(t)
	_, err := NewClient(Options{})
	if err == nil || !strings.Contains(err.Error(), "no vault token") {
		t.Fatalf("missing token: expected no-token error, got %v", err)
	}
	writeTokenTestFile(t, filepath.Join(home, ".vault-token"), " \n", 0o600)
	_, err = NewClient(Options{})
	if err == nil || !strings.Contains(err.Error(), "no vault token") {
		t.Fatalf("empty token: expected no-token error, got %v", err)
	}
	if err := os.Remove(filepath.Join(home, ".vault-token")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(home, ".vault-token"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err = NewClient(Options{})
	if err == nil || !strings.Contains(err.Error(), "reading vault token helper") {
		t.Fatalf("unreadable token: expected helper error, got %v", err)
	}
	writeTokenTestFile(t, filepath.Join(home, ".vault"), "invalid = \"sensitive-value\"", 0o600)
	_, err = NewClient(Options{})
	if err == nil || !strings.Contains(err.Error(), "configuring vault token helper") || strings.Contains(err.Error(), "sensitive-value") {
		t.Fatalf("invalid config: expected sanitized configuration error, got %v", err)
	}
}

func TestNewClientExternalTokenHelper(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper uses a shell script")
	}
	home := tokenTestHome(t)
	helper := filepath.Join(home, "helper")
	config := filepath.Join(home, "custom-config")
	writeTokenTestFile(t, config, "token_helper = "+strconv.Quote(helper), 0o600)
	t.Setenv("VAULT_CONFIG_PATH", config)
	t.Setenv("KVX_TEST_HELPER_TOKEN", "custom-token")
	writeTokenTestFile(t, helper, "#!/bin/sh\n[ \"$1\" = get ] || exit 3\nprintf ' %s\\n' \"$KVX_TEST_HELPER_TOKEN\"\n", 0o700)

	client, err := NewClient(Options{})
	if err != nil || client.API.Token() != "custom-token" {
		t.Fatalf("custom helper token not used: %v", err)
	}
	writeTokenTestFile(t, filepath.Join(home, ".vault"), "token_helper = "+strconv.Quote(helper), 0o600)
	t.Setenv("VAULT_CONFIG_PATH", "")
	client, err = NewClient(Options{})
	if err != nil || client.API.Token() != "custom-token" {
		t.Fatalf("default configuration helper token not used: %v", err)
	}
	writeTokenTestFile(t, helper, "#!/bin/sh\nprintf 'sensitive-value' >&2\nprintf 'sensitive-value'\nexit 7\n", 0o700)
	_, err = NewClient(Options{})
	if err == nil || !strings.Contains(err.Error(), "status 7") || strings.Contains(err.Error(), "sensitive-value") {
		t.Fatalf("helper failure: expected sanitized exit status, got %v", err)
	}
	writeTokenTestFile(t, helper, "#!/bin/sh\nprintf ' \\n'\n", 0o700)
	_, err = NewClient(Options{})
	if err == nil || !strings.Contains(err.Error(), "no vault token") {
		t.Fatalf("empty helper: expected no-token error, got %v", err)
	}
	if err := os.Remove(helper); err != nil {
		t.Fatal(err)
	}
	_, err = NewClient(Options{})
	if err == nil || !strings.Contains(err.Error(), "configuring vault token helper") {
		t.Fatalf("missing helper: expected configuration error, got %v", err)
	}
}
