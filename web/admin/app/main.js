/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * HO Admin SPA - Entry Point
 */

import * as router from './core/router.js';
import * as theme from './core/theme.js';
import * as state from './core/state.js';
import { SSEClient } from './core/sse.js';
import { get, TOKEN_KEY } from './core/http.js';
import { render, clear, h } from './core/dom.js';
import { addRecent, setActiveSite } from './ui/site-switcher.js';

import { createLayout } from './ui/layout.js';
import { renderLogin } from './views/login.js';
import { renderSetup } from './views/setup/index.js';
import * as toast from './ui/toast.js';
import { renderDashboard } from './views/dashboard.js';
import { renderSites } from './views/sites.js';
import { renderSiteDetail } from './views/site/index.js';
import { renderProviders } from './views/global/providers.js';
import { renderServiceProviders } from './views/global/service-providers.js';
import { renderSettings } from './views/global/settings.js';
import { renderUsage } from './views/global/usage.js';
import { renderUsers } from './views/global/users.js';
import { renderQuestions } from './views/global/questions.js';
import { renderTemplates } from './views/global/templates.js';
import { renderChatProviders } from './views/global/chat-providers.js';

let sse = null;
let layout = null;

/**
 * Show a connection error screen when the backend is unreachable.
 */
function renderConnectionError(app) {
  clear(app);
  app.appendChild(
    h('div', { className: 'connection-error' }, [
      h('h2', {}, 'Unable to connect'),
      h('p', {}, 'Could not reach the HO server. Check that it is running and try again.'),
      h('button', { className: 'btn btn--primary', onClick: () => boot() }, 'Retry'),
    ])
  );
}

/**
 * Boot the application.
 */
async function boot() {
  // Initialize theme from saved preference
  theme.init();

  const app = document.getElementById('app');
  const token = localStorage.getItem(TOKEN_KEY);

  if (!token) {
    // No token: check if first-time setup is needed or show login
    try {
      const check = await get('/admin/api/setup/check');
      if (check.needs_setup) {
        renderSetup(app);
        return;
      }
    } catch {
      renderConnectionError(app);
      return;
    }
    renderLogin(app);
    return;
  }

  // Token exists: validate it and init the shell
  try {
    const status = await get('/admin/api/system/status');
    state.set('systemStatus', status);
    state.set('runningSites', status.running_sites || []);
  } catch (err) {
    // Token likely expired or DB was reset
    localStorage.removeItem(TOKEN_KEY);
    // Check if setup is needed before showing login (handles fresh DB with stale token)
    try {
      const check = await get('/admin/api/setup/check');
      if (check.needs_setup) {
        renderSetup(app);
        return;
      }
    } catch {
      renderConnectionError(app);
      return;
    }
    renderLogin(app);
    return;
  }

  // Load initial data for sidebar
  try {
    const sites = await get('/admin/api/sites');
    state.set('sites', sites);
  } catch {
    state.set('sites', []);
  }

  // Initialize site activity tracking
  state.set('siteActivity', {});

  // Create app layout
  layout = createLayout();
  render(app, layout.root);

  // Register routes
  registerRoutes(layout.main);

  // Track active site on route changes
  window.addEventListener('hashchange', trackActiveSite);
  trackActiveSite();

  // Start router
  if (!window.location.hash || window.location.hash === '#' || window.location.hash === '#/') {
    window.location.hash = '#/dashboard';
  }
  router.start();

  // Connect SSE for real-time updates
  connectSSE();
}

/**
 * Track active site ID and recents based on current URL.
 */
function trackActiveSite() {
  const siteId = router.getActiveSiteId();
  if (siteId) {
    setActiveSite(siteId);
    addRecent(siteId);
  }
}

/**
 * Register all app routes.
 */
function registerRoutes(mainContent) {
  router.register('/dashboard', () => {
    renderDashboard(mainContent);
  });

  router.register('/login', () => {
    // If navigating to login while authed, just go to dashboard
    const token = localStorage.getItem(TOKEN_KEY);
    if (token) {
      router.navigate('/dashboard');
      return;
    }
    const app = document.getElementById('app');
    clear(app);
    renderLogin(app);
  });

  router.register('/sites', () => {
    renderSites(mainContent);
  });

  router.register('/sites/:id/:tab', (params) => {
    renderSiteDetail(mainContent, params);
  });

  router.register('/sites/:id', (params) => {
    router.navigate(`/sites/${params.id}/home`);
  });

  router.register('/providers', () => {
    renderProviders(mainContent);
  });

  router.register('/chat-providers', () => {
    renderChatProviders(mainContent);
  });

  router.register('/service-providers', () => {
    renderServiceProviders(mainContent);
  });

  router.register('/settings', () => {
    renderSettings(mainContent);
  });

  router.register('/usage', () => {
    renderUsage(mainContent);
  });

  router.register('/users', () => {
    renderUsers(mainContent);
  });

  router.register('/questions', () => {
    renderQuestions(mainContent);
  });

  router.register('/templates', () => {
    renderTemplates(mainContent);
  });

  router.setNotFound(() => {
    renderDashboard(mainContent);
  });
}

/**
 * Connect to the SSE event stream for real-time updates.
 */
function connectSSE() {
  if (sse) {
    sse.disconnect();
  }

  sse = new SSEClient();

  // Event names must match backend dot-notation (events/types.go)
  sse.on('site.created', (data) => {
    refreshSites();
  });

  sse.on('site.updated', (data) => {
    refreshSites();
  });

  sse.on('site.deleted', (data) => {
    refreshSites();
  });

  sse.on('brain.started', (data) => {
    refreshStatus();
    updateDashboardStatus(data.site_id, true);
  });

  sse.on('brain.stopped', (data) => {
    refreshStatus();
    updateDashboardStatus(data.site_id, false);
  });

  sse.on('brain.mode_changed', (data) => {
    if (data?.payload?.mode) {
      state.set('brainModeChanged', { site_id: data.site_id, mode: data.payload.mode });
      // Update dashboard mode cell if visible
      const modeCell = document.querySelector(`[data-site-mode="${data.site_id}"]`);
      if (modeCell) modeCell.textContent = data.payload.mode;
    }
  });

  sse.on('question.asked', (data) => {
    // Route to state for inline chat question cards
    if (data?.payload) {
      state.set('questionAsked', { site_id: data.site_id, ...(data.payload || {}) });
      // Increment pending question count for this site's sidebar badge
      const map = Object.assign({}, state.get('pendingQuestions') || {});
      map[data.site_id] = (map[data.site_id] || 0) + 1;
      state.set('pendingQuestions', map);
      // Show global toast regardless of current page
      const siteId = data.site_id;
      toast.show('HO needs your input — check Chat', 'warning', 10000, () => {
        router.navigate(`/sites/${siteId}/home`);
      });
    }
  });

  sse.on('question.answered', (data) => {
    if (data?.payload) {
      state.set('questionAnswered', { site_id: data.site_id, ...(data.payload || {}) });
      // This event only fires when ALL pending questions for this site are answered.
      const map = Object.assign({}, state.get('pendingQuestions') || {});
      map[data.site_id] = 0;
      state.set('pendingQuestions', map);
    }
  });

  // Initialize pending question count from server (SSE only handles deltas).
  get('/admin/api/questions?status=pending').then(questions => {
    if (Array.isArray(questions)) {
      const map = {};
      for (const q of questions) map[q.site_id] = (map[q.site_id] || 0) + 1;
      state.set('pendingQuestions', map);
    }
  }).catch(() => { /* non-critical */ });

  // Brain activity events — routed to state store for chat view consumption.
  sse.on('brain.message', (data) => {
    state.set('brainMessage', { site_id: data.site_id, ...(data.payload || {}) });
    trackSiteActivity(data.site_id);
  });

  sse.on('brain.tool_start', (data) => {
    state.set('brainToolStart', { site_id: data.site_id, ...(data.payload || {}) });
    trackSiteActivity(data.site_id);
  });

  sse.on('brain.tool_result', (data) => {
    state.set('brainToolResult', { site_id: data.site_id, ...(data.payload || {}) });
  });

  sse.on('brain.stage_change', (data) => {
    state.set('brainStageChange', { site_id: data.site_id, ...(data.payload || {}) });
    trackSiteActivity(data.site_id);
  });

  sse.on('brain.progress', (data) => {
    state.set('brainProgress', { site_id: data.site_id, ...(data.payload || {}) });
  });

  sse.on('tool.executed', (data) => {
    if (data?.payload) {
      state.set('toolExecuted', {
        site_id: data.site_id,
        tool: data.payload.tool,
        success: data.payload.success,
        timestamp: Date.now(),
      });
    }
  });

  sse.on('page.saved', (data) => {
    state.set('toolExecuted', {
      site_id: data.site_id,
      tool: 'save_page',
      success: true,
      timestamp: Date.now(),
    });
  });

  sse.connect('/admin/api/events/stream');
}

/**
 * Track brain activity per site for cross-site activity indicator.
 */
function trackSiteActivity(siteId) {
  if (!siteId) return;
  const activity = { ...(state.get('siteActivity') || {}) };
  activity[siteId] = Date.now();
  state.set('siteActivity', activity);
}

/**
 * Update dashboard table status cells if visible (called from SSE handlers).
 */
function updateDashboardStatus(siteId, running) {
  const dot = document.querySelector(`[data-site-dot="${siteId}"]`);
  if (dot) dot.className = `status-dot${running ? ' status-dot--active' : ''}`;
  const badge = document.querySelector(`[data-site-status="${siteId}"]`);
  if (badge) {
    badge.className = `badge ${running ? 'badge--success' : 'badge--warning'}`;
    badge.textContent = running ? 'Running' : 'Stopped';
  }
}

async function refreshSites() {
  try {
    const sites = await get('/admin/api/sites');
    state.set('sites', sites);
  } catch (_) { /* silent */ }
}

async function refreshStatus() {
  try {
    const status = await get('/admin/api/system/status');
    state.set('systemStatus', status);
    state.set('runningSites', status.running_sites || []);
  } catch (_) { /* silent */ }
}

// Boot on DOM ready
if (document.readyState === 'loading') {
  document.addEventListener('DOMContentLoaded', boot);
} else {
  boot();
}
