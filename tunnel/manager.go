/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package tunnel

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// ManagerConfig holds global tunnel settings.
type ManagerConfig struct {
	BrokerURL string
	Token     string
	LocalPort int // port of the HO public server
}

// Status describes the state of a tunnel for a given site.
type Status struct {
	Active    bool   `json:"active"`
	Subdomain string `json:"subdomain,omitempty"`
	URL       string `json:"url,omitempty"`
	Since     string `json:"since,omitempty"` // ISO 8601
}

type tunnelEntry struct {
	client    *Client
	startedAt time.Time
}

// Manager tracks active tunnel connections per site.
type Manager struct {
	mu      sync.Mutex
	tunnels map[string]*tunnelEntry // siteID → entry
	cfg     ManagerConfig
}

// NewManager creates a tunnel manager.
func NewManager(cfg ManagerConfig) *Manager {
	return &Manager{
		tunnels: make(map[string]*tunnelEntry),
		cfg:     cfg,
	}
}

// Start opens a tunnel for the given site and subdomain. Returns the public URL.
func (m *Manager) Start(siteID, subdomain string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Stop existing tunnel for this site if any.
	if entry, ok := m.tunnels[siteID]; ok {
		entry.client.Close()
		delete(m.tunnels, siteID)
	}

	if m.cfg.BrokerURL == "" {
		return "", fmt.Errorf("tunnel broker URL not configured")
	}

	cfg := Config{
		BrokerURL: m.cfg.BrokerURL,
		Token:     m.cfg.Token,
		Subdomain: subdomain,
		LocalAddr: fmt.Sprintf("127.0.0.1:%d", m.cfg.LocalPort),
		SiteID:    siteID,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	client, err := Connect(ctx, cfg)
	if err != nil {
		return "", err
	}

	m.tunnels[siteID] = &tunnelEntry{client: client, startedAt: time.Now()}

	// Monitor for disconnects and auto-cleanup.
	go func() {
		<-client.Done()
		m.mu.Lock()
		if entry, ok := m.tunnels[siteID]; ok && entry.client == client {
			delete(m.tunnels, siteID)
		}
		m.mu.Unlock()
		slog.Info("tunnel auto-cleaned", "site_id", siteID, "subdomain", subdomain)
	}()

	return client.URL(), nil
}

// Stop disconnects the tunnel for the given site.
func (m *Manager) Stop(siteID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	entry, ok := m.tunnels[siteID]
	if !ok {
		return nil
	}

	err := entry.client.Close()
	delete(m.tunnels, siteID)
	return err
}

// GetStatus returns the current tunnel status for a site.
func (m *Manager) GetStatus(siteID string) Status {
	m.mu.Lock()
	defer m.mu.Unlock()

	entry, ok := m.tunnels[siteID]
	if !ok {
		return Status{Active: false}
	}

	// Check if still alive.
	select {
	case <-entry.client.Done():
		delete(m.tunnels, siteID)
		return Status{Active: false}
	default:
	}

	return Status{
		Active:    true,
		Subdomain: entry.client.Subdomain(),
		URL:       entry.client.URL(),
		Since:     entry.startedAt.UTC().Format(time.RFC3339),
	}
}

// StopAll disconnects all active tunnels. Called on app shutdown.
func (m *Manager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for siteID, entry := range m.tunnels {
		entry.client.Close()
		delete(m.tunnels, siteID)
	}
	slog.Info("all tunnels stopped")
}
