/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Site-scoped analytics panel.
 */

import { h, clear } from '../../core/dom.js';
import { get } from '../../core/http.js';
import { icon } from '../../ui/icon.js';
import * as toast from '../../ui/toast.js';
import { emptyState } from '../../ui/helpers.js';

export async function renderSiteAnalytics(container, siteId) {
  clear(container);

  // Date range state
  const now = new Date();
  const weekAgo = new Date(now);
  weekAgo.setDate(weekAgo.getDate() - 7);

  const startInput = h('input', {
    type: 'date',
    className: 'input',
    style: { width: 'auto', fontSize: 'var(--text-sm)' },
    value: fmt(weekAgo),
  });
  const endInput = h('input', {
    type: 'date',
    className: 'input',
    style: { width: 'auto', fontSize: 'var(--text-sm)' },
    value: fmt(now),
  });

  const body = h('div', { className: 'context-panel__body' });

  function setRange(days) {
    const endDate = new Date();
    const startDate = new Date(endDate);
    if (days === 0) { startDate.setHours(0, 0, 0, 0); }
    else if (days > 0) { startDate.setDate(startDate.getDate() - days); }
    else { startDate.setFullYear(2020); } // "all time"
    startInput.value = fmt(startDate);
    endInput.value = fmt(endDate);
    loadAnalytics(body, siteId, fmt(startDate), fmt(endDate));
  }

  const presets = h('div', { className: 'flex gap-2' }, [
    h('button', { className: 'btn btn--sm btn--ghost', onClick: () => setRange(0) }, 'Today'),
    h('button', { className: 'btn btn--sm btn--ghost', onClick: () => setRange(7) }, '7d'),
    h('button', { className: 'btn btn--sm btn--ghost', onClick: () => setRange(30) }, '30d'),
    h('button', { className: 'btn btn--sm btn--ghost', onClick: () => setRange(-1) }, 'All'),
  ]);

  const refreshBtn = h('button', {
    className: 'btn btn--sm btn--secondary',
    onClick: () => loadAnalytics(body, siteId, startInput.value, endInput.value),
  }, 'Refresh');

  const header = h('div', { className: 'context-panel__header' }, [
    h('h3', { className: 'context-panel__title' }, 'Analytics'),
  ]);

  const controls = h('div', { className: 'flex items-center gap-2 mb-3', style: { flexWrap: 'wrap' } }, [
    startInput, h('span', { className: 'text-secondary' }, 'to'), endInput,
    presets, refreshBtn,
  ]);

  container.appendChild(header);
  body.appendChild(controls);
  container.appendChild(body);

  await loadAnalytics(body, siteId, startInput.value, endInput.value);
}

function fmt(d) {
  return d.toISOString().split('T')[0];
}

async function loadAnalytics(container, siteId, start, end) {
  clear(container);
  container.appendChild(h('p', { className: 'text-secondary text-sm', style: { padding: '12px' } }, 'Loading...'));

  try {
    const data = await get(`/admin/api/sites/${siteId}/analytics/summary?start=${start}&end=${end}`);
    renderAnalytics(container, data);
  } catch (err) {
    clear(container);
    toast.error('Failed to load analytics: ' + err.message);
  }
}

function renderAnalytics(container, data) {
  clear(container);

  if (data.total_views === 0) {
    container.appendChild(emptyState('No analytics data for this period.'));
    return;
  }

  // Stat cards — same pattern as dashboard
  const statsGrid = h('div', { className: 'stat-cards' }, [
    h('div', { className: 'stat-card' }, [
      h('div', { className: 'flex items-center justify-between mb-2' }, [
        h('span', { className: 'stat-card__label' }, 'Total Views'),
        h('span', { innerHTML: icon('activity'), style: { color: 'var(--text-tertiary)' } }),
      ]),
      h('div', { className: 'stat-card__value' }, String(data.total_views)),
    ]),
    h('div', { className: 'stat-card' }, [
      h('div', { className: 'flex items-center justify-between mb-2' }, [
        h('span', { className: 'stat-card__label' }, 'Unique Visitors'),
        h('span', { innerHTML: icon('users'), style: { color: 'var(--text-tertiary)' } }),
      ]),
      h('div', { className: 'stat-card__value' }, String(data.unique_visitors)),
    ]),
  ]);
  container.appendChild(statsGrid);

  // Daily chart
  if (data.daily.length > 0) {
    container.appendChild(h('h4', { className: 'text-sm text-secondary mb-2' }, 'Daily Views'));
    container.appendChild(renderBarChart(data.daily));
  }

  // Top pages
  if (data.top_pages.length > 0) {
    container.appendChild(h('h4', { className: 'text-sm text-secondary mb-2 mt-4' }, 'Top Pages'));
    const maxViews = data.top_pages[0].views;
    for (const p of data.top_pages) {
      const pct = Math.round((p.views / maxViews) * 100);
      container.appendChild(h('div', { className: 'mb-2' }, [
        h('div', { className: 'flex justify-between text-sm mb-1' }, [
          h('code', { style: { fontSize: 'var(--text-xs)' } }, p.path),
          h('span', { className: 'text-secondary' }, String(p.views)),
        ]),
        h('div', { style: { height: '4px', background: 'var(--bg-secondary)', borderRadius: '2px' } }, [
          h('div', { style: { height: '100%', width: pct + '%', background: 'var(--primary)', borderRadius: '2px' } }),
        ]),
      ]));
    }
  }

  // Top referrers
  if (data.top_referrers.length > 0) {
    container.appendChild(h('h4', { className: 'text-sm text-secondary mb-2 mt-4' }, 'Top Referrers'));
    for (const r of data.top_referrers) {
      container.appendChild(h('div', { className: 'flex justify-between text-sm mb-1' }, [
        h('span', { style: { wordBreak: 'break-all' } }, r.referrer),
        h('span', { className: 'text-secondary' }, String(r.count)),
      ]));
    }
  }
}

function renderBarChart(daily) {
  const maxViews = Math.max(...daily.map(d => d.views), 1);

  return h('div', {
    className: 'card mb-3',
    style: { padding: '12px', display: 'flex', alignItems: 'flex-end', gap: '2px', height: '120px' },
  },
    daily.map(d => {
      const pct = Math.max((d.views / maxViews) * 100, 2);
      return h('div', {
        style: {
          flex: '1',
          height: pct + '%',
          background: 'var(--primary)',
          borderRadius: '2px 2px 0 0',
          minWidth: '4px',
        },
        title: `${d.date}: ${d.views} views, ${d.unique_visitors} unique`,
      });
    })
  );
}
