package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_Defaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(`
app_role: dev
database:
  host: localhost
  database: q
jwt:
  secret: x
`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Friction.TakerFeeBPS != 10 {
		t.Errorf("default TakerFeeBPS want 10, got %v", cfg.Friction.TakerFeeBPS)
	}
	if cfg.Friction.SlippageBPS != 5 {
		t.Errorf("default SlippageBPS want 5, got %v", cfg.Friction.SlippageBPS)
	}
	if cfg.Database.SSLMode != "disable" {
		t.Errorf("default ssl_mode want disable, got %q", cfg.Database.SSLMode)
	}
	if cfg.Server.HTTPListen != ":8080" {
		t.Errorf("default http_listen want :8080, got %q", cfg.Server.HTTPListen)
	}
}

func TestLoad_RejectsBadAppRole(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(`
app_role: prod
database:
  host: localhost
  database: q
jwt:
  secret: x
`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Error("expected error for app_role=prod, got nil")
	}
}

func TestLoad_RequiresJWTSecret(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(`
app_role: dev
database:
  host: localhost
  database: q
`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Error("expected error when jwt.secret missing")
	}
}

func TestLoad_RejectsShortJWTSecretInSaaS(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(`
app_role: saas
database:
  host: localhost
  database: q
jwt:
  secret: short
`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Error("expected error for app_role=saas with short jwt.secret, got nil")
	}
}

func TestLoad_AcceptsShortJWTSecretInDev(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(`
app_role: dev
database:
  host: localhost
  database: q
jwt:
  secret: x
`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path); err != nil {
		t.Errorf("dev role should accept short jwt.secret, got: %v", err)
	}
}

func TestResolveMigrationMode(t *testing.T) {
	cases := []struct {
		name     string
		role     AppRole
		explicit string
		want     string
	}{
		{"saas default goose", AppRoleSaaS, "", MigrationModeGoose},
		{"lab default automigrate", AppRoleLab, "", MigrationModeAutoMigrate},
		{"dev default automigrate", AppRoleDev, "", MigrationModeAutoMigrate},
		{"lab explicit goose", AppRoleLab, MigrationModeGoose, MigrationModeGoose},
		{"dev explicit goose", AppRoleDev, MigrationModeGoose, MigrationModeGoose},
		{"explicit overrides saas default", AppRoleSaaS, MigrationModeGoose, MigrationModeGoose},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{AppRole: tc.role}
			c.Database.MigrationMode = tc.explicit
			if got := c.ResolveMigrationMode(); got != tc.want {
				t.Errorf("ResolveMigrationMode()=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestLoad_RejectsBadMigrationMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(`
app_role: lab
database:
  host: localhost
  database: q
  migration_mode: gorm
jwt:
  secret: x
`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Error("expected error for migration_mode=gorm, got nil")
	}
}

func TestLoad_RejectsSaaSWithAutoMigrate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(`
app_role: saas
database:
  host: localhost
  database: q
  migration_mode: automigrate
jwt:
  secret: a-secret-of-at-least-thirty-two-bytes-long
`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Error("expected error for app_role=saas + migration_mode=automigrate, got nil")
	}
}

func TestLoad_AcceptsLabWithGoose(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(`
app_role: lab
database:
  host: localhost
  database: q
  migration_mode: goose
jwt:
  secret: x
`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("lab + migration_mode=goose should load, got: %v", err)
	}
	if got := cfg.ResolveMigrationMode(); got != MigrationModeGoose {
		t.Errorf("ResolveMigrationMode()=%q, want goose", got)
	}
}

func TestLoad_ReconcileDefaultsAndOverride(t *testing.T) {
	dir := t.TempDir()
	// Defaults when section omitted.
	def := filepath.Join(dir, "def.yaml")
	if err := os.WriteFile(def, []byte(`
app_role: dev
database: { host: localhost, database: q }
jwt: { secret: x }
`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	c, err := Load(def)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Reconcile.FreezeToleranceBps != 200 || c.Reconcile.FreezeDebounceReports != 2 {
		t.Errorf("defaults = %v/%d, want 200/2", c.Reconcile.FreezeToleranceBps, c.Reconcile.FreezeDebounceReports)
	}

	// Explicit override is preserved.
	ovr := filepath.Join(dir, "ovr.yaml")
	if err := os.WriteFile(ovr, []byte(`
app_role: dev
database: { host: localhost, database: q }
jwt: { secret: x }
reconcile: { freeze_tolerance_bps: 350, freeze_debounce_reports: 3 }
`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	c2, err := Load(ovr)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c2.Reconcile.FreezeToleranceBps != 350 || c2.Reconcile.FreezeDebounceReports != 3 {
		t.Errorf("override = %v/%d, want 350/3", c2.Reconcile.FreezeToleranceBps, c2.Reconcile.FreezeDebounceReports)
	}
}

func TestLoad_RejectsNegativeReconcile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(`
app_role: dev
database: { host: localhost, database: q }
jwt: { secret: x }
reconcile: { freeze_tolerance_bps: -1 }
`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Error("expected error for negative freeze_tolerance_bps, got nil")
	}
}

func TestLoad_RejectsBadExpectedEnvironment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(`
app_role: dev
database: { host: localhost, database: q }
jwt: { secret: x }
live: { expected_environment: prod }
`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Error("expected error for live.expected_environment=prod, got nil")
	}
}

func TestLoad_AcceptsValidExpectedEnvironment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(`
app_role: dev
database: { host: localhost, database: q }
jwt: { secret: x }
live: { expected_environment: testnet }
`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Live.ExpectedEnvironment != "testnet" {
		t.Errorf("ExpectedEnvironment = %q, want testnet", c.Live.ExpectedEnvironment)
	}
}

func TestDatabaseDSN(t *testing.T) {
	d := DatabaseConfig{
		Host: "h", Port: 5432, User: "u", Password: "p", Database: "db", SSLMode: "disable",
	}
	want := "host=h port=5432 user=u password=p dbname=db sslmode=disable"
	if got := d.DSN(); got != want {
		t.Errorf("DSN mismatch:\n  got=%q\n  want=%q", got, want)
	}
}
