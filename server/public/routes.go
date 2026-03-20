/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package public

import (
	"log/slog"

	"github.com/go-chi/chi/v5"
	"github.com/markdr-hue/HO/db"
	"github.com/markdr-hue/HO/events"
	"github.com/markdr-hue/HO/llm"
	"github.com/markdr-hue/HO/security"
)

// Deps holds all public handler dependencies.
type Deps struct {
	DB            *db.DB
	SiteDBManager *db.SiteDBManager
	Bus           *events.Bus
	WSHub         *wsHub
	LLMRegistry   *llm.Registry
	Encryptor     *security.Encryptor
	JWTManager    *security.JWTManager
	Logger        *slog.Logger
}

// RegisterRoutes mounts all public site routes on the given router.
func RegisterRoutes(r chi.Router, deps *Deps) {
	h := &Handler{deps: deps}

	// All public routes go through SiteResolver which looks up the
	// site from the Host header and injects it into context.
	r.Use(h.SiteResolver)

	// SEO essentials.
	r.Get("/sitemap.xml", h.Sitemap)
	r.Get("/robots.txt", h.Robots)

	// PWA assets (served from DB content, not disk).
	r.Get("/manifest.json", h.ServeManifest)
	r.Get("/service-worker.js", h.ServeServiceWorker)

	// Asset serving.
	r.Get("/assets/*", h.ServeAsset)

	// File serving (user uploads).
	r.Get("/files/*", h.ServeFile)

	// Page JSON API (SPA router navigation).
	r.Get("/api/page", h.Page)

	// Incoming webhooks.
	r.Post("/webhooks/{name}", h.IncomingWebhook)

	// Dynamic API endpoints (LLM-created) — includes auth endpoints.
	r.HandleFunc("/api/*", h.DynamicAPI)

	// Catch-all: serve SSR pages by path.
	r.Get("/*", h.ServePage)
}
