/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package caddy

import (
	"encoding/json"
	"fmt"

	"github.com/markdr-hue/HO/db/models"
)

// buildConfig generates a Caddy JSON configuration from active sites in
// the database. Each active site that has a domain gets a server block
// with a reverse_proxy handler pointing to localhost:{PublicPort}. Auto
// HTTPS is enabled via Let's Encrypt, and HTTP-to-HTTPS redirect is
// configured. An admin server block is also created for the admin panel
// on localhost:{AdminPort}.
func (m *CaddyManager) buildConfig() ([]byte, error) {
	sites, err := models.ListActiveSites(m.db)
	if err != nil {
		return nil, fmt.Errorf("listing active sites: %w", err)
	}

	// Build route entries for each site with a domain
	servers := make(map[string]interface{})

	// Collect site routes for public-facing server
	var siteRoutes []interface{}
	for _, site := range sites {
		if site.Domain == nil || *site.Domain == "" {
			m.logger.Debug("skipping site without domain", "site_id", site.ID, "name", site.Name)
			continue
		}

		domain := *site.Domain
		upstream := fmt.Sprintf("localhost:%d", m.config.PublicPort)

		route := map[string]interface{}{
			"match": []map[string]interface{}{
				{
					"host": []string{domain},
				},
			},
			"handle": []map[string]interface{}{
				{
					"handler": "reverse_proxy",
					"upstreams": []map[string]interface{}{
						{
							"dial": upstream,
						},
					},
				},
			},
		}

		siteRoutes = append(siteRoutes, route)
		m.logger.Debug("added site route", "domain", domain, "upstream", upstream, "site_id", site.ID)
	}

	// Public HTTPS server - handles all site domains
	if len(siteRoutes) > 0 {
		servers["sites"] = map[string]interface{}{
			"listen": []string{":443"},
			"routes": siteRoutes,
		}

		// HTTP-to-HTTPS redirect server
		servers["http_redirect"] = map[string]interface{}{
			"listen": []string{":80"},
			"routes": []map[string]interface{}{
				{
					"match": []map[string]interface{}{
						{
							"protocol": "http",
						},
					},
					"handle": []map[string]interface{}{
						{
							"handler": "static_response",
							"headers": map[string][]string{
								"Location": {"{http.request.scheme}s://{http.request.host}{http.request.uri}"},
							},
							"status_code": "301",
						},
					},
				},
			},
		}
	}

	// Admin panel server block - serves on localhost only
	adminUpstream := fmt.Sprintf("localhost:%d", m.config.AdminPort)
	servers["admin_panel"] = map[string]interface{}{
		"listen": []string{fmt.Sprintf(":%d", m.config.AdminPort+1000)},
		"routes": []map[string]interface{}{
			{
				"handle": []map[string]interface{}{
					{
						"handler": "reverse_proxy",
						"upstreams": []map[string]interface{}{
							{
								"dial": adminUpstream,
							},
						},
					},
				},
			},
		},
		"automatic_https": map[string]interface{}{
			"disable": true,
		},
	}

	// Full Caddy config
	caddyConfig := map[string]interface{}{
		"admin": map[string]interface{}{
			"disabled": true,
		},
		"apps": map[string]interface{}{
			"http": map[string]interface{}{
				"servers": servers,
			},
		},
	}

	cfgJSON, err := json.MarshalIndent(caddyConfig, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshaling caddy config: %w", err)
	}

	m.logger.Info("built Caddy config", "site_count", len(siteRoutes), "server_count", len(servers))
	return cfgJSON, nil
}
