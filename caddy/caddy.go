/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package caddy

import (
	"database/sql"
	"fmt"
	"log/slog"
	"sync"

	caddycmd "github.com/caddyserver/caddy/v2"
	_ "github.com/caddyserver/caddy/v2/modules/standard"

	"github.com/markdr-hue/HO/config"
	"github.com/markdr-hue/HO/events"
)

// CaddyManager manages the embedded Caddy server lifecycle.
// It reads active sites from the database, builds a Caddy JSON config,
// and loads/reloads Caddy as sites are created, updated, or deleted.
type CaddyManager struct {
	config  *config.Config
	db      *sql.DB
	logger  *slog.Logger
	running bool
	mu      sync.Mutex
}

// NewManager creates a new CaddyManager instance.
func NewManager(cfg *config.Config, db *sql.DB, logger *slog.Logger) *CaddyManager {
	return &CaddyManager{
		config: cfg,
		db:     db,
		logger: logger.With("component", "caddy"),
	}
}

// Start builds the Caddy JSON config from active sites and loads it
// into the embedded Caddy server. If CaddyEnabled is false in the
// config, Start is a no-op and returns nil.
func (m *CaddyManager) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.config.CaddyEnabled {
		m.logger.Info("Caddy integration disabled, skipping start")
		return nil
	}

	cfgJSON, err := m.buildConfig()
	if err != nil {
		return fmt.Errorf("building caddy config: %w", err)
	}

	m.logger.Info("starting Caddy with generated config")
	m.logger.Debug("caddy config", "json", string(cfgJSON))

	if err := caddycmd.Load(cfgJSON, true); err != nil {
		return fmt.Errorf("loading caddy config: %w", err)
	}

	m.running = true
	m.logger.Info("Caddy started successfully")
	return nil
}

// Stop shuts down the embedded Caddy server. If Caddy is not running,
// Stop is a no-op.
func (m *CaddyManager) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.running {
		m.logger.Debug("Caddy not running, nothing to stop")
		return nil
	}

	m.logger.Info("stopping Caddy")
	if err := caddycmd.Stop(); err != nil {
		return fmt.Errorf("stopping caddy: %w", err)
	}

	m.running = false
	m.logger.Info("Caddy stopped successfully")
	return nil
}

// Reload rebuilds the Caddy config from active sites and reloads it.
// If CaddyEnabled is false or Caddy is not running, Reload is a no-op.
func (m *CaddyManager) Reload() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.config.CaddyEnabled {
		return nil
	}

	if !m.running {
		m.logger.Debug("Caddy not running, skipping reload")
		return nil
	}

	cfgJSON, err := m.buildConfig()
	if err != nil {
		return fmt.Errorf("building caddy config for reload: %w", err)
	}

	m.logger.Info("reloading Caddy configuration")
	m.logger.Debug("caddy reload config", "json", string(cfgJSON))

	if err := caddycmd.Load(cfgJSON, true); err != nil {
		return fmt.Errorf("reloading caddy config: %w", err)
	}

	m.logger.Info("Caddy reloaded successfully")
	return nil
}

// SubscribeToEvents registers event handlers on the given event bus so
// that Caddy automatically reloads whenever sites are created, updated,
// or deleted.
func (m *CaddyManager) SubscribeToEvents(bus *events.Bus) {
	reloadHandler := func(e events.Event) {
		m.logger.Info("site change detected, reloading Caddy", "event", e.Type, "site_id", e.SiteID)
		if err := m.Reload(); err != nil {
			m.logger.Error("failed to reload Caddy after site change", "event", e.Type, "site_id", e.SiteID, "error", err)
		}
	}

	bus.Subscribe(events.EventSiteCreated, reloadHandler)
	bus.Subscribe(events.EventSiteUpdated, reloadHandler)
	bus.Subscribe(events.EventSiteDeleted, reloadHandler)

	m.logger.Info("subscribed to site events for auto-reload")
}
