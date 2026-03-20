/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package config

const (
	DefaultAdminPort      = 5001 // Admin panel HTTP server port
	DefaultPublicPort     = 5000 // Public-facing site HTTP server port
	DefaultDataDir        = "./data"
	DefaultLogLevel       = "error"
	DefaultSeedPath       = "./seed.json"
	DefaultRateLimitRate  = 100 // Requests per second per IP (public endpoints)
	DefaultRateLimitBurst = 200 // Burst capacity per IP (public endpoints)
	DefaultLLMTimeoutSec  = 480 // 8 minutes default for LLM HTTP calls (must exceed brain's 7-min context timeout)
)
