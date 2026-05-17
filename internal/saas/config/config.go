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
	AppRole  AppRole        `yaml:"app_role"`
	Database DatabaseConfig `yaml:"database"`
	Redis    RedisConfig    `yaml:"redis"`
	JWT      JWTConfig      `yaml:"jwt"`
	Server   ServerConfig   `yaml:"server"`
	Friction FrictionConfig `yaml:"friction"`
	GA       GAConfig       `yaml:"ga"`
	DataFeed DataFeedConfig `yaml:"data_feed"`
}

type DatabaseConfig struct {
	Host         string `yaml:"host"`
	Port         int    `yaml:"port"`
	User         string `yaml:"user"`
	Password     string `yaml:"password"`
	Database     string `yaml:"database"`
	SSLMode      string `yaml:"ssl_mode"`       // disable / require / verify-full
	MaxOpenConns int    `yaml:"max_open_conns"`
	MaxIdleConns int    `yaml:"max_idle_conns"`
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

type RedisConfig struct {
	Addr     string `yaml:"addr"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
}

type JWTConfig struct {
	Secret string        `yaml:"secret"`
	TTL    time.Duration `yaml:"ttl"`
}

type ServerConfig struct {
	HTTPListen      string `yaml:"http_listen"`       // ":8080"
	WSListen        string `yaml:"ws_listen"`         // ":8081"
	MetricsListen   string `yaml:"metrics_listen"`    // ":9090"
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

// DataFeedConfig is the datafeeder defaults. [INVENTED v1] — feeder
// settings will firm up during Phase 1.5.
type DataFeedConfig struct {
	BinanceArchiveBaseURL string        `yaml:"binance_archive_base_url"`
	BinanceAPIBaseURL     string        `yaml:"binance_api_base_url"`
	APIRateInterval       time.Duration `yaml:"api_rate_interval"`
	DefaultSymbol         string        `yaml:"default_symbol"`
	DefaultInterval       string        `yaml:"default_interval"`
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
	return nil
}
