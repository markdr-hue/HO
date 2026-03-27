/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

type Config struct {
	AdminPort      int      `json:"admin_port"`
	PublicPort     int      `json:"public_port"`
	DataDir        string   `json:"data_dir"`
	DBPath         string   `json:"db_path"`
	JWTSecret      string   `json:"jwt_secret"`
	EncryptionKey  string   `json:"encryption_key"`
	LogLevel       string   `json:"log_level"`
	SeedPath       string   `json:"seed_path"`
	CaddyEnabled   bool     `json:"caddy_enabled"`
	CORSOrigins    []string `json:"cors_origins"`
	RateLimitRate  float64  `json:"rate_limit_rate"`
	RateLimitBurst int      `json:"rate_limit_burst"`

	// Brain monitoring intervals (seconds). Zero means use built-in defaults.
	BrainMonitoringBaseSec int `json:"brain_monitoring_base_sec"`
	BrainMonitoringMaxSec  int `json:"brain_monitoring_max_sec"`

	// LLM HTTP client timeout in seconds. Zero means use default (180s / 3 minutes).
	LLMTimeoutSec int `json:"llm_timeout_sec"`

	// Tunnel sharing via humansout.com subdomains.
	TunnelBrokerURL string `json:"tunnel_broker_url"`
	TunnelToken     string `json:"tunnel_token"`
}

func Load() (*Config, error) {
	cfg := defaults()

	// Try loading from config.json
	if data, err := os.ReadFile("config.json"); err == nil {
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parsing config.json: %w", err)
		}
	}

	// Environment variable overrides
	applyEnvOverrides(cfg)

	// Ensure data directory exists
	if err := os.MkdirAll(cfg.DataDir, 0750); err != nil {
		return nil, fmt.Errorf("creating data dir: %w", err)
	}

	// Set DB path relative to data dir if not explicitly set
	if cfg.DBPath == "" {
		cfg.DBPath = filepath.Join(cfg.DataDir, "ho.db")
	}

	// Auto-generate security keys on first run
	securityDir := filepath.Join(cfg.DataDir, ".security")
	if err := os.MkdirAll(securityDir, 0700); err != nil {
		return nil, fmt.Errorf("creating security dir: %w", err)
	}

	if cfg.JWTSecret == "" {
		secret, err := loadOrGenerateKey(filepath.Join(securityDir, "jwt.key"), 32)
		if err != nil {
			return nil, fmt.Errorf("jwt key: %w", err)
		}
		cfg.JWTSecret = secret
	}
	if len(cfg.JWTSecret) < 32 {
		return nil, fmt.Errorf("JWT secret must be at least 32 characters (got %d)", len(cfg.JWTSecret))
	}

	if cfg.EncryptionKey == "" {
		key, err := loadOrGenerateKey(filepath.Join(securityDir, "encryption.key"), 32)
		if err != nil {
			return nil, fmt.Errorf("encryption key: %w", err)
		}
		cfg.EncryptionKey = key
	}
	if len(cfg.EncryptionKey) < 64 {
		return nil, fmt.Errorf("encryption key must be at least 64 hex characters (32 bytes), got %d", len(cfg.EncryptionKey))
	}

	// Validate port ranges.
	if cfg.AdminPort < 1 || cfg.AdminPort > 65535 {
		return nil, fmt.Errorf("admin_port must be 1-65535, got %d", cfg.AdminPort)
	}
	if cfg.PublicPort < 1 || cfg.PublicPort > 65535 {
		return nil, fmt.Errorf("public_port must be 1-65535, got %d", cfg.PublicPort)
	}

	return cfg, nil
}

func defaults() *Config {
	return &Config{
		AdminPort:      DefaultAdminPort,
		PublicPort:     DefaultPublicPort,
		DataDir:        DefaultDataDir,
		LogLevel:       DefaultLogLevel,
		SeedPath:       DefaultSeedPath,
		CaddyEnabled:   false,
		RateLimitRate:   DefaultRateLimitRate,
		RateLimitBurst:  DefaultRateLimitBurst,
		TunnelBrokerURL: DefaultTunnelBrokerURL,
	}
}

func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("HO_ADMIN_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			cfg.AdminPort = p
		}
	}
	if v := os.Getenv("HO_PUBLIC_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			cfg.PublicPort = p
		}
	}
	if v := os.Getenv("HO_DATA_DIR"); v != "" {
		cfg.DataDir = v
	}
	if v := os.Getenv("HO_DB_PATH"); v != "" {
		cfg.DBPath = v
	}
	if v := os.Getenv("HO_JWT_SECRET"); v != "" {
		cfg.JWTSecret = v
	}
	if v := os.Getenv("HO_ENCRYPTION_KEY"); v != "" {
		cfg.EncryptionKey = v
	}
	if v := os.Getenv("HO_LOG_LEVEL"); v != "" {
		cfg.LogLevel = v
	}
	if v := os.Getenv("HO_SEED_PATH"); v != "" {
		cfg.SeedPath = v
	}
	if v := os.Getenv("HO_CADDY_ENABLED"); v == "true" || v == "1" {
		cfg.CaddyEnabled = true
	}
	if v := os.Getenv("HO_RATE_LIMIT_RATE"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			cfg.RateLimitRate = f
		}
	}
	if v := os.Getenv("HO_RATE_LIMIT_BURST"); v != "" {
		if b, err := strconv.Atoi(v); err == nil && b > 0 {
			cfg.RateLimitBurst = b
		}
	}
	if v := os.Getenv("HO_LLM_TIMEOUT"); v != "" {
		if t, err := strconv.Atoi(v); err == nil && t > 0 {
			cfg.LLMTimeoutSec = t
		}
	}
	if v := os.Getenv("HO_TUNNEL_BROKER_URL"); v != "" {
		cfg.TunnelBrokerURL = v
	}
	if v := os.Getenv("HO_TUNNEL_TOKEN"); v != "" {
		cfg.TunnelToken = v
	}
}

// LLMTimeout returns the configured LLM HTTP client timeout as a time.Duration.
func (c *Config) LLMTimeout() time.Duration {
	if c.LLMTimeoutSec > 0 {
		return time.Duration(c.LLMTimeoutSec) * time.Second
	}
	return time.Duration(DefaultLLMTimeoutSec) * time.Second
}

func loadOrGenerateKey(path string, size int) (string, error) {
	if data, err := os.ReadFile(path); err == nil {
		return string(data), nil
	}

	key := make([]byte, size)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}

	hexKey := hex.EncodeToString(key)
	if err := os.WriteFile(path, []byte(hexKey), 0600); err != nil {
		return "", err
	}

	slog.Info("generated security key", "path", path)
	return hexKey, nil
}
