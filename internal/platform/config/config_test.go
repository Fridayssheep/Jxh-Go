package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultIncludesAdminAndDatabasePool(t *testing.T) {
	t.Parallel()

	cfg := Default()
	if cfg.Admin.Addr != "127.0.0.1:8090" {
		t.Fatalf("admin addr = %q, want %q", cfg.Admin.Addr, "127.0.0.1:8090")
	}
	if !cfg.Admin.CookieSecure {
		t.Fatal("admin cookie must be secure by default")
	}
	if cfg.Admin.SessionTTLSeconds != 12*60*60 {
		t.Fatalf("session ttl = %d, want %d", cfg.Admin.SessionTTLSeconds, 12*60*60)
	}
	if cfg.Admin.SessionIdleTimeoutSeconds != 30*60 {
		t.Fatalf("session idle timeout = %d, want %d", cfg.Admin.SessionIdleTimeoutSeconds, 30*60)
	}
	if cfg.Admin.LoginWindowSeconds != 5*60 || cfg.Admin.LoginMaxAttempts != 5 {
		t.Fatalf("unexpected login rate limit defaults: %+v", cfg.Admin)
	}
	if cfg.Admin.MaxRequestBodyBytes != 1<<20 {
		t.Fatalf("max request body = %d, want %d", cfg.Admin.MaxRequestBodyBytes, 1<<20)
	}
	if cfg.Admin.MaxConcurrentRequests != 128 {
		t.Fatalf("max concurrent requests = %d, want 128", cfg.Admin.MaxConcurrentRequests)
	}
	if cfg.Database.MaxOpenConns != 20 || cfg.Database.MaxIdleConns != 10 {
		t.Fatalf("unexpected database pool defaults: %+v", cfg.Database)
	}
	if cfg.Database.ConnMaxLifetimeSeconds != 30*60 || cfg.Database.ConnMaxIdleTimeSeconds != 5*60 {
		t.Fatalf("unexpected database lifetime defaults: %+v", cfg.Database)
	}
	if cfg.Database.PingTimeoutSeconds != 5 {
		t.Fatalf("database ping timeout = %d, want 5", cfg.Database.PingTimeoutSeconds)
	}
}

func TestLoadAppliesAdminAndDatabaseEnvironment(t *testing.T) {
	t.Setenv("JXH_ADMIN_ADDR", ":9090")
	t.Setenv("JXH_ADMIN_PUBLIC_ORIGIN", "https://manager.example.test")
	t.Setenv("JXH_ADMIN_SESSION_SECRET", "test-only-secret")
	t.Setenv("JXH_ADMIN_COOKIE_SECURE", "false")
	t.Setenv("JXH_ADMIN_TRUSTED_PROXIES", "127.0.0.1/32, 10.0.0.0/8")
	t.Setenv("JXH_ADMIN_SESSION_TTL_SECONDS", "7200")
	t.Setenv("JXH_ADMIN_MAX_CONCURRENT_REQUESTS", "17")
	t.Setenv("JXH_DATABASE_MAX_OPEN_CONNS", "31")
	t.Setenv("JXH_DATABASE_PING_TIMEOUT_SECONDS", "9")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Admin.Addr != ":9090" || cfg.Admin.PublicOrigin != "https://manager.example.test" {
		t.Fatalf("admin addr/origin = %q/%q", cfg.Admin.Addr, cfg.Admin.PublicOrigin)
	}
	if cfg.Admin.SessionSecret != "test-only-secret" || cfg.Admin.CookieSecure {
		t.Fatalf("admin secret configured = %t, cookie secure = %t", cfg.Admin.SessionSecret != "", cfg.Admin.CookieSecure)
	}
	if len(cfg.Admin.TrustedProxies) != 2 || cfg.Admin.TrustedProxies[1] != "10.0.0.0/8" {
		t.Fatalf("trusted proxies = %#v", cfg.Admin.TrustedProxies)
	}
	if cfg.Admin.SessionTTLSeconds != 7200 {
		t.Fatalf("session ttl = %d, want 7200", cfg.Admin.SessionTTLSeconds)
	}
	if cfg.Admin.MaxConcurrentRequests != 17 {
		t.Fatalf("max concurrent requests = %d, want 17", cfg.Admin.MaxConcurrentRequests)
	}
	if cfg.Database.MaxOpenConns != 31 || cfg.Database.PingTimeoutSeconds != 9 {
		t.Fatalf("max open conns/ping timeout = %d/%d", cfg.Database.MaxOpenConns, cfg.Database.PingTimeoutSeconds)
	}
}

func TestLoadRejectsInvalidAdminEnvironment(t *testing.T) {
	t.Setenv("JXH_ADMIN_COOKIE_SECURE", "sometimes")

	if _, err := Load(""); err == nil {
		t.Fatal("Load() error = nil, want invalid boolean error")
	}
}

func TestLoadRedactsInvalidAdminEnvironmentValue(t *testing.T) {
	const (
		key         = "JXH_ADMIN_COOKIE_SECURE"
		secretValue = "invalid-sensitive-cookie-value-7f03"
	)
	t.Setenv(key, secretValue)

	_, err := Load("")
	if err == nil {
		t.Fatal("Load() error = nil, want invalid boolean error")
	}
	errorText := err.Error()
	if strings.Contains(errorText, secretValue) {
		t.Fatal("Load() error exposes the raw environment value")
	}
	if !strings.Contains(errorText, key) {
		t.Fatal("Load() error does not identify the invalid environment variable")
	}
	if !strings.Contains(errorText, "boolean") {
		t.Fatal("Load() error does not include the expected boolean type")
	}
}

func TestLoadNormalizesNonPositiveRuntimeLimits(t *testing.T) {
	t.Setenv("JXH_ADMIN_SESSION_TTL_SECONDS", "0")
	t.Setenv("JXH_ADMIN_MAX_CONCURRENT_REQUESTS", "-1")
	t.Setenv("JXH_DATABASE_MAX_OPEN_CONNS", "-1")
	t.Setenv("JXH_DATABASE_MAX_IDLE_CONNS", "0")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Admin.SessionTTLSeconds != 12*60*60 {
		t.Fatalf("session ttl = %d, want default", cfg.Admin.SessionTTLSeconds)
	}
	if cfg.Admin.MaxConcurrentRequests != 128 {
		t.Fatalf("max concurrent requests = %d, want default", cfg.Admin.MaxConcurrentRequests)
	}
	if cfg.Database.MaxOpenConns != 20 {
		t.Fatalf("max open conns = %d, want default", cfg.Database.MaxOpenConns)
	}
	if cfg.Database.MaxIdleConns != 10 {
		t.Fatalf("max idle conns = %d, want default", cfg.Database.MaxIdleConns)
	}
}

func TestLoadRecordsTheConfigurationSourcePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("app:\n  timezone: Asia/Shanghai\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SourcePath != path {
		t.Fatalf("source path = %q, want %q", cfg.SourcePath, path)
	}
}

func TestLoadRecordsSourceVersionAndRestartMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	contents := []byte("app:\n  timezone: Asia/Shanghai\n")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JXH_BOT_RESTART_MODE", "supervised_exit")

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SourceVersion != versionFor(contents) {
		t.Fatalf("source version = %d, want %d", cfg.SourceVersion, versionFor(contents))
	}
	if cfg.BotRestartMode != BotRestartSupervisedExit {
		t.Fatalf("restart mode = %q", cfg.BotRestartMode)
	}

	t.Setenv("JXH_BOT_RESTART_MODE", "unsupported")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "JXH_BOT_RESTART_MODE") {
		t.Fatalf("Load() error = %v, want restart mode validation", err)
	}
}
