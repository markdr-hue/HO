/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package caddy

import (
	"io"
	"log"
	"log/slog"
	"strings"
)

// slogWriter adapts slog to the io.Writer interface so we can redirect
// Caddy's standard library log output through our structured logger.
// This filters out noisy internal messages and surfaces certificate errors
// at a level the user can understand.
type slogWriter struct {
	logger *slog.Logger
}

func (w *slogWriter) Write(p []byte) (n int, err error) {
	msg := strings.TrimSpace(string(p))
	if msg == "" {
		return len(p), nil
	}

	lower := strings.ToLower(msg)

	// Surface certificate/ACME errors prominently.
	if containsAny(lower, "certificate", "acme", "tls", "ssl") {
		if containsAny(lower, "error", "fail", "unable", "timeout", "denied", "refused") {
			w.logger.Error("HTTPS certificate problem: "+summarizeCertError(msg),
				"raw", msg,
			)
			return len(p), nil
		}
		// Certificate info (renewals, obtainment) at info level.
		if containsAny(lower, "obtain", "renew", "success", "served") {
			w.logger.Info(msg)
			return len(p), nil
		}
	}

	// Surface clear network/bind errors.
	if containsAny(lower, "bind", "address already in use", "permission denied", "listen") {
		if containsAny(lower, "error", "fail", ":80", ":443") {
			w.logger.Error("Caddy port problem: "+summarizePortError(msg),
				"raw", msg,
			)
			return len(p), nil
		}
	}

	// Suppress Caddy's verbose startup/module loading noise.
	if containsAny(lower, "provisioning", "loading", "adapting", "cleaned up",
		"shutting down", "autosaved", "storage", "starting") {
		w.logger.Debug(msg)
		return len(p), nil
	}

	// Default: pass through at debug level to keep console clean.
	w.logger.Debug(msg)
	return len(p), nil
}

// RedirectCaddyLogs sends Caddy's standard library log output through
// our slog logger, filtering noise and surfacing errors clearly.
func RedirectCaddyLogs(logger *slog.Logger) {
	writer := &slogWriter{logger: logger.With("component", "caddy_internal")}
	log.SetOutput(io.Writer(writer))
	log.SetFlags(0) // slog handles timestamps
}

// summarizeCertError turns a raw Caddy certificate error into a
// user-friendly message.
func summarizeCertError(raw string) string {
	lower := strings.ToLower(raw)

	switch {
	case strings.Contains(lower, "dns"):
		return "DNS verification failed. Make sure your domain's A record points to this server's public IP address"
	case strings.Contains(lower, "timeout") || strings.Contains(lower, "timed out"):
		return "certificate request timed out. Let's Encrypt could not reach this server. Check that ports 80 and 443 are open and not blocked by a firewall"
	case strings.Contains(lower, "connection refused"):
		return "Let's Encrypt could not connect to this server. Make sure ports 80 and 443 are open"
	case strings.Contains(lower, "too many"):
		return "rate limit reached. Let's Encrypt limits how many certificates you can request per domain per week. Wait an hour and try again"
	case strings.Contains(lower, "unauthorized") || strings.Contains(lower, "403"):
		return "domain verification failed. Let's Encrypt could not confirm you control this domain. Check your DNS settings"
	case strings.Contains(lower, "invalid domain") || strings.Contains(lower, "not a domain"):
		return "the domain name is not valid for a certificate. Make sure it's a real, publicly reachable domain"
	default:
		return "a certificate error occurred (see raw message in debug logs)"
	}
}

// summarizePortError turns a raw port bind error into a user-friendly message.
func summarizePortError(raw string) string {
	lower := strings.ToLower(raw)

	switch {
	case strings.Contains(lower, ":80") && strings.Contains(lower, "address already in use"):
		return "port 80 is already in use by another program. Stop the other program or configure it to not use port 80. Common culprits: Apache, nginx, another Caddy instance"
	case strings.Contains(lower, ":443") && strings.Contains(lower, "address already in use"):
		return "port 443 is already in use by another program. Stop the other program first"
	case strings.Contains(lower, "permission denied"):
		return "insufficient permissions to bind to port 80/443. On Linux, run with sudo or use setcap: sudo setcap cap_net_bind_service=+ep ./ho"
	default:
		return "could not bind to a required port (see raw message in debug logs)"
	}
}

func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
