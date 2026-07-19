// Package settings provides runtime-editable app settings persisted in the
// settings table, falling back to boot config (env/YAML/defaults). This is
// the "UI edits > env > yaml > defaults" precedence: at boot, env/yaml
// values are seeded; UI edits overwrite the DB copy and win thereafter
// unless CONFIG_LOCK is set.
package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"pingway.net/pingway/internal/config"
	"pingway.net/pingway/internal/store"
)

const dbKey = "app_settings"

// App is the runtime-editable subset of configuration.
type App struct {
	SpeedtestEngine          string `json:"speedtest_engine"`
	SpeedtestIntervalMinutes int    `json:"speedtest_interval_minutes"`
	SpeedtestEnabled         bool   `json:"speedtest_enabled"`
	OoklaAcceptEULA          bool   `json:"ookla_accept_eula"`
	RetentionRawHours        int    `json:"retention_raw_hours"`
	RetentionRollup1mDays    int    `json:"retention_rollup_1m_days"`
	ConfigLock               bool   `json:"config_lock"` // read-only via API
	UIEdited                 bool   `json:"ui_edited,omitempty"`
}

// Manager serializes access to the persisted settings.
type Manager struct {
	store *store.Store
	mu    sync.RWMutex
	cur   App
	lock  bool
}

// NewManager seeds settings from boot config. If CONFIG_LOCK is set, the
// boot config always wins and PUTs are rejected; otherwise a previously
// persisted UI edit takes precedence over env/yaml. A persisted copy that
// was never edited through the UI does NOT win — env/yaml changes apply
// on the next restart.
func NewManager(ctx context.Context, s *store.Store, cfg *config.Config) (*Manager, error) {
	boot := App{
		SpeedtestEngine:          cfg.Speedtest.Engine,
		SpeedtestIntervalMinutes: cfg.Speedtest.IntervalMinutes,
		SpeedtestEnabled:         cfg.Speedtest.IsEnabled(),
		OoklaAcceptEULA:          cfg.Speedtest.OoklaAcceptEULA,
		RetentionRawHours:        cfg.Retention.RawHours,
		RetentionRollup1mDays:    cfg.Retention.Rollup1mDays,
		ConfigLock:               cfg.ConfigLock,
	}
	m := &Manager{store: s, cur: boot, lock: cfg.ConfigLock}

	if !cfg.ConfigLock {
		if raw, ok, err := s.GetSetting(ctx, dbKey); err != nil {
			return nil, err
		} else if ok {
			var saved App
			if err := json.Unmarshal([]byte(raw), &saved); err == nil && saved.UIEdited {
				saved.ConfigLock = false
				m.cur = saved
			}
		}
	}
	if err := m.persist(ctx); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) Get() App {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cur
}

// Update validates, persists, and applies new settings. Returns an error
// if CONFIG_LOCK is active.
func (m *Manager) Update(ctx context.Context, a App) error {
	if m.lock {
		return fmt.Errorf("CONFIG_LOCK is set; settings are managed by env/yaml")
	}
	if err := validate(&a); err != nil {
		return err
	}
	m.mu.Lock()
	a.ConfigLock = false
	a.UIEdited = true
	m.cur = a
	m.mu.Unlock()
	return m.persist(ctx)
}

func (m *Manager) persist(ctx context.Context) error {
	m.mu.RLock()
	b, err := json.Marshal(m.cur)
	m.mu.RUnlock()
	if err != nil {
		return err
	}
	return m.store.SetSetting(ctx, dbKey, string(b))
}

func validate(a *App) error {
	switch a.SpeedtestEngine {
	case "librespeed", "cloudflare", "ookla":
	default:
		return fmt.Errorf("speedtest_engine must be librespeed, cloudflare, or ookla")
	}
	if a.SpeedtestIntervalMinutes < 5 || a.SpeedtestIntervalMinutes > 24*60 {
		return fmt.Errorf("speedtest_interval_minutes must be between 5 and 1440")
	}
	if a.RetentionRawHours < 2 || a.RetentionRawHours > 24*30 {
		return fmt.Errorf("retention_raw_hours must be between 2 and 720")
	}
	if a.RetentionRollup1mDays < 1 || a.RetentionRollup1mDays > 3650 {
		return fmt.Errorf("retention_rollup_1m_days must be between 1 and 3650")
	}
	return nil
}
