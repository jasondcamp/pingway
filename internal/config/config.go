// Package config loads pingway configuration with the precedence:
// UI edits (DB) > env vars > YAML file > defaults. This package handles
// the env/YAML/defaults layers; the DB layer is applied by the store at
// startup.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	DefaultListen             = ":8080"
	DefaultPingIntervalMs     = 1000
	DefaultPingTimeoutMs      = 2000
	DefaultSpeedtestEngine    = "librespeed"
	DefaultSpeedtestInterval  = 45
	DefaultFailureThreshold   = 5
	DefaultRecoveryThreshold  = 3
	DefaultRawRetentionHours  = 48
	DefaultRollup1mRetireDays = 30
)

type Config struct {
	Listen    string          `yaml:"listen"`
	DataDir   string          `yaml:"data_dir"`
	Ping      PingConfig      `yaml:"ping"`
	Speedtest SpeedtestConfig `yaml:"speedtest"`
	Outage    OutageConfig    `yaml:"outage"`
	Retention RetentionConfig `yaml:"retention"`
	Targets   []TargetSpec    `yaml:"targets"`
	// ConfigLock prevents UI edits from being persisted as source of truth;
	// YAML/env are re-applied on every boot.
	ConfigLock bool `yaml:"config_lock"`
	// LogFormat is "json" or "text". Default: json when not a terminal.
	LogFormat string `yaml:"log_format"`
}

type PingConfig struct {
	IntervalMs int `yaml:"interval_ms"`
	TimeoutMs  int `yaml:"timeout_ms"`
}

type SpeedtestConfig struct {
	Engine          string `yaml:"engine"`
	IntervalMinutes int    `yaml:"interval_minutes"`
	Enabled         *bool  `yaml:"enabled"` // pointer so YAML absence != false
	OoklaAcceptEULA bool   `yaml:"ookla_accept_eula"`
}

func (s SpeedtestConfig) IsEnabled() bool {
	return s.Enabled == nil || *s.Enabled
}

type OutageConfig struct {
	FailureThreshold  int `yaml:"failure_threshold"`
	RecoveryThreshold int `yaml:"recovery_threshold"`
}

type RetentionConfig struct {
	RawHours     int `yaml:"raw_hours"`
	Rollup1mDays int `yaml:"rollup_1m_days"`
}

type TargetSpec struct {
	Name string `yaml:"name"`
	Host string `yaml:"host"`
	Tier int    `yaml:"tier"`
}

// Load builds the effective config: defaults, overlaid by the YAML file at
// path (if it exists), overlaid by environment variables. An empty path
// checks the conventional /config/config.yaml location.
func Load(path string) (*Config, error) {
	cfg := defaults()

	if path == "" {
		path = "/config/config.yaml"
	}
	if b, err := os.ReadFile(path); err == nil {
		if err := yaml.Unmarshal(b, cfg); err != nil {
			return nil, fmt.Errorf("parse config file %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read config file %s: %w", path, err)
	}

	if err := applyEnv(cfg); err != nil {
		return nil, err
	}
	normalize(cfg)
	return cfg, nil
}

func defaults() *Config {
	return &Config{
		Listen:  DefaultListen,
		DataDir: "/data",
		Ping: PingConfig{
			IntervalMs: DefaultPingIntervalMs,
			TimeoutMs:  DefaultPingTimeoutMs,
		},
		Speedtest: SpeedtestConfig{
			Engine:          DefaultSpeedtestEngine,
			IntervalMinutes: DefaultSpeedtestInterval,
		},
		Outage: OutageConfig{
			FailureThreshold:  DefaultFailureThreshold,
			RecoveryThreshold: DefaultRecoveryThreshold,
		},
		Retention: RetentionConfig{
			RawHours:     DefaultRawRetentionHours,
			Rollup1mDays: DefaultRollup1mRetireDays,
		},
	}
}

func applyEnv(cfg *Config) error {
	if v := os.Getenv("LISTEN"); v != "" {
		cfg.Listen = v
	}
	if v := os.Getenv("DATA_DIR"); v != "" {
		cfg.DataDir = v
	}
	if v := os.Getenv("PING_INTERVAL_MS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("PING_INTERVAL_MS: %w", err)
		}
		cfg.Ping.IntervalMs = n
	}
	if v := os.Getenv("PING_TIMEOUT_MS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("PING_TIMEOUT_MS: %w", err)
		}
		cfg.Ping.TimeoutMs = n
	}
	if v := os.Getenv("SPEEDTEST_ENGINE"); v != "" {
		cfg.Speedtest.Engine = v
	}
	if v := os.Getenv("SPEEDTEST_INTERVAL_MINUTES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("SPEEDTEST_INTERVAL_MINUTES: %w", err)
		}
		cfg.Speedtest.IntervalMinutes = n
	}
	if v := os.Getenv("SPEEDTEST_ENABLED"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("SPEEDTEST_ENABLED: %w", err)
		}
		cfg.Speedtest.Enabled = &b
	}
	if v := os.Getenv("OOKLA_ACCEPT_EULA"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("OOKLA_ACCEPT_EULA: %w", err)
		}
		cfg.Speedtest.OoklaAcceptEULA = b
	}
	if v := os.Getenv("CONFIG_LOCK"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("CONFIG_LOCK: %w", err)
		}
		cfg.ConfigLock = b
	}
	if v := os.Getenv("LOG_FORMAT"); v != "" {
		cfg.LogFormat = v
	}
	if v := os.Getenv("TARGETS"); v != "" {
		targets, err := ParseTargetsEnv(v)
		if err != nil {
			return err
		}
		cfg.Targets = targets
	}
	return nil
}

// ParseTargetsEnv parses "Name:host:tier,Name:host:tier,...".
// Tier defaults to 3 when omitted.
func ParseTargetsEnv(s string) ([]TargetSpec, error) {
	var out []TargetSpec
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		fields := strings.Split(part, ":")
		if len(fields) < 2 || len(fields) > 3 {
			return nil, fmt.Errorf("TARGETS: bad entry %q (want Name:host[:tier])", part)
		}
		t := TargetSpec{Name: strings.TrimSpace(fields[0]), Host: strings.TrimSpace(fields[1]), Tier: 3}
		if t.Name == "" || t.Host == "" {
			return nil, fmt.Errorf("TARGETS: bad entry %q (empty name or host)", part)
		}
		if len(fields) == 3 {
			tier, err := strconv.Atoi(strings.TrimSpace(fields[2]))
			if err != nil || tier < 1 || tier > 3 {
				return nil, fmt.Errorf("TARGETS: bad tier in %q (want 1-3)", part)
			}
			t.Tier = tier
		}
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("TARGETS: no valid entries in %q", s)
	}
	return out, nil
}

func normalize(cfg *Config) {
	if cfg.Ping.IntervalMs < 100 {
		cfg.Ping.IntervalMs = DefaultPingIntervalMs
	}
	if cfg.Ping.TimeoutMs < 100 {
		cfg.Ping.TimeoutMs = DefaultPingTimeoutMs
	}
	if cfg.Speedtest.IntervalMinutes < 1 {
		cfg.Speedtest.IntervalMinutes = DefaultSpeedtestInterval
	}
	if cfg.Outage.FailureThreshold < 1 {
		cfg.Outage.FailureThreshold = DefaultFailureThreshold
	}
	if cfg.Outage.RecoveryThreshold < 1 {
		cfg.Outage.RecoveryThreshold = DefaultRecoveryThreshold
	}
	if cfg.Retention.RawHours < 1 {
		cfg.Retention.RawHours = DefaultRawRetentionHours
	}
	if cfg.Retention.Rollup1mDays < 1 {
		cfg.Retention.Rollup1mDays = DefaultRollup1mRetireDays
	}
	for i := range cfg.Targets {
		if cfg.Targets[i].Tier < 1 || cfg.Targets[i].Tier > 3 {
			cfg.Targets[i].Tier = 3
		}
	}
}

// DefaultTargets returns the zero-config target set: the default gateway
// (if detectable) as tier 1, plus two public anchors as tier 3.
func DefaultTargets() []TargetSpec {
	var out []TargetSpec
	if gw, err := DefaultGateway(); err == nil && gw != "" {
		out = append(out, TargetSpec{Name: "Gateway", Host: gw, Tier: 1})
	}
	out = append(out,
		TargetSpec{Name: "Cloudflare", Host: "1.1.1.1", Tier: 3},
		TargetSpec{Name: "Google DNS", Host: "8.8.8.8", Tier: 3},
	)
	return out
}
