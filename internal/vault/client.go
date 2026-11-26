// Package vault wraps the HashiCorp Vault API client with KV-aware helpers
// (KV v1/v2 detection, listing, reading, metadata) used by every kvx command.
// It works with OpenBao too, since OpenBao keeps the same HTTP API.
package vault

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/hashicorp/vault/api"
	"github.com/hashicorp/vault/api/cliconfig"
)

// Options configure the Vault client. Empty fields fall back to the standard
// VAULT_* environment variables (VAULT_ADDR, VAULT_TOKEN, VAULT_NAMESPACE...).
type Options struct {
	Address   string
	Token     string
	Namespace string
	// KVVersion forces the KV engine version (1 or 2). 0 means auto-detect.
	KVVersion int
}

// Client is a Vault API client plus kvx settings.
type Client struct {
	API  *api.Client
	opts Options
}

// NewClient builds a client from options, the environment, and the Vault token helper.
func NewClient(opts Options) (*Client, error) {
	cfg := api.DefaultConfig()
	if cfg.Error != nil {
		return nil, fmt.Errorf("reading vault config: %w", cfg.Error)
	}
	if opts.Address != "" {
		cfg.Address = opts.Address
	}
	c, err := api.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("creating vault client: %w", err)
	}
	if opts.Token != "" {
		c.SetToken(opts.Token)
	}
	if c.Token() == "" {
		helper, err := cliconfig.DefaultTokenHelper()
		if err != nil {
			return nil, fmt.Errorf("configuring vault token helper: check the Vault CLI configuration and helper path")
		}
		token, err := helper.Get()
		if err != nil {
			// The helper's stderr may contain credentials, so do not expose it.
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				return nil, fmt.Errorf("reading vault token helper: helper exited with status %d", exitErr.ExitCode())
			}
			return nil, fmt.Errorf("reading vault token helper: unable to run helper or read token file; check permissions and configuration")
		}
		c.SetToken(strings.TrimSpace(token))
	}
	if opts.Namespace != "" {
		c.SetNamespace(opts.Namespace)
	}
	if c.Token() == "" {
		return nil, fmt.Errorf("no vault token: set VAULT_TOKEN, run `vault login`, or pass --token")
	}
	return &Client{API: c, opts: opts}, nil
}

// Address returns the Vault address in use.
func (c *Client) Address() string { return c.API.Address() }
