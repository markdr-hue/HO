/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package admin

import (
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/markdr-hue/HO/brain"
	"github.com/markdr-hue/HO/chat"
	"github.com/markdr-hue/HO/config"
	"github.com/markdr-hue/HO/db"
	"github.com/markdr-hue/HO/events"
	"github.com/markdr-hue/HO/llm"
	"github.com/markdr-hue/HO/security"
	"github.com/markdr-hue/HO/server/middleware"
	"github.com/markdr-hue/HO/tools"
)

// Deps holds all admin handler dependencies.
type Deps struct {
	Config          *config.Config
	DB              *db.DB
	SiteDBManager   *db.SiteDBManager
	JWTManager      *security.JWTManager
	Encryptor       *security.Encryptor
	Bus             *events.Bus
	BrainManager    *brain.BrainManager
	ChatHandler     *chat.ChatHandler
	LLMRegistry     *llm.Registry
	ToolRegistry    *tools.Registry
	ProviderFactory llm.ProviderFactory
	Logger          *slog.Logger
	Version         string
}

// RegisterRoutes mounts all admin API routes on the given router.
func RegisterRoutes(r chi.Router, deps *Deps, adminFS fs.FS) {
	auth := &AuthHandler{deps: deps}
	sites := &SitesHandler{deps: deps}
	brainH := &BrainHandler{deps: deps}
	chatH := &ChatHandlerAdmin{deps: deps}
	providers := &ProvidersHandler{deps: deps}
	svcProviders := &ServiceProvidersHandler{deps: deps}
	toolsH := &ToolsHandler{deps: deps}
	users := &UsersHandler{deps: deps}
	system := &SystemHandler{deps: deps}
	questions := &QuestionsHandler{deps: deps}
	settings := &SettingsHandler{deps: deps}
	logs := &LogsHandler{deps: deps}
	sitePagesH := &SitePagesHandler{deps: deps}
	siteAssetsH := &SiteAssetsHandler{deps: deps}
	siteTablesH := &SiteTablesHandler{deps: deps}
	siteTasksH := &SiteTasksHandler{deps: deps}
	siteEndpointsH := &SiteEndpointsHandler{deps: deps}
	siteFilesH := &SiteFilesHandler{deps: deps}
	siteWebhooksH := &SiteWebhooksHandler{deps: deps}
	siteAuthEndpointsH := &SiteAuthEndpointsHandler{deps: deps}
	siteSecretsH := &SiteSecretsHandler{deps: deps}

	templatesH := &TemplatesHandler{deps: deps}
	siteLayoutsH := &SiteLayoutsHandler{deps: deps}
	siteAnalyticsH := &SiteAnalyticsHandler{deps: deps}
	siteDiagH := &SiteDiagnosticsHandler{deps: deps}
	authLimiter := middleware.NewRateLimiter(5, 10)
	r.Route("/admin/api", func(r chi.Router) {
		// Public endpoints (no JWT required).
		r.Get("/setup/check", auth.HandleSetupCheck)
		r.Get("/setup/providers", auth.HandleProviderCatalog)
		r.Group(func(r chi.Router) {
			r.Use(authLimiter.Handler)
			r.Post("/auth/login", auth.HandleLogin)
			r.Post("/auth/logout", auth.HandleLogout)
			r.Post("/setup", auth.HandleSetup)
		})

		// Protected endpoints (JWT required).
		r.Group(func(r chi.Router) {
			r.Use(deps.JWTManager.Authenticator)

			// Read-only endpoints available to all authenticated users.
			r.Get("/sites", sites.List)
			r.Get("/providers/catalog", providers.Catalog)
			r.Get("/providers", providers.List)
			r.Get("/providers/{providerID}", providers.Get)
			r.Get("/providers/{providerID}/models", providers.Models)
			r.Get("/tools", toolsH.List)
			r.Get("/users", users.List)
			r.Get("/system/status", system.Status)
			r.Get("/events/stream", system.EventStream)
			r.Get("/questions", questions.List)
			r.Get("/settings", settings.Get)
			r.Get("/templates", templatesH.List)
			r.Get("/templates/{templateID}", templatesH.Get)
			r.Get("/logs/{siteID}", logs.List)
			r.Get("/logs/{siteID}/llm", logs.ListLLM)
			r.Get("/logs/{siteID}/llm/csv", logs.ExportCSV)
			r.Get("/logs/{siteID}/llm/stats", logs.Stats)
			r.Get("/logs/{siteID}/llm/{logID}", logs.GetLLM)

			// Site endpoints — single Route block with read + admin groups.
			r.Route("/sites/{siteID}", func(r chi.Router) {
				// Read-only — all authenticated users
				r.Get("/", sites.Get)
				r.Get("/summary", sites.Summary)
				r.Get("/pages", sitePagesH.List)
				r.Get("/pages/{pageID}", sitePagesH.Get)
				r.Get("/assets", siteAssetsH.List)
				r.Get("/assets/{assetID}/content", siteAssetsH.Content)
				r.Get("/tables", siteTablesH.List)
				r.Get("/tables/{tableName}/rows", siteTablesH.Rows)
				r.Get("/endpoints", siteEndpointsH.List)
				r.Get("/endpoints/{endpointID}", siteEndpointsH.Get)
				r.Get("/files", siteFilesH.List)
				r.Get("/tasks", siteTasksH.List)
				r.Get("/webhooks", siteWebhooksH.List)
				r.Get("/webhooks/{webhookID}", siteWebhooksH.Get)
				r.Get("/tasks/{taskID}/runs", siteTasksH.TaskRuns)
				r.Get("/questions", questions.ListBySite)
				r.Get("/service-providers", svcProviders.List)
				r.Get("/auth-endpoints", siteAuthEndpointsH.List)
				r.Get("/secrets", siteSecretsH.List)

				r.Get("/layouts", siteLayoutsH.List)
				r.Get("/layouts/{layoutID}", siteLayoutsH.Get)
				r.Get("/analytics/summary", siteAnalyticsH.Summary)
				r.Get("/diagnostics/health", siteDiagH.Health)
				r.Get("/diagnostics/integrity", siteDiagH.Integrity)
				r.Get("/diagnostics/errors", siteDiagH.Errors)

				// Admin-only — destructive site actions
				r.Group(func(r chi.Router) {
					r.Use(security.RequireRole("admin"))
					r.Put("/", sites.Update)
					r.Delete("/", sites.Delete)
					r.Post("/toggle-status", sites.ToggleStatus)
					r.Post("/assets", siteAssetsH.Create)
					r.Delete("/assets/{assetID}", siteAssetsH.Delete)
					r.Post("/tables/{tableName}/rows", siteTablesH.InsertRow)
					r.Put("/tables/{tableName}/rows/{rowID}", siteTablesH.UpdateRow)
					r.Delete("/tables/{tableName}/rows/{rowID}", siteTablesH.DeleteRow)
					r.Put("/endpoints/{endpointID}", siteEndpointsH.Update)
					r.Delete("/endpoints/{endpointID}", siteEndpointsH.Delete)
					r.Post("/files", siteFilesH.Create)
					r.Delete("/files/{fileID}", siteFilesH.Delete)
					r.Post("/tasks/{taskID}/toggle", siteTasksH.Toggle)
					r.Delete("/tasks/{taskID}", siteTasksH.Delete)
					r.Post("/tasks", siteTasksH.Create)
					r.Put("/tasks/{taskID}", siteTasksH.Update)
					r.Post("/webhooks/{webhookID}/toggle", siteWebhooksH.Toggle)
					r.Delete("/webhooks/{webhookID}", siteWebhooksH.Delete)
					r.Put("/pages/{pageID}", sitePagesH.Update)
					r.Delete("/pages/{pageID}", sitePagesH.Delete)
					r.Delete("/auth-endpoints/{endpointID}", siteAuthEndpointsH.Delete)
					r.Delete("/secrets/{secretID}", siteSecretsH.Delete)

					r.Delete("/layouts/{layoutID}", siteLayoutsH.Delete)
					r.Post("/service-providers/{provID}/toggle", svcProviders.Toggle)
					r.Delete("/service-providers/{provID}", svcProviders.Delete)
				})
			})

			r.Get("/brain/{siteID}/status", brainH.Status)
			r.Get("/chat/{siteID}/history", chatH.History)

			// Chat streaming — all authenticated users can chat.
			r.Post("/chat/{siteID}/stream", chatH.Stream)

			// Question answering — all authenticated users can answer.
			r.Post("/questions/{questionID}/answer", questions.Answer)

			// Admin-only endpoints (destructive or configuration actions).
			r.Group(func(r chi.Router) {
				r.Use(security.RequireRole("admin"))

				// Sites (create, clone from template)
				r.Post("/sites", sites.Create)
				r.Post("/sites/clone", templatesH.Clone)

				// Templates (save, delete)
				r.Post("/templates", templatesH.Save)
				r.Delete("/templates/{templateID}", templatesH.Delete)

				// Brain control
				r.Post("/brain/{siteID}/start", brainH.Start)
				r.Post("/brain/{siteID}/stop", brainH.Stop)
				r.Post("/brain/{siteID}/mode", brainH.Mode)

				// Providers (create, update, delete, test)
				r.Post("/providers", providers.Create)
				r.Put("/providers/{providerID}", providers.Update)
				r.Delete("/providers/{providerID}", providers.Delete)
				r.Post("/providers/{providerID}/test", providers.Test)
				r.Post("/providers/{providerID}/models", providers.CreateModel)
				r.Delete("/providers/{providerID}/models/{modelID}", providers.DeleteModel)
				r.Post("/providers/{providerID}/models/{modelID}/default", providers.SetDefaultModel)

				// Users (create, update, delete)
				r.Post("/users", users.Create)
				r.Put("/users/{userID}", users.Update)
				r.Delete("/users/{userID}", users.Delete)

				// Settings (update)
				r.Put("/settings", settings.Update)
			})
		})
	})

	// Serve the embedded admin SPA for all non-API routes.
	if adminFS != nil {
		fileServer := http.FileServer(http.FS(adminFS))
		r.Get("/*", func(w http.ResponseWriter, r *http.Request) {
			// Try to serve the requested file. If it doesn't exist,
			// fall back to index.html for SPA routing.
			path := r.URL.Path
			if path == "/" {
				path = "/index.html"
			}

			// Check if the file exists in the embedded FS.
			if _, err := fs.Stat(adminFS, path[1:]); err != nil {
				// File not found -- serve index.html for SPA routing.
				indexBytes, readErr := fs.ReadFile(adminFS, "index.html")
				if readErr != nil {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Write(indexBytes)
				return
			}

			fileServer.ServeHTTP(w, r)
		})
	}
}
