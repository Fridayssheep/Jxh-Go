package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	App       AppConfig       `yaml:"app"`
	Admin     AdminConfig     `yaml:"admin"`
	OneBot    OneBotConfig    `yaml:"onebot"`
	WPS       WPSConfig       `yaml:"wps"`
	Database  DatabaseConfig  `yaml:"database"`
	AI        AIConfig        `yaml:"ai"`
	Quote     QuoteConfig     `yaml:"quote"`
	Scheduler SchedulerConfig `yaml:"scheduler"`
}

type AppConfig struct {
	Timezone string `yaml:"timezone"`
}

type AdminConfig struct {
	Addr                      string   `yaml:"addr"`
	PublicOrigin              string   `yaml:"public_origin"`
	SessionSecret             string   `yaml:"session_secret"`
	CookieSecure              bool     `yaml:"cookie_secure"`
	SessionTTLSeconds         int      `yaml:"session_ttl_seconds"`
	SessionIdleTimeoutSeconds int      `yaml:"session_idle_timeout_seconds"`
	LoginWindowSeconds        int      `yaml:"login_window_seconds"`
	LoginMaxAttempts          int      `yaml:"login_max_attempts"`
	TrustedProxies            []string `yaml:"trusted_proxies"`
	ReadHeaderTimeoutSeconds  int      `yaml:"read_header_timeout_seconds"`
	ReadTimeoutSeconds        int      `yaml:"read_timeout_seconds"`
	WriteTimeoutSeconds       int      `yaml:"write_timeout_seconds"`
	IdleTimeoutSeconds        int      `yaml:"idle_timeout_seconds"`
	ShutdownTimeoutSeconds    int      `yaml:"shutdown_timeout_seconds"`
	MaxRequestBodyBytes       int64    `yaml:"max_request_body_bytes"`
	MaxConcurrentRequests     int      `yaml:"max_concurrent_requests"`
}

type OneBotConfig struct {
	WSURL                string `yaml:"ws_url"`
	AccessToken          string `yaml:"access_token"`
	APITimeoutSec        int    `yaml:"api_timeout_sec"`
	ReconnectIntervalSec int    `yaml:"reconnect_interval_sec"`
}

type WPSConfig struct {
	ShareURL   string `yaml:"share_url"`
	SID        string `yaml:"sid"`
	Sheet      string `yaml:"sheet"`
	CacheFile  string `yaml:"cache_file"`
	TimeoutSec int    `yaml:"timeout_sec"`
}

type DatabaseConfig struct {
	Host                   string `yaml:"host"`
	Port                   int    `yaml:"port"`
	User                   string `yaml:"user"`
	Password               string `yaml:"password"`
	Name                   string `yaml:"name"`
	Charset                string `yaml:"charset"`
	ParseTime              bool   `yaml:"parse_time"`
	Loc                    string `yaml:"loc"`
	DSN                    string `yaml:"dsn"`
	MaxOpenConns           int    `yaml:"max_open_conns"`
	MaxIdleConns           int    `yaml:"max_idle_conns"`
	ConnMaxLifetimeSeconds int    `yaml:"conn_max_lifetime_seconds"`
	ConnMaxIdleTimeSeconds int    `yaml:"conn_max_idle_time_seconds"`
	PingTimeoutSeconds     int    `yaml:"ping_timeout_seconds"`
	// TriggerLogRetentionDays controls how many days of trigger logs to keep.
	// Zero or negative disables automatic purging.
	TriggerLogRetentionDays int `yaml:"trigger_log_retention_days"`
}

type AIConfig struct {
	Enabled          bool   `yaml:"enabled"`
	Provider         string `yaml:"provider"`
	BaseURL          string `yaml:"base_url"`
	APIKey           string `yaml:"api_key"`
	Model            string `yaml:"model"`
	TimeoutSec       int    `yaml:"timeout_sec"`
	MaxQuestionChars int    `yaml:"max_question_chars"`
}

type QuoteConfig struct {
	BaseURL    string `yaml:"base_url"`
	TimeoutSec int    `yaml:"timeout_sec"`
}

type SchedulerConfig struct {
	Timezone string `yaml:"timezone"`
}

func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return Config{}, err
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return Config{}, err
		}
	}
	if err := applyEnv(&cfg); err != nil {
		return Config{}, err
	}
	normalize(&cfg)
	return cfg, nil
}

func Default() Config {
	return Config{
		App: AppConfig{Timezone: "Asia/Shanghai"},
		Admin: AdminConfig{
			Addr:                      "127.0.0.1:8090",
			CookieSecure:              true,
			SessionTTLSeconds:         12 * 60 * 60,
			SessionIdleTimeoutSeconds: 30 * 60,
			LoginWindowSeconds:        5 * 60,
			LoginMaxAttempts:          5,
			ReadHeaderTimeoutSeconds:  5,
			ReadTimeoutSeconds:        15,
			WriteTimeoutSeconds:       30,
			IdleTimeoutSeconds:        60,
			ShutdownTimeoutSeconds:    10,
			MaxRequestBodyBytes:       1 << 20,
			MaxConcurrentRequests:     128,
		},
		OneBot: OneBotConfig{
			WSURL:                "ws://127.0.0.1:3001",
			APITimeoutSec:        30,
			ReconnectIntervalSec: 5,
		},
		WPS: WPSConfig{
			Sheet:      "release",
			CacheFile:  "./data/cache/knowledge.xlsx",
			TimeoutSec: 120,
		},
		Database: DatabaseConfig{
			Host:                    "127.0.0.1",
			Port:                    3306,
			User:                    "jxh",
			Name:                    "jxh_bot",
			Charset:                 "utf8mb4",
			ParseTime:               true,
			Loc:                     "Local",
			MaxOpenConns:            20,
			MaxIdleConns:            10,
			ConnMaxLifetimeSeconds:  30 * 60,
			ConnMaxIdleTimeSeconds:  5 * 60,
			PingTimeoutSeconds:      5,
			TriggerLogRetentionDays: 180,
		},
		AI: AIConfig{
			Enabled:          true,
			Provider:         "openai",
			TimeoutSec:       30,
			MaxQuestionChars: 500,
		},
		Quote:     QuoteConfig{BaseURL: "http://quote:5000", TimeoutSec: 10},
		Scheduler: SchedulerConfig{Timezone: "Asia/Shanghai"},
	}
}

func applyEnv(cfg *Config) error {
	override := func(key string, set func(string)) {
		if value := os.Getenv(key); value != "" {
			set(value)
		}
	}
	overrideInt := func(key string, set func(int)) error {
		value := os.Getenv(key)
		if value == "" {
			return nil
		}
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("%s: invalid integer", key)
		}
		set(parsed)
		return nil
	}
	overrideInt64 := func(key string, set func(int64)) error {
		value := os.Getenv(key)
		if value == "" {
			return nil
		}
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return fmt.Errorf("%s: invalid integer", key)
		}
		set(parsed)
		return nil
	}
	overrideBool := func(key string, set func(bool)) error {
		value := os.Getenv(key)
		if value == "" {
			return nil
		}
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("%s: invalid boolean", key)
		}
		set(parsed)
		return nil
	}

	override("JXH_ADMIN_ADDR", func(v string) { cfg.Admin.Addr = v })
	override("JXH_ADMIN_PUBLIC_ORIGIN", func(v string) { cfg.Admin.PublicOrigin = v })
	override("JXH_ADMIN_SESSION_SECRET", func(v string) { cfg.Admin.SessionSecret = v })
	override("JXH_ADMIN_TRUSTED_PROXIES", func(v string) {
		cfg.Admin.TrustedProxies = splitCommaSeparated(v)
	})
	if err := overrideBool("JXH_ADMIN_COOKIE_SECURE", func(v bool) { cfg.Admin.CookieSecure = v }); err != nil {
		return err
	}
	adminInts := []struct {
		key string
		set func(int)
	}{
		{"JXH_ADMIN_SESSION_TTL_SECONDS", func(v int) { cfg.Admin.SessionTTLSeconds = v }},
		{"JXH_ADMIN_SESSION_IDLE_TIMEOUT_SECONDS", func(v int) { cfg.Admin.SessionIdleTimeoutSeconds = v }},
		{"JXH_ADMIN_LOGIN_WINDOW_SECONDS", func(v int) { cfg.Admin.LoginWindowSeconds = v }},
		{"JXH_ADMIN_LOGIN_MAX_ATTEMPTS", func(v int) { cfg.Admin.LoginMaxAttempts = v }},
		{"JXH_ADMIN_READ_HEADER_TIMEOUT_SECONDS", func(v int) { cfg.Admin.ReadHeaderTimeoutSeconds = v }},
		{"JXH_ADMIN_READ_TIMEOUT_SECONDS", func(v int) { cfg.Admin.ReadTimeoutSeconds = v }},
		{"JXH_ADMIN_WRITE_TIMEOUT_SECONDS", func(v int) { cfg.Admin.WriteTimeoutSeconds = v }},
		{"JXH_ADMIN_IDLE_TIMEOUT_SECONDS", func(v int) { cfg.Admin.IdleTimeoutSeconds = v }},
		{"JXH_ADMIN_SHUTDOWN_TIMEOUT_SECONDS", func(v int) { cfg.Admin.ShutdownTimeoutSeconds = v }},
		{"JXH_ADMIN_MAX_CONCURRENT_REQUESTS", func(v int) { cfg.Admin.MaxConcurrentRequests = v }},
	}
	for _, item := range adminInts {
		if err := overrideInt(item.key, item.set); err != nil {
			return err
		}
	}
	if err := overrideInt64("JXH_ADMIN_MAX_REQUEST_BODY_BYTES", func(v int64) { cfg.Admin.MaxRequestBodyBytes = v }); err != nil {
		return err
	}
	override("JXH_ONEBOT_TOKEN", func(v string) { cfg.OneBot.AccessToken = v })
	override("JXH_ONEBOT_WS_URL", func(v string) { cfg.OneBot.WSURL = v })
	override("JXH_DATABASE_HOST", func(v string) { cfg.Database.Host = v })
	if err := overrideInt("JXH_DATABASE_PORT", func(v int) { cfg.Database.Port = v }); err != nil {
		return err
	}
	override("JXH_DATABASE_USER", func(v string) { cfg.Database.User = v })
	override("JXH_DATABASE_NAME", func(v string) { cfg.Database.Name = v })
	override("JXH_DATABASE_CHARSET", func(v string) { cfg.Database.Charset = v })
	if err := overrideBool("JXH_DATABASE_PARSE_TIME", func(v bool) { cfg.Database.ParseTime = v }); err != nil {
		return err
	}
	override("JXH_DATABASE_LOC", func(v string) { cfg.Database.Loc = v })
	databaseInts := []struct {
		key string
		set func(int)
	}{
		{"JXH_DATABASE_MAX_OPEN_CONNS", func(v int) { cfg.Database.MaxOpenConns = v }},
		{"JXH_DATABASE_MAX_IDLE_CONNS", func(v int) { cfg.Database.MaxIdleConns = v }},
		{"JXH_DATABASE_CONN_MAX_LIFETIME_SECONDS", func(v int) { cfg.Database.ConnMaxLifetimeSeconds = v }},
		{"JXH_DATABASE_CONN_MAX_IDLE_TIME_SECONDS", func(v int) { cfg.Database.ConnMaxIdleTimeSeconds = v }},
		{"JXH_DATABASE_PING_TIMEOUT_SECONDS", func(v int) { cfg.Database.PingTimeoutSeconds = v }},
	}
	for _, item := range databaseInts {
		if err := overrideInt(item.key, item.set); err != nil {
			return err
		}
	}
	override("JXH_WPS_SID", func(v string) { cfg.WPS.SID = v })
	override("JXH_WPS_SHARE_URL", func(v string) { cfg.WPS.ShareURL = v })
	if err := overrideInt("JXH_WPS_TIMEOUT_SEC", func(v int) { cfg.WPS.TimeoutSec = v }); err != nil {
		return err
	}
	override("JXH_MYSQL_PASSWORD", func(v string) { cfg.Database.Password = v })
	override("JXH_MYSQL_DSN", func(v string) { cfg.Database.DSN = v })
	override("JXH_QUOTE_BASE_URL", func(v string) { cfg.Quote.BaseURL = v })
	override("JXH_AI_PROVIDER", func(v string) { cfg.AI.Provider = v })
	override("JXH_AI_BASE_URL", func(v string) { cfg.AI.BaseURL = v })
	override("JXH_AI_API_KEY", func(v string) { cfg.AI.APIKey = v })
	override("JXH_AI_MODEL", func(v string) { cfg.AI.Model = v })
	return nil
}

func normalize(cfg *Config) {
	defaults := Default()
	if strings.TrimSpace(cfg.Admin.Addr) == "" {
		cfg.Admin.Addr = defaults.Admin.Addr
	}
	if cfg.Admin.SessionTTLSeconds <= 0 {
		cfg.Admin.SessionTTLSeconds = defaults.Admin.SessionTTLSeconds
	}
	if cfg.Admin.SessionIdleTimeoutSeconds <= 0 {
		cfg.Admin.SessionIdleTimeoutSeconds = defaults.Admin.SessionIdleTimeoutSeconds
	}
	if cfg.Admin.LoginWindowSeconds <= 0 {
		cfg.Admin.LoginWindowSeconds = defaults.Admin.LoginWindowSeconds
	}
	if cfg.Admin.LoginMaxAttempts <= 0 {
		cfg.Admin.LoginMaxAttempts = defaults.Admin.LoginMaxAttempts
	}
	if cfg.Admin.ReadHeaderTimeoutSeconds <= 0 {
		cfg.Admin.ReadHeaderTimeoutSeconds = defaults.Admin.ReadHeaderTimeoutSeconds
	}
	if cfg.Admin.ReadTimeoutSeconds <= 0 {
		cfg.Admin.ReadTimeoutSeconds = defaults.Admin.ReadTimeoutSeconds
	}
	if cfg.Admin.WriteTimeoutSeconds <= 0 {
		cfg.Admin.WriteTimeoutSeconds = defaults.Admin.WriteTimeoutSeconds
	}
	if cfg.Admin.IdleTimeoutSeconds <= 0 {
		cfg.Admin.IdleTimeoutSeconds = defaults.Admin.IdleTimeoutSeconds
	}
	if cfg.Admin.ShutdownTimeoutSeconds <= 0 {
		cfg.Admin.ShutdownTimeoutSeconds = defaults.Admin.ShutdownTimeoutSeconds
	}
	if cfg.Admin.MaxRequestBodyBytes <= 0 {
		cfg.Admin.MaxRequestBodyBytes = defaults.Admin.MaxRequestBodyBytes
	}
	if cfg.Admin.MaxConcurrentRequests <= 0 {
		cfg.Admin.MaxConcurrentRequests = defaults.Admin.MaxConcurrentRequests
	}
	if cfg.Database.MaxOpenConns <= 0 {
		cfg.Database.MaxOpenConns = defaults.Database.MaxOpenConns
	}
	if cfg.Database.MaxIdleConns <= 0 {
		cfg.Database.MaxIdleConns = defaults.Database.MaxIdleConns
	}
	if cfg.Database.MaxIdleConns > cfg.Database.MaxOpenConns {
		cfg.Database.MaxIdleConns = cfg.Database.MaxOpenConns
	}
	if cfg.Database.ConnMaxLifetimeSeconds <= 0 {
		cfg.Database.ConnMaxLifetimeSeconds = defaults.Database.ConnMaxLifetimeSeconds
	}
	if cfg.Database.ConnMaxIdleTimeSeconds <= 0 {
		cfg.Database.ConnMaxIdleTimeSeconds = defaults.Database.ConnMaxIdleTimeSeconds
	}
	if cfg.Database.PingTimeoutSeconds <= 0 {
		cfg.Database.PingTimeoutSeconds = defaults.Database.PingTimeoutSeconds
	}
	if cfg.WPS.Sheet == "" {
		cfg.WPS.Sheet = "release"
	}
	if cfg.WPS.TimeoutSec <= 0 {
		cfg.WPS.TimeoutSec = 120
	}
	if cfg.OneBot.APITimeoutSec <= 0 {
		cfg.OneBot.APITimeoutSec = 30
	}
	if cfg.OneBot.ReconnectIntervalSec <= 0 {
		cfg.OneBot.ReconnectIntervalSec = 5
	}
	if cfg.AI.TimeoutSec <= 0 {
		cfg.AI.TimeoutSec = 30
	}
	if cfg.AI.MaxQuestionChars <= 0 {
		cfg.AI.MaxQuestionChars = 500
	}
	if cfg.AI.Provider == "" {
		cfg.AI.Provider = "openai"
	}
	if cfg.Quote.TimeoutSec <= 0 {
		cfg.Quote.TimeoutSec = 10
	}
	if cfg.Scheduler.Timezone == "" {
		cfg.Scheduler.Timezone = cfg.App.Timezone
	}
}

func splitCommaSeparated(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}
