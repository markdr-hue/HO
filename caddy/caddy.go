/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package caddy

import (
	"crypto/tls"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	caddycmd "github.com/caddyserver/caddy/v2"
	_ "github.com/caddyserver/caddy/v2/modules/standard"

	"github.com/markdr-hue/HO/config"
	"github.com/markdr-hue/HO/db/models"
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

	// Redirect Caddy's internal logs through our structured logger.
	RedirectCaddyLogs(m.logger)

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

	// Run HTTPS health check in background after a delay to give Caddy
	// time to obtain certificates.
	go m.checkHTTPSHealth(10 * time.Second)

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

	// Check HTTPS health after reload.
	go m.checkHTTPSHealth(15 * time.Second)

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

// checkHTTPSHealth waits for the given delay, then tests HTTPS connectivity
// for every active site with a domain. Results are logged as clear,
// actionable messages.
func (m *CaddyManager) checkHTTPSHealth(delay time.Duration) {
	time.Sleep(delay)

	sites, err := models.ListActiveSites(m.db)
	if err != nil {
		return
	}

	// HTTP client that accepts any cert (we just want to know if TLS handshake works).
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // don't follow redirects
		},
	}

	for _, site := range sites {
		if site.Domain == nil || *site.Domain == "" {
			continue
		}
		domain := *site.Domain

		url := fmt.Sprintf("https://%s/", domain)
		resp, err := client.Get(url)
		if err != nil {
			m.logger.Warn("HTTPS is not working for this domain",
				"domain", domain,
				"site_id", site.ID,
				"help", diagnoseHTTPSError(err, domain),
			)
			continue
		}
		resp.Body.Close()

		m.logger.Info("HTTPS is working",
			"domain", domain,
			"site_id", site.ID,
			"status", resp.StatusCode,
		)
	}
}

// diagnoseHTTPSError returns a plain-English explanation of why HTTPS failed.
func diagnoseHTTPSError(err error, domain string) string {
	msg := err.Error()

	switch {
	case containsAny(msg, "no such host", "server mismatch"):
		return fmt.Sprintf("DNS is not pointing '%s' to this server. Update your DNS A record to point to this server's public IP address", domain)
	case containsAny(msg, "connection refused"):
		return fmt.Sprintf("port 443 is not reachable on '%s'. Make sure the port is open in your firewall and no other program is using it", domain)
	case containsAny(msg, "timeout", "deadline exceeded"):
		return fmt.Sprintf("connection to '%s' timed out. The domain may not point to this server, or a firewall is blocking port 443", domain)
	case containsAny(msg, "certificate"):
		return fmt.Sprintf("TLS certificate problem for '%s'. Caddy may still be obtaining the certificate from Let's Encrypt (this can take a few minutes). If this persists, check that ports 80 and 443 are open", domain)
	default:
		return fmt.Sprintf("could not connect to '%s' via HTTPS: %v", domain, err)
	}
}
