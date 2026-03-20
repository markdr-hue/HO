/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"time"

	"github.com/markdr-hue/HO/db/models"
	"github.com/markdr-hue/HO/events"
)

// SystemHandler handles system status and SSE event stream.
type SystemHandler struct {
	deps *Deps
}

type systemStatus struct {
	Version       string `json:"version"`
	GoVersion     string `json:"go_version"`
	GOOS          string `json:"goos"`
	GOARCH        string `json:"goarch"`
	NumGoroutines int    `json:"num_goroutines"`
	UserCount     int    `json:"user_count"`
	SiteCount     int    `json:"site_count"`
	ProviderCount int    `json:"provider_count"`
	BrainRunning  int    `json:"brain_running"`
	RunningSites  []int  `json:"running_sites"`
	Uptime        string `json:"uptime"`
	PublicPort    int    `json:"public_port"`
}

var startTime = time.Now()

// Status returns system status information for the admin dashboard.
func (h *SystemHandler) Status(w http.ResponseWriter, r *http.Request) {
	userCount, _ := models.CountUsers(h.deps.DB.DB)
	sites, _ := models.ListSites(h.deps.DB.DB)
	providerCount, _ := models.CountProviders(h.deps.DB.DB)

	status := systemStatus{
		Version:       h.deps.Version,
		GoVersion:     runtime.Version(),
		GOOS:          runtime.GOOS,
		GOARCH:        runtime.GOARCH,
		NumGoroutines: runtime.NumGoroutine(),
		UserCount:     userCount,
		SiteCount:     len(sites),
		ProviderCount: providerCount,
		BrainRunning:  h.deps.BrainManager.RunningCount(),
		RunningSites:  h.deps.BrainManager.RunningSites(),
		Uptime:        time.Since(startTime).Round(time.Second).String(),
		PublicPort:    h.deps.Config.PublicPort,
	}

	writeJSON(w, http.StatusOK, status)
}

// EventStream sends server-sent events for all system events.
func (h *SystemHandler) EventStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// Disable write deadline for this long-lived SSE connection.
	// Without this, Go's http.Server WriteTimeout kills the connection.
	rc := http.NewResponseController(w)
	rc.SetWriteDeadline(time.Time{})

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// Channel to receive events.
	eventCh := make(chan events.Event, 64)

	// Subscribe to all events; clean up on disconnect.
	subID := h.deps.Bus.SubscribeAll(func(e events.Event) {
		select {
		case eventCh <- e:
		default:
			// Drop event if channel full to avoid blocking.
		}
	})

	// Send keepalive comment every 30 seconds.
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			h.deps.Bus.Unsubscribe(subID)
			return
		case e := <-eventCh:
			data, err := json.Marshal(e)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Type, data)
			flusher.Flush()
		case <-ticker.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}
