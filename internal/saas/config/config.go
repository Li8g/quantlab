// Package config loads QuantLab's runtime configuration from a YAML file
// at a path given by the CONFIG_PATH env var, or ./config.yaml by default.
//
// AppRole is the single switch that flips behavior between production
// SaaS, lab (offline backtest), and dev (local). The startup sequence
// (cmd/saas/main.go) reads AppRole and conditionally starts components
// (WS hub, cron, etc.) — see docs/系统总体拓扑结构.md §2.
package config

import (
	"errors"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"quantlab/internal/wire"
)

// AppRole is the runtime-mode switch.
type AppRole string

const (
	AppRoleSaaS AppRole = "saas"
	AppRoleLab  AppRole = "lab"
	AppRoleDev  AppRole = "dev"
)

// IsValid reports whether r is one of saas/lab/dev.
func (r AppRole) IsValid() bool {
	switch r {
	case AppRoleSaaS, AppRoleLab, AppRoleDev:
		return true
	}
	return false
}

// Config is the in-memory representation of config.yaml.
type Config struct {
	AppRole   AppRole         `yaml:"app_role"`
	Database  DatabaseConfig  `yaml:"database"`
	JWT       JWTConfig       `yaml:"jwt"`
	Server    ServerConfig    `yaml:"server"`
	Friction  FrictionConfig  `yaml:"friction"`
	GA        GAConfig        `yaml:"ga"`
	DataFeed  DataFeedConfig  `yaml:"data_feed"`
	Reconcile ReconcileConfig `yaml:"reconcile"`
	Live      LiveConfig      `yaml:"live"`
	Orders    OrdersConfig    `yaml:"orders"`
}

// LiveConfig tunes the live-trading fleet boundary.
type LiveConfig struct {
	// ExpectedEnvironment is the trading environment this deployment's
	// agents must be on (mainnet/testnet/mock), asserted at the WS
	// handshake against Hello.Environment (backlog ⑥). On app_role=saas a
	// mismatch is a hard auth_fail; on dev/lab it is a warn-only so the
	// intended testnet workflow (mainnet klines + testnet agent) keeps
	// running. Empty → the assertion is skipped entirely.
	ExpectedEnvironment string `yaml:"expected_environment"`
}

type DatabaseConfig struct {
	Host         string `yaml:"host"`
	Port         int    `yaml:"port"`
	User         string `yaml:"user"`
	Password     string `yaml:"password"`
	Database     string `yaml:"database"`
	SSLMode      string `yaml:"ssl_mode"` // disable / require / verify-full
	MaxOpenConns int    `yaml:"max_open_conns"`
	MaxIdleConns int    `yaml:"max_idle_conns"`

	// MigrationMode selects how store.NewDB provisions the schema,
	// independently of AppRole. AppRole drives runtime behavior splits
	// (import worker, gin mode, JWT length floor); the schema backend is a
	// separate axis. Decoupling them lets a lab/backtest cluster or
	// paper-trading node run the production-faithful goose schema without
	// inheriting saas's other side effects.
	//
	// Values (see MigrationMode* constants):
	//   "goose"       — apply versioned goose migrations (prod schema)
	//   "automigrate" — GORM AutoMigrate + raw DDL (dev fast-iteration)
	//   ""            — derive from AppRole: saas→goose, lab/dev→automigrate
	//
	// app_role=saas may not use "automigrate" (铁律 4); Validate rejects it.
	// [INVENTED v1] field name + enum values.
	MigrationMode string `yaml:"migration_mode"`
}

// Migration backend identifiers for DatabaseConfig.MigrationMode.
const (
	MigrationModeGoose       = "goose"
	MigrationModeAutoMigrate = "automigrate"
)

// ResolveMigrationMode returns the effective schema-provisioning backend,
// applying the AppRole-derived default when Database.MigrationMode is unset.
// store.NewDB branches on this (not on AppRole directly) so the migration
// engine is chosen independently of the runtime role.
func (c *Config) ResolveMigrationMode() string {
	if c.Database.MigrationMode != "" {
		return c.Database.MigrationMode
	}
	if c.AppRole == AppRoleSaaS {
		return MigrationModeGoose
	}
	return MigrationModeAutoMigrate
}

// DSN renders a libpq-style connection string for gorm.io/driver/postgres.
func (d DatabaseConfig) DSN() string {
	ssl := d.SSLMode
	if ssl == "" {
		ssl = "disable"
	}
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		d.Host, d.Port, d.User, d.Password, d.Database, ssl,
	)
}

type JWTConfig struct {
	Secret string        `yaml:"secret"`
	TTL    time.Duration `yaml:"ttl"`

	// AdminTTL bounds how long an admin-role JWT lives. Sudo-style
	// step-up: most sessions are viewer/operator with the longer TTL;
	// admin tokens auto-expire quickly to shrink the fat-finger window.
	// 0 → 10 minutes.
	AdminTTL time.Duration `yaml:"admin_ttl"`
}

type ServerConfig struct {
	HTTPListen      string        `yaml:"http_listen"`    // ":8080"
	WSListen        string        `yaml:"ws_listen"`      // ":8081"
	MetricsListen   string        `yaml:"metrics_listen"` // ":9090"
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
}

// FrictionConfig is the default friction baseline used when a request
// does not specify its own (corresponds to coding plan §I-3.11).
//
// Types are float64 to match domain.FrictionParams / resultpkg.FrictionActual
// / api.CreateEvolutionTaskRequest.TakerFeeBPS — friction is bps but the
// engine may interpolate finer increments (e.g. 7.5 bps for a stress sweep).
type FrictionConfig struct {
	TakerFeeBPS float64 `yaml:"taker_fee_bps"`
	SlippageBPS float64 `yaml:"slippage_bps"`
}

// GAConfig holds engine-wide defaults. Per-task overrides come from the
// CreateEvolutionTaskRequest body.
type GAConfig struct {
	PopSize              int     `yaml:"pop_size"`
	MaxGenerations       int     `yaml:"max_generations"`
	EliteRatio           float64 `yaml:"elite_ratio"`
	FatalMDD             float64 `yaml:"fatal_mdd"`
	OosDays              int     `yaml:"oos_days"`
	FatalAuditSampleRate float64 `yaml:"fatal_audit_sample_rate"`
	SbbBlockLenFallback  int     `yaml:"sbb_block_len_fallback"`
}

// ReconcileConfig tunes the live-trading auto-freeze (kill_switch Option 3).
// These were compile-time consts marked [INVENTED v1: tune as real drift data
// arrives]; lifting them to config lets an operator retune from observed
// `delta_report_reconcile_summary` logs without a redeploy. The right value
// is coupled to trade speed: a faster strategy has more in-flight fills per
// 60s delta_report window, so its *normal* transient drift is larger and the
// freeze line must sit above it. Zero on either field → the default below.
type ReconcileConfig struct {
	// FreezeToleranceBps is the auto-freeze line in bps; a managed asset's
	// drift above this (for FreezeDebounceReports consecutive reports) halts
	// the agent. Strictly higher than the 50bps ledger/alert line. Default 200.
	FreezeToleranceBps float64 `yaml:"freeze_tolerance_bps"`
	// FreezeDebounceReports is how many CONSECUTIVE delta_reports must breach
	// the line before the kill fires, so a single in-flight-fill blip at the
	// 60s cadence doesn't halt the agent. Default 2.
	FreezeDebounceReports int `yaml:"freeze_debounce_reports"`
}

// DataFeedConfig is the datafeeder defaults.
type DataFeedConfig struct {
	BinanceArchiveBaseURL string        `yaml:"binance_archive_base_url"`
	BinanceAPIBaseURL     string        `yaml:"binance_api_base_url"`
	APIRateInterval       time.Duration `yaml:"api_rate_interval"`
	DefaultSymbol         string        `yaml:"default_symbol"`
	DefaultInterval       string        `yaml:"default_interval"`
	// MaxBarStaleness bounds how old the newest trailing 1m kline may be at
	// Tick time before the live-trading manager skips trading for that cycle
	// (no order dispatch) and warns. Stale klines would price orders off a
	// wrong or zero close, which the LOT_SIZE/notional checks reject and the
	// reconciler eventually auto-freezes on — far better to not trade until
	// the feed catches up. Consumed by internal/saas/instance.Manager, NOT
	// the datafeeder. Zero → instance.DefaultMaxBarStaleness.
	MaxBarStaleness time.Duration `yaml:"max_bar_staleness"`
}

// DefaultPriceCapBps is the B2 price-protection cap applied when
// orders.price_cap_bps is omitted. [INVENTED v1] — tune per deployment.
const DefaultPriceCapBps = 50

// OrdersConfig tunes the live-trading order execution layer (B2 price
// protection, decision-b2-limit-order-price-protection.md). It is a
// human-held execution guardrail in the same family as
// reconcile.freeze_tolerance_bps — it never enters the GA or the backtest.
type OrdersConfig struct {
	// PriceCapBps caps how far a marketable-limit order's price may sit from
	// the decision-time close. The SaaS dispatcher rewrites each market
	// intent as a marketable LIMIT IOC at close×(1±cap/1e4); a fill worse than
	// the cap is rejected by the exchange rather than executing at a flash
	// price. A pointer so "absent" (→ DefaultPriceCapBps, protection on) is
	// distinguishable from an explicit 0 (protection off, market passthrough).
	// Invariant: cap ≥ the deployed champion's effective slippage_bps,
	// enforced at deploy-champion (otherwise the backtest's normal fill price
	// would itself be rejected live).
	PriceCapBps *float64 `yaml:"price_cap_bps"`
}

// EffectivePriceCapBps resolves the configured cap: an explicit value (incl.
// 0 = disabled) is honored; absent (nil) falls back to DefaultPriceCapBps.
func (o OrdersConfig) EffectivePriceCapBps() float64 {
	if o.PriceCapBps == nil {
		return DefaultPriceCapBps
	}
	return *o.PriceCapBps
}

// Load reads, parses, and applies defaults to the YAML at path.
// If path is empty, it falls back to $CONFIG_PATH then ./config.yaml.
func Load(path string) (*Config, error) {
	if path == "" {
		path = os.Getenv("CONFIG_PATH")
	}
	if path == "" {
		path = "config.yaml"
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}

	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}

	c.applyDefaults()

	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.Friction.TakerFeeBPS == 0 {
		c.Friction.TakerFeeBPS = 10
	}
	if c.Friction.SlippageBPS == 0 {
		c.Friction.SlippageBPS = 5
	}
	if c.Database.SSLMode == "" {
		c.Database.SSLMode = "disable"
	}
	if c.JWT.TTL == 0 {
		c.JWT.TTL = 24 * time.Hour
	}
	if c.JWT.AdminTTL == 0 {
		c.JWT.AdminTTL = 10 * time.Minute
	}
	if c.Server.HTTPListen == "" {
		c.Server.HTTPListen = ":8080"
	}
	if c.Server.WSListen == "" {
		c.Server.WSListen = ":8081"
	}
	if c.Server.MetricsListen == "" {
		c.Server.MetricsListen = ":9090"
	}
	if c.Server.ShutdownTimeout == 0 {
		c.Server.ShutdownTimeout = 30 * time.Second
	}
	if c.Reconcile.FreezeToleranceBps == 0 {
		c.Reconcile.FreezeToleranceBps = 200
	}
	if c.Reconcile.FreezeDebounceReports == 0 {
		c.Reconcile.FreezeDebounceReports = 2
	}
}

// jwtSecretMinBytesSaaS is the minimum jwt.secret length when
// app_role=saas. RFC 7518 §3.2 requires HS256 keys to be at least the size
// of the hash output (256 bits = 32 bytes). Dev/lab roles are exempt so
// local development isn't blocked by long-secret bookkeeping.
const jwtSecretMinBytesSaaS = 32

// Validate checks the high-level constraints that aren't sensible defaults.
func (c *Config) Validate() error {
	if !c.AppRole.IsValid() {
		return errors.New("app_role must be one of saas/lab/dev")
	}
	if c.Database.Host == "" {
		return errors.New("database.host is required")
	}
	if c.Database.Database == "" {
		return errors.New("database.database is required")
	}
	if c.JWT.Secret == "" {
		return errors.New("jwt.secret is required")
	}
	if c.AppRole == AppRoleSaaS && len(c.JWT.Secret) < jwtSecretMinBytesSaaS {
		return fmt.Errorf("jwt.secret must be at least %d bytes when app_role=saas (HS256/RFC 7518); got %d",
			jwtSecretMinBytesSaaS, len(c.JWT.Secret))
	}
	switch c.Database.MigrationMode {
	case "", MigrationModeGoose, MigrationModeAutoMigrate:
	default:
		return fmt.Errorf("database.migration_mode must be %q or %q (or empty to derive from app_role); got %q",
			MigrationModeGoose, MigrationModeAutoMigrate, c.Database.MigrationMode)
	}
	// 铁律 4: prod schema is goose-owned; AutoMigrate has no read-only mode
	// and would silently mutate prod. saas may not opt down to automigrate.
	if c.AppRole == AppRoleSaaS && c.Database.MigrationMode == MigrationModeAutoMigrate {
		return errors.New("app_role=saas cannot use database.migration_mode=automigrate (prod schema is goose-owned; 铁律 4)")
	}
	if c.Reconcile.FreezeToleranceBps < 0 {
		return errors.New("reconcile.freeze_tolerance_bps must be >= 0 (0 → default)")
	}
	if c.Reconcile.FreezeDebounceReports < 0 {
		return errors.New("reconcile.freeze_debounce_reports must be >= 0 (0 → default)")
	}
	if c.DataFeed.MaxBarStaleness < 0 {
		return errors.New("data_feed.max_bar_staleness must be >= 0 (0 → default)")
	}
	if c.Orders.PriceCapBps != nil && *c.Orders.PriceCapBps < 0 {
		return errors.New("orders.price_cap_bps must be >= 0 (omit → default 50; 0 → disabled)")
	}
	switch c.Live.ExpectedEnvironment {
	case "", wire.EnvironmentMainnet, wire.EnvironmentTestnet, wire.EnvironmentMock:
	default:
		return fmt.Errorf("live.expected_environment must be empty or one of mainnet/testnet/mock, got %q",
			c.Live.ExpectedEnvironment)
	}
	return nil
}
