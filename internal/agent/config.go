// Package agent implements the SaaS-side WebSocket client used by
// cmd/agent. Each Agent process services exactly one exchange account
// (1:1 binding per docs/saas-ws-protocol-v1.md §1) and never accepts
// inbound connections.
//
// Protocol responsibilities (per saas-ws-protocol-v1.md):
//   - §4.2 handshake: send hello, wait auth_required, send auth, wait
//     auth_ok, wait state_sync_request, reply with state_sync_response
//   - §4.4 heartbeat: reply pong within 5s of each ping
//   - §4.5 reconnect: exponential backoff (500/1/2/4/8/16/32/60 s)
//     with ±20% jitter; cap 60s; never give up
//   - §5.8/§5.9: TradeCommand → idempotency check → exchange.Submit →
//     Ack; subsequent fills surface as OrderUpdate
//   - §8.2: cache best bid/ask at submit to compute ActualSlippageBps
//
// Iron rule 3: API keys live only in config.agent.yaml; this package
// never forwards them on the WS connection.
package agent

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"quantlab/internal/wire"
)

// Config mirrors config.agent.yaml. Field tags match
// docs/saas-ws-protocol-v1.md §8.1.
type Config struct {
	AgentID     string            `yaml:"agent_id"`
	AccountID   string            `yaml:"account_id"`
	SaaSURL     string            `yaml:"saas_url"`
	SaaSToken   string            `yaml:"saas_token"`
	Exchange    ExchangeConfig    `yaml:"exchange"`
	Log         LogConfig         `yaml:"log"`
	Idempotency IdempotencyConfig `yaml:"idempotency"`
}

// ExchangeConfig holds credentials and the exchange selector. Iron rule
// 3: ApiKey / ApiSecret never leave this struct.
type ExchangeConfig struct {
	Name      string `yaml:"name"`       // v1: "binance_spot" or "mock"
	APIKey    string `yaml:"api_key"`    // never sent over WS
	APISecret string `yaml:"api_secret"` // never sent over WS
	BaseURL   string `yaml:"base_url"`
}

// Environment reports the trading environment this exchange config points
// at, as one of the wire.Environment* constants, for the backlog ⑥
// handshake consistency assertion. The mock exchange is its own
// environment; otherwise it is inferred from base_url (testnet hosts
// carry "testnet" in the domain). Empty when it cannot be determined —
// the SaaS then skips the assertion.
func (e ExchangeConfig) Environment() string {
	if e.Name == "mock" {
		return wire.EnvironmentMock
	}
	if strings.Contains(e.BaseURL, "testnet") {
		return wire.EnvironmentTestnet
	}
	if strings.Contains(e.BaseURL, "binance.com") {
		return wire.EnvironmentMainnet
	}
	return ""
}

type LogConfig struct {
	Level string `yaml:"level"`
	Path  string `yaml:"path"`
}

type IdempotencyConfig struct {
	DBPath        string `yaml:"db_path"`
	RetentionDays int    `yaml:"retention_days"`
}

// LoadConfig reads and validates the YAML at path. Missing required
// fields produce errors at startup rather than first WS handshake.
func LoadConfig(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("agent: read config %q: %w", path, err)
	}
	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("agent: parse config %q: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	c.applyDefaults()
	return &c, nil
}

// Validate checks that the minimum fields needed to connect are present.
// Exchange credentials are NOT validated here — a missing api_key only
// breaks orders, not the WS session, so it's an exchange-time error.
func (c *Config) Validate() error {
	switch {
	case c.AgentID == "":
		return errors.New("agent: agent_id empty")
	case c.AccountID == "":
		return errors.New("agent: account_id empty")
	case c.SaaSURL == "":
		return errors.New("agent: saas_url empty")
	case c.SaaSToken == "":
		return errors.New("agent: saas_token empty")
	case c.Exchange.Name == "":
		return errors.New("agent: exchange.name empty")
	}
	return nil
}

// applyDefaults fills zero values with sensible defaults (called after
// Validate succeeds).
func (c *Config) applyDefaults() {
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
	if c.Idempotency.RetentionDays == 0 {
		c.Idempotency.RetentionDays = 7
	}
}

// DefaultDeltaReportInterval is the §5.11 delta_report cadence. The
// Agent sends an account-level reconciliation snapshot this often as the
// fallback channel for lost OrderUpdate frames.
const DefaultDeltaReportInterval = 60 * time.Second

// DefaultBackoff returns the 8-step backoff sequence from §4.5. Cap is
// the final value; reconnect loops should hold at cap for further misses.
func DefaultBackoff() []time.Duration {
	return []time.Duration{
		500 * time.Millisecond,
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		32 * time.Second,
		60 * time.Second,
	}
}
