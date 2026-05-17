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

func TestDatabaseDSN(t *testing.T) {
	d := DatabaseConfig{
		Host: "h", Port: 5432, User: "u", Password: "p", Database: "db", SSLMode: "disable",
	}
	want := "host=h port=5432 user=u password=p dbname=db sslmode=disable"
	if got := d.DSN(); got != want {
		t.Errorf("DSN mismatch:\n  got=%q\n  want=%q", got, want)
	}
}
