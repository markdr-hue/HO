/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Site detail shell — Home (stats + chat) or full-width content panel.
 */

import { h, render, clear } from '../../core/dom.js';
import { get, post } from '../../core/http.js';
import { navigate, currentPath } from '../../core/router.js';
import { icon } from '../../ui/icon.js';
import * as toast from '../../ui/toast.js';
import * as state from '../../core/state.js';
import { setPanelSwitcher } from '../../ui/chat/cards.js';
import { formatPublicUrl } from '../../ui/helpers.js';

import { renderSiteHome } from './home.js';
import { renderSitePages } from './pages.js';
import { renderSiteAssets } from './assets.js';
import { renderSiteTables } from './tables.js';
import { renderSiteEndpoints } from './endpoints.js';
import { renderSiteFiles } from './files.js';
import { renderSiteTasks } from './tasks.js';
import { renderSiteProviders } from './providers.js';
import { renderSiteSettings } from './settings.js';
import { renderSiteLogs } from './logs.js';
import { renderSiteQuestions } from './questions.js';
import { renderSiteWebhooks } from './webhooks.js';
import { renderSiteSecrets } from './secrets.js';

import { renderSiteLayouts } from './layouts.js';
import { renderSiteAnalytics } from './analytics.js';
import { renderSiteDiagnostics } from './diagnostics.js';

// Track cleanup functions
let _headerUnwatchers = [];
let _chatCleanup = null;
let _containerCleanup = null;
let _panelCleanup = null;

export async function renderSiteDetail(container, params) {
  const siteId = parseInt(params.id);
  const panel = params.tab || 'home';

  // Clean up previous state watchers
  for (const fn of _headerUnwatchers) fn();
  _headerUnwatchers = [];
  if (_chatCleanup) {
    _chatCleanup();
    _chatCleanup = null;
  }
  if (_containerCleanup) {
    _containerCleanup();
    _containerCleanup = null;
  }
  if (_panelCleanup) {
    _panelCleanup();
    _panelCleanup = null;
  }

  // Load site data
  let site;
  try {
    site = await get(`/admin/api/sites/${siteId}`);
  } catch (err) {
    toast.error('Failed to load site: ' + err.message);
    navigate('/sites');
    return;
  }

  // Check brain status
  let brainStatus;
  try {
    brainStatus = await get(`/admin/api/brain/${siteId}/status`);
  } catch {
    brainStatus = { state: 'unknown' };
  }

  const isRunning = brainStatus.running || !['idle', 'unknown'].includes(brainStatus.state);

  // --- Header ---
  const statusDot = h('span', {
    className: `status-dot${isRunning ? ' status-dot--active' : ''}`,
  });

  const stateBadge = h('span', {
    className: isRunning ? 'badge badge--success' : stateBadgeClass(brainStatus.state),
    style: { fontSize: '0.75rem' },
  }, isRunning ? stateLabel(brainStatus.mode || site.mode || 'building') : stateLabel(brainStatus.state));

  const modeSelect = h('select', {
    className: 'input',
    style: { width: 'auto', minWidth: '120px', fontSize: '0.8rem' },
    value: site.mode || 'building',
    onChange: async (e) => {
      modeSelect.disabled = true;
      try {
        await post(`/admin/api/brain/${siteId}/mode`, { mode: e.target.value });
        toast.success(`Mode: ${e.target.value}`);
      } catch (err) {
        toast.error('Failed to change mode: ' + err.message);
      }
      modeSelect.disabled = false;
    },
  }, [
    h('option', { value: 'building' }, 'Building'),
    h('option', { value: 'monitoring' }, 'Monitoring'),
    h('option', { value: 'paused' }, 'Paused'),
  ]);
  modeSelect.value = site.mode || 'building';

  const brainBtn = h('button', {
    className: `btn btn--sm ${isRunning ? 'btn--danger' : 'btn--primary'}`,
    onClick: async () => {
      brainBtn.disabled = true;
      brainBtn.textContent = isRunning ? 'Stopping...' : 'Starting...';
      try {
        if (isRunning) {
          await post(`/admin/api/brain/${siteId}/stop`);
          toast.success('Brain stopped');
        } else {
          await post(`/admin/api/brain/${siteId}/start`);
          toast.success('Brain started');
        }
        renderSiteDetail(container, params);
      } catch (err) {
        toast.error(err.message);
        brainBtn.disabled = false;
        brainBtn.textContent = isRunning ? 'Stop' : 'Start';
      }
    },
  }, isRunning ? 'Stop' : 'Start');

  const header = h('div', { className: 'site-header' }, [
    h('div', { className: 'flex items-center gap-3' }, [
      h('button', {
        className: 'btn btn--ghost btn--sm',
        onClick: () => navigate('/sites'),
        innerHTML: icon('chevron-right'),
        style: { transform: 'rotate(180deg)' },
      }),
      statusDot,
      h('h2', { className: 'site-header__name' }, site.name),
      stateBadge,
      (() => {
        const { url, label } = formatPublicUrl(site, state.get('systemStatus'));
        return h('a', {
          href: url,
          target: '_blank',
          rel: 'noopener',
          className: 'link',
          title: 'View site',
          style: { fontSize: '0.8rem', display: 'inline-flex', alignItems: 'center', gap: '3px' },
        }, label);
      })(),
    ]),
    h('div', { className: 'site-header__mode flex items-center gap-2' }, [
      modeSelect,
      brainBtn,
    ]),
  ]);

  // Register panel switcher for chat card links
  setPanelSwitcher((panelName) => {
    navigate(`/sites/${siteId}/${panelName}`);
  });

  if (panel === 'home') {
    // --- Home: stats bar + chat, full width ---
    const homeContainer = h('div', { className: 'site-home-container' });
    const wrapper = h('div', { className: 'flex-col', style: { height: '100%', overflow: 'hidden' } }, [
      header,
      homeContainer,
    ]);
    render(container, wrapper);

    container.style.overflowY = 'hidden';
    _containerCleanup = () => { container.style.overflowY = ''; };

    _chatCleanup = renderSiteHome(homeContainer, siteId);
  } else {
    // --- All other tabs: full-width content, no chat ---
    const contentPanel = h('div', { className: 'site-content-full' });
    const wrapper = h('div', { className: 'flex-col', style: { height: '100%', overflow: 'hidden' } }, [
      header,
      contentPanel,
    ]);
    render(container, wrapper);

    container.style.overflowY = 'hidden';
    _containerCleanup = () => { container.style.overflowY = ''; };

    renderContextPanel(contentPanel, panel, siteId, site);
  }

  // Watch for brain state changes — update header status dot, badge, and button
  _headerUnwatchers.push(state.watch('runningSites', () => {
    const path = currentPath();
    if (!path.startsWith(`/sites/${siteId}`)) return;

    const runningSites = state.get('runningSites') || [];
    const nowRunning = runningSites.includes(siteId);
    statusDot.className = `status-dot${nowRunning ? ' status-dot--active' : ''}`;
    stateBadge.className = nowRunning ? 'badge badge--success' : 'badge badge--warning';
    stateBadge.textContent = nowRunning ? stateLabel(site.mode || 'building') : 'Stopped';
    brainBtn.className = `btn btn--sm ${nowRunning ? 'btn--danger' : 'btn--primary'}`;
    brainBtn.textContent = nowRunning ? 'Stop' : 'Start';
  }));

  // Watch for mode changes — update header badge text
  _headerUnwatchers.push(state.watch('brainModeChanged', (data) => {
    if (!data || data.site_id !== siteId) return;
    site.mode = data.mode;
    const runningSites = state.get('runningSites') || [];
    if (runningSites.includes(siteId)) {
      stateBadge.textContent = stateLabel(data.mode);
    }
    modeSelect.value = data.mode;
  }));
}

function renderContextPanel(container, panel, siteId, site) {
  clear(container);
  if (_panelCleanup) {
    _panelCleanup();
    _panelCleanup = null;
  }

  const panels = {
    pages: (c, id) => renderSitePages(c, id),
    assets: (c, id) => renderSiteAssets(c, id),
    tables: (c, id) => renderSiteTables(c, id),
    endpoints: (c, id) => renderSiteEndpoints(c, id),
    files: (c, id) => renderSiteFiles(c, id),
    tasks: (c, id) => renderSiteTasks(c, id),
    questions: (c, id) => renderSiteQuestions(c, id),
    providers: (c, id) => renderSiteProviders(c, id),
    webhooks: (c, id) => renderSiteWebhooks(c, id),
    logs: (c, id) => renderSiteLogs(c, id),
    secrets: (c, id) => renderSiteSecrets(c, id),

    layouts: (c, id) => renderSiteLayouts(c, id),
    analytics: (c, id) => renderSiteAnalytics(c, id),
    diagnostics: (c, id) => renderSiteDiagnostics(c, id),
    settings: (c, id) => renderSiteSettings(c, id, site),
  };

  const renderFn = panels[panel] || panels.pages;
  const cleanup = renderFn(container, siteId);

  // cleanup may be a Promise (from async render fns) that resolves to a function
  if (cleanup && typeof cleanup.then === 'function') {
    cleanup.then(fn => {
      if (typeof fn === 'function') _panelCleanup = fn;
    });
  } else if (typeof cleanup === 'function') {
    _panelCleanup = cleanup;
  }
}

function stateLabel(s) {
  const labels = {
    idle: 'Stopped',
    building: 'Building',
    monitoring: 'Monitoring',
    paused: 'Paused',
    error: 'Error',
    unknown: 'Unknown',
  };
  return labels[s] || s;
}

function stateBadgeClass(s) {
  if (s === 'error') return 'badge badge--danger';
  if (s === 'paused') return 'badge badge--warning';
  if (s === 'idle' || s === 'unknown') return 'badge badge--warning';
  return 'badge badge--success';
}
