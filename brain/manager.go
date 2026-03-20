/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package brain

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/markdr-hue/HO/db/models"
)

const defaultMaxConcurrentTicks = 4

// workerHandle pairs a running pipeline worker and job worker with their cancel functions.
type workerHandle struct {
	worker    *PipelineWorker
	cancel    context.CancelFunc
	jobWorker *JobWorker
	jobCancel context.CancelFunc
}

// BrainManager manages pipeline workers for all active sites.
type BrainManager struct {
	mu        sync.RWMutex
	workers   map[int]*workerHandle
	wg        sync.WaitGroup
	deps      *Deps
	semaphore chan struct{}
	logger    *slog.Logger
	rootCtx   context.Context
	scheduler *Scheduler
}

// NewBrainManager creates a manager that can start and stop pipeline workers.
func NewBrainManager(deps *Deps, ctx context.Context) *BrainManager {
	return &BrainManager{
		workers:   make(map[int]*workerHandle),
		deps:      deps,
		semaphore: make(chan struct{}, defaultMaxConcurrentTicks),
		logger:    slog.With("component", "brain_manager"),
		rootCtx:   ctx,
	}
}

// StartSite starts a pipeline worker for the given site.
func (m *BrainManager) StartSite(siteID int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.workers[siteID]; exists {
		m.logger.Debug("pipeline worker already running", "site_id", siteID)
		return nil
	}

	ctx, cancel := context.WithCancel(m.rootCtx)
	worker, err := NewPipelineWorker(siteID, m.deps, m.semaphore)
	if err != nil {
		cancel()
		return fmt.Errorf("brain: failed to create pipeline worker for site %d: %w", siteID, err)
	}

	// Start the background job worker alongside the pipeline worker.
	jobCtx, jobCancel := context.WithCancel(m.rootCtx)
	var jw *JobWorker
	if siteDB, err := m.deps.SiteDBManager.Open(siteID); err == nil {
		jw = NewJobWorker(siteID, siteDB, m.deps)
	}

	m.workers[siteID] = &workerHandle{
		worker:    worker,
		cancel:    cancel,
		jobWorker: jw,
		jobCancel: jobCancel,
	}

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		worker.Run(ctx)
	}()

	if jw != nil {
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			jw.Run(jobCtx)
		}()
	}

	m.logger.Info("pipeline worker started for site", "site_id", siteID)
	return nil
}

// StopSite stops the pipeline worker for the given site.
func (m *BrainManager) StopSite(siteID int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	handle, exists := m.workers[siteID]
	if !exists {
		return fmt.Errorf("brain: no worker running for site %d", siteID)
	}

	handle.cancel()
	if handle.jobCancel != nil {
		handle.jobCancel()
	}
	delete(m.workers, siteID)

	m.logger.Info("pipeline worker stopped for site", "site_id", siteID)
	return nil
}

// StopAll stops all running pipeline workers and waits for them to finish.
func (m *BrainManager) StopAll() {
	m.mu.Lock()
	for siteID, handle := range m.workers {
		handle.cancel()
		if handle.jobCancel != nil {
			handle.jobCancel()
		}
		m.logger.Info("pipeline worker stopping", "site_id", siteID)
	}
	m.workers = make(map[int]*workerHandle)
	m.mu.Unlock()

	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		m.logger.Info("all pipeline workers stopped")
	case <-time.After(30 * time.Second):
		m.logger.Warn("pipeline workers did not stop within 30s")
	}
}

// Status returns the current state of the pipeline worker for a site.
func (m *BrainManager) Status(siteID int) BrainState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	handle, exists := m.workers[siteID]
	if !exists {
		return StateIdle
	}
	return handle.worker.State()
}

// SendCommand sends a command to the pipeline worker for a specific site.
func (m *BrainManager) SendCommand(siteID int, cmd BrainCommand) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	handle, exists := m.workers[siteID]
	if !exists {
		return fmt.Errorf("brain: no worker running for site %d", siteID)
	}

	return handle.worker.Send(cmd)
}

// AutoStart reads all active sites from the database and starts a pipeline
// worker for each one.
func (m *BrainManager) AutoStart() error {
	sites, err := models.ListActiveSites(m.deps.DB.DB)
	if err != nil {
		return fmt.Errorf("brain: failed to list active sites: %w", err)
	}

	m.logger.Info("auto-starting pipeline workers", "active_sites", len(sites))

	for _, site := range sites {
		if err := m.StartSite(site.ID); err != nil {
			m.logger.Error("failed to start pipeline worker", "site_id", site.ID, "error", err)
		}
	}

	return nil
}

// StartScheduler starts the background task scheduler.
func (m *BrainManager) StartScheduler() {
	m.scheduler = NewScheduler(m.deps.SiteDBManager, m)
	go m.scheduler.Run(m.rootCtx)
	m.logger.Info("task scheduler started")
}

// StartLogCleanup runs a daily goroutine that deletes ho_llm_log entries older
// than 30 days from each site's database.
func (m *BrainManager) StartLogCleanup() {
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()

		time.Sleep(5 * time.Minute)
		m.cleanupLLMLogs()

		for {
			select {
			case <-m.rootCtx.Done():
				return
			case <-ticker.C:
				m.cleanupLLMLogs()
			}
		}
	}()
	m.logger.Info("llm log cleanup goroutine started")
}

func (m *BrainManager) cleanupLLMLogs() {
	sites, err := models.ListActiveSites(m.deps.DB.DB)
	if err != nil {
		m.logger.Error("log cleanup: failed to list sites", "error", err)
		return
	}
	for _, site := range sites {
		siteDB, err := m.deps.SiteDBManager.Open(site.ID)
		if err != nil {
			m.logger.Warn("log cleanup: failed to open site db", "site_id", site.ID, "error", err)
			continue
		}
		result, err := siteDB.ExecWrite("DELETE FROM ho_llm_log WHERE created_at < datetime('now', '-30 days')")
		if err != nil {
			m.logger.Warn("log cleanup: delete failed", "site_id", site.ID, "error", err)
			continue
		}
		if n, _ := result.RowsAffected(); n > 0 {
			m.logger.Info("cleaned up old ho_llm_log entries", "site_id", site.ID, "deleted", n)
		}
	}
}

// IsRunning returns true if a pipeline worker exists for the given site.
func (m *BrainManager) IsRunning(siteID int) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, exists := m.workers[siteID]
	return exists
}

// RunningCount returns the number of currently running workers.
func (m *BrainManager) RunningCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.workers)
}

// RunningSites returns the IDs of all sites with active workers.
func (m *BrainManager) RunningSites() []int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	ids := make([]int, 0, len(m.workers))
	for id := range m.workers {
		ids = append(ids, id)
	}
	return ids
}
