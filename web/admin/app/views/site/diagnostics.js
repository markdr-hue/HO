/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Site-scoped diagnostics panel.
 */

import { h, clear } from '../../core/dom.js';
import { get } from '../../core/http.js';
import { icon } from '../../ui/icon.js';
import * as toast from '../../ui/toast.js';
import { emptyState } from '../../ui/helpers.js';

export async function renderSiteDiagnostics(container, siteId) {
  clear(container);

  const header = h('div', { className: 'context-panel__header' }, [
    h('h3', { className: 'context-panel__title' }, 'Diagnostics'),
  ]);

  const body = h('div', { className: 'context-panel__body' });

  const refreshBtn = h('button', {
    className: 'btn btn--sm btn--secondary mb-3',
    onClick: () => loadAll(body, siteId),
  }, 'Run Checks');

  container.appendChild(header);
  body.appendChild(refreshBtn);
  container.appendChild(body);

  await loadAll(body, siteId);
}

async function loadAll(container, siteId) {
  clear(container);
  container.appendChild(h('p', { className: 'text-secondary text-sm', style: { padding: '12px' } }, 'Running checks...'));

  try {
    const [health, integrity, errors] = await Promise.all([
      get(`/admin/api/sites/${siteId}/diagnostics/health`),
      get(`/admin/api/sites/${siteId}/diagnostics/integrity`),
      get(`/admin/api/sites/${siteId}/diagnostics/errors`),
    ]);
    renderResults(container, health, integrity, errors);
  } catch (err) {
    clear(container);
    toast.error('Failed to run diagnostics: ' + err.message);
  }
}

function renderResults(container, health, integrity, errors) {
  clear(container);

  // Health section
  container.appendChild(sectionHeader('System Health', 'activity'));
  const rt = health.runtime || {};
  const mem = health.memory || {};
  const site = health.site || {};
  container.appendChild(h('div', { className: 'stat-cards stat-cards--compact' }, [
    statCard('Go', rt.go_version, 'code'),
    statCard('Goroutines', rt.goroutines, 'activity'),
    statCard('Memory', (mem.alloc_mb ?? '-') + ' MB', 'cpu'),
    statCard('GC Runs', mem.num_gc, 'refresh-cw'),
    statCard('Pages', site.pages, 'file-text'),
    statCard('Memories', site.memories, 'brain'),
    statCard('Tables', site.tables, 'database'),
  ]));

  // Integrity section
  const hasRealIssues = (integrity.issues || []).some(i => i.level === 'error' || i.level === 'warning');
  container.appendChild(sectionHeader('Integrity Check', integrity.ok ? 'check' : hasRealIssues ? 'alert-circle' : 'info'));
  const issues = integrity.issues || [];
  if (issues.length === 0) {
    container.appendChild(h('div', { className: 'card mb-3' }, [
      h('div', { className: 'flex items-center gap-2' }, [
        h('span', { innerHTML: icon('check'), style: { color: 'var(--success)' } }),
        h('span', { className: 'text-sm' }, 'All checks passed.'),
      ]),
    ]));
  } else {
    for (const issue of issues) {
      const levelIcon = issue.level === 'error' ? 'alert-circle' : issue.level === 'info' ? 'info' : 'alert-triangle';
      const levelColor = issue.level === 'error' ? 'var(--danger)' : issue.level === 'info' ? 'var(--info)' : 'var(--warning)';
      const badgeClass = issue.level === 'error' ? 'badge--danger' : issue.level === 'info' ? 'badge--info' : 'badge--warning';
      container.appendChild(h('div', { className: 'card mb-2' }, [
        h('div', { className: 'flex items-center gap-2' }, [
          h('span', {
            innerHTML: icon(levelIcon),
            style: { color: levelColor },
          }),
          h('span', { className: 'text-sm' }, issue.message),
          h('span', { className: `badge ${badgeClass}` }, issue.level),
        ]),
      ]));
    }
  }

  // Errors section
  container.appendChild(sectionHeader('Recent Errors', 'zap'));
  if (errors.length === 0) {
    container.appendChild(h('div', { className: 'card mb-3' }, [
      h('div', { className: 'flex items-center gap-2' }, [
        h('span', { innerHTML: icon('check'), style: { color: 'var(--text-tertiary)' } }),
        h('span', { className: 'text-sm text-secondary' }, 'No recent errors.'),
      ]),
    ]));
  } else {
    for (const err of errors) {
      container.appendChild(h('div', { className: 'card mb-2' }, [
        h('div', { className: 'card__header' }, [
          h('div', { className: 'flex items-center gap-2' }, [
            h('span', { className: 'badge badge--danger' }, err.event_type),
            h('span', { className: 'text-sm' }, err.summary),
          ]),
          h('span', { className: 'text-xs text-secondary' },
            new Date(err.created_at).toLocaleString()),
        ]),
        ...(err.details ? [h('pre', {
          className: 'text-xs text-secondary',
          style: { whiteSpace: 'pre-wrap', wordBreak: 'break-word', maxHeight: '100px', overflow: 'auto', margin: 0 },
        }, err.details)] : []),
      ]));
    }
  }
}

function sectionHeader(title, iconName) {
  return h('h4', { className: 'flex items-center gap-2 text-sm text-secondary mb-2 mt-4' }, [
    h('span', { innerHTML: icon(iconName) }),
    h('span', {}, title),
  ]);
}

function statCard(label, value, iconName) {
  return h('div', { className: 'stat-card' }, [
    h('div', { className: 'flex items-center justify-between mb-2' }, [
      h('span', { className: 'stat-card__label' }, label),
      h('span', { innerHTML: icon(iconName), style: { color: 'var(--text-tertiary)' } }),
    ]),
    h('div', { className: 'stat-card__value' }, String(value ?? '-')),
  ]);
}
