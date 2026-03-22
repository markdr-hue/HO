/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package tools

// RegisterAll registers every tool with the given registry.
func RegisterAll(r *Registry) {
	// Pages (unified: save, get, list, delete, restore, history, search)
	r.Register(&PagesTool{})

	// Storage: unified assets + files (storage="assets"|"files")
	r.Register(&FilesTool{})

	// Dynamic tables (with secure columns: PASSWORD, ENCRYPTED)
	r.Register(&SchemaTool{})
	r.Register(&DataTool{})

	// Endpoints (unified: create_api, list_api, delete_api, create_auth, list_auth, delete_auth, verify_password)
	r.Register(&EndpointsTool{})

	// Communication (unified: ask, check)
	r.Register(&CommunicationTool{})

	// Analytics (unified: query, summary)
	r.Register(&AnalyticsTool{})

	// HTTP
	r.Register(&MakeHTTPRequestTool{})

	// Webhooks (unified: create, get, list, delete, update, subscribe)
	r.Register(&WebhooksTool{})

	// Service Providers (unified: add, list, remove, update, request)
	r.Register(&ProvidersTool{})

	// Secrets (unified: store, list, delete)
	r.Register(&SecretsTool{})

	// Site (unified: info, set_mode)
	r.Register(&SiteTool{})

	// Scheduler (unified: create, list, update, delete)
	r.Register(&SchedulerTool{})

	// Layout (unified: save, get, list)
	r.Register(&LayoutTool{})

	// Diagnostics (unified: health, errors, integrity)
	r.Register(&DiagnosticsTool{})

	// Email (unified: configure, send, save_template, list_templates)
	r.Register(&EmailTool{})

	// Payments (unified: configure, create_checkout, check_status, list, handle_webhook)
	r.Register(&PaymentsTool{})

	// Search (FTS5: create_index, search, drop_index, list_indexes)
	r.Register(&SearchTool{})

	// SEO (set_meta, get_meta, generate_sitemap, set_robots, validate)
	r.Register(&SEOTool{})

	// Media (resize, thumbnail, optimize)
	r.Register(&MediaTool{})

	// Blobs (register, list, get, delete)
	r.Register(&BlobsTool{})

	// Background Jobs (enqueue, status, list, cancel)
	r.Register(&JobsTool{})

	// Server-Side Actions (create, list, update, delete, test)
	r.Register(&ActionsTool{})

	// Memory (store, recall, list, forget)
	r.Register(&MemoryTool{})

	// Testing (test_endpoint, test_auth_flow, test_page)
	r.Register(&TestingTool{})

	// Reusable HTML Components (save, get, list, delete)
	r.Register(&ComponentsTool{})

	// Plan Amendments (add_table, add_endpoint, get_plan)
	r.Register(&PlanAmendTool{})
}
