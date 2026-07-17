package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"LISTEN", "DATA_DIR", "PING_INTERVAL_MS", "PING_TIMEOUT_MS",
		"SPEEDTEST_ENGINE", "SPEEDTEST_INTERVAL_MINUTES", "SPEEDTEST_ENABLED",
		"OOKLA_ACCEPT_EULA", "CONFIG_LOCK", "LOG_FORMAT", "TARGETS"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}

func TestLoadDefaults(t *testing.T) {
	clearEnv(t)
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != ":8080" || cfg.Ping.IntervalMs != 1000 || cfg.Ping.TimeoutMs != 2000 {
		t.Fatalf("defaults wrong: %+v", cfg)
	}
	if cfg.Speedtest.Engine != "librespeed" || cfg.Speedtest.IntervalMinutes != 45 || !cfg.Speedtest.IsEnabled() {
		t.Fatalf("speedtest defaults wrong: %+v", cfg.Speedtest)
	}
}

func TestLoadYAMLOverridesDefaults(t *testing.T) {
	clearEnv(t)
	p := writeFile(t, `
listen: ":9090"
ping:
  interval_ms: 500
speedtest:
  engine: cloudflare
  enabled: false
targets:
  - name: Router
    host: 192.168.0.1
    tier: 1
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != ":9090" || cfg.Ping.IntervalMs != 500 {
		t.Fatalf("yaml not applied: %+v", cfg)
	}
	if cfg.Ping.TimeoutMs != 2000 {
		t.Fatal("unset yaml field should keep default")
	}
	if cfg.Speedtest.Engine != "cloudflare" || cfg.Speedtest.IsEnabled() {
		t.Fatalf("speedtest yaml not applied: %+v", cfg.Speedtest)
	}
	if len(cfg.Targets) != 1 || cfg.Targets[0].Host != "192.168.0.1" {
		t.Fatalf("targets: %+v", cfg.Targets)
	}
}

func TestEnvOverridesYAML(t *testing.T) {
	clearEnv(t)
	p := writeFile(t, `
listen: ":9090"
speedtest:
  engine: cloudflare
  interval_minutes: 10
targets:
  - name: Router
    host: 192.168.0.1
    tier: 1
`)
	t.Setenv("LISTEN", ":7070")
	t.Setenv("SPEEDTEST_ENGINE", "ookla")
	t.Setenv("TARGETS", "GW:10.0.0.1:1,CF:1.1.1.1:3")
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != ":7070" {
		t.Fatalf("env should beat yaml: %s", cfg.Listen)
	}
	if cfg.Speedtest.Engine != "ookla" {
		t.Fatalf("engine = %s", cfg.Speedtest.Engine)
	}
	if cfg.Speedtest.IntervalMinutes != 10 {
		t.Fatal("yaml interval should survive when env unset")
	}
	if len(cfg.Targets) != 2 || cfg.Targets[0].Host != "10.0.0.1" || cfg.Targets[1].Tier != 3 {
		t.Fatalf("env targets should replace yaml targets: %+v", cfg.Targets)
	}
}

func TestParseTargetsEnv(t *testing.T) {
	cases := []struct {
		in      string
		wantN   int
		wantErr bool
	}{
		{"GW:10.0.0.1:1,CF:1.1.1.1:3", 2, false},
		{"NoTier:1.0.0.1", 1, false},              // tier defaults to 3
		{" Spaced : 8.8.8.8 : 2 ", 1, false},      // whitespace tolerated
		{"GW:10.0.0.1:1,", 1, false},              // trailing comma ok
		{"", 0, true},
		{"bad", 0, true},
		{"a:b:c:d", 0, true},
		{"Name:host:9", 0, true},                  // bad tier
		{":1.1.1.1:1", 0, true},                   // empty name
	}
	for _, c := range cases {
		got, err := ParseTargetsEnv(c.in)
		if c.wantErr != (err != nil) {
			t.Fatalf("%q: err = %v", c.in, err)
		}
		if err == nil && len(got) != c.wantN {
			t.Fatalf("%q: n = %d, want %d", c.in, len(got), c.wantN)
		}
	}
	got, _ := ParseTargetsEnv("NoTier:1.0.0.1")
	if got[0].Tier != 3 {
		t.Fatalf("default tier = %d, want 3", got[0].Tier)
	}
}
