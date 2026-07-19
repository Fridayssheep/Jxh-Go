package config

import "testing"

func TestLoadAppliesRedisEnv(t *testing.T) {
	t.Setenv("JXH_REDIS_ADDR", "redis:6379")
	t.Setenv("JXH_REDIS_PASSWORD", "secret")
	t.Setenv("JXH_REDIS_DB", "2")
	t.Setenv("JXH_REDIS_DAILY_RETENTION_DAYS", "30")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Redis.Addr != "redis:6379" {
		t.Fatalf("redis addr = %q", cfg.Redis.Addr)
	}
	if cfg.Redis.Password != "secret" {
		t.Fatalf("redis password = %q", cfg.Redis.Password)
	}
	if cfg.Redis.DB != 2 {
		t.Fatalf("redis db = %d", cfg.Redis.DB)
	}
	if cfg.Redis.DailyRetentionDays != 30 {
		t.Fatalf("redis retention days = %d", cfg.Redis.DailyRetentionDays)
	}
}
