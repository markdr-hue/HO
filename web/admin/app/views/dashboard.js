/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Dashboard view.
 */

import { h, clear } from '../core/dom.js';
import { get } from '../core/http.js';
import { navigate } from '../core/router.js';
import { icon } from '../ui/icon.js';
import * as state from '../core/state.js';
import * as toast from '../ui/toast.js';
import { emptyState, formatPublicUrl } from '../ui/helpers.js';

export async function renderDashboard(container) {
  clear(container);

  const header = h('div', { className: 'context-panel__header context-panel__header--page' }, [
    h('h3', { className: 'context-panel__title context-panel__title--page' }, 'Dashboard'),
  ]);

  const body = h('div', { className: 'context-panel__body context-panel__body--page' });

  const statsGrid = h('div', { className: 'stat-cards' }, [
    h('div', { className: 'skeleton skeleton--card' }),
    h('div', { className: 'skeleton skeleton--card' }),
    h('div', { className: 'skeleton skeleton--card' }),
    h('div', { className: 'skeleton skeleton--card' }),
  ]);
  body.appendChild(statsGrid);

  const sitesContainer = h('div');
  body.appendChild(sitesContainer);

  container.appendChild(header);
  container.appendChild(body);

  try {
    const [status, sites, questions] = await Promise.all([
      get('/admin/api/system/status').catch(() => null),
      get('/admin/api/sites').catch(() => []),
      get('/admin/api/questions?status=pending').catch(() => []),
    ]);

    state.set('sites', sites);
    if (status) state.set('runningSites', status.running_sites || []);

    if (questions.length > 0) {
      const banner = h('div', {
        className: 'attention-banner',
        onClick: () => navigate('/questions'),
      }, [
        h('span', { innerHTML: icon('alert-circle') }),
        h('span', { className: 'attention-banner__text' },
          `${questions.length} pending question${questions.length !== 1 ? 's' : ''} need your attention`
        ),
        h('span', { innerHTML: icon('chevron-right'), style: { marginLeft: 'auto' } }),
      ]);
      body.insertBefore(banner, statsGrid);
    }

    clear(statsGrid);
    const statItems = status ? [
      {
        label: 'Projects',
        value: status.site_count,
        sub: `${status.brain_running} brain${status.brain_running !== 1 ? 's' : ''} running`,
        icon: 'globe',
      },
      {
        label: 'AI Status',
        value: status.provider_count > 0 ? 'Connected' : 'No Provider',
        sub: `${status.provider_count} provider${status.provider_count !== 1 ? 's' : ''} configured`,
        icon: 'brain',
      },
      {
        label: 'System',
        value: status.version,
        sub: `Uptime: ${status.uptime}`,
        icon: 'activity',
      },
      {
        label: 'Pending Questions',
        value: questions.length,
        sub: questions.length > 0 ? 'Needs your input' : 'All clear',
        icon: 'chat',
      },
    ] : [
      { label: 'System Status', value: 'Unavailable', sub: 'Could not load status', icon: 'activity' },
    ];

    for (const stat of statItems) {
      statsGrid.appendChild(
        h('div', { className: 'stat-card' }, [
          h('div', { className: 'flex items-center justify-between mb-2' }, [
            h('span', { className: 'stat-card__label' }, stat.label),
            h('span', { innerHTML: icon(stat.icon), style: { color: 'var(--text-tertiary)' } }),
          ]),
          h('div', { className: 'stat-card__value' }, String(stat.value)),
          h('div', { className: 'stat-card__sub' }, stat.sub),
        ])
      );
    }

    clear(sitesContainer);
    if (sites.length === 0) {
      sitesContainer.appendChild(emptyState('No projects yet. Create your first project to get started.'));
    } else {
      const runningSites = status?.running_sites || [];

      const tableRows = sites.map(site => {
        const isRunning = runningSites.includes(site.id);
        return h('tr', {
          style: { cursor: 'pointer' },
          onClick: () => navigate(`/sites/${site.id}/home`),
        }, [
          h('td', {}, [
            h('div', { className: 'flex items-center gap-2' }, [
              h('span', {
                className: `status-dot${isRunning ? ' status-dot--active' : ''}`,
                dataset: { siteDot: site.id },
              }),
              h('strong', {}, site.name),
            ]),
          ]),
          h('td', {}, (() => {
            if (!site.domain) return '--';
            const { url, label } = formatPublicUrl(site, state.get('systemStatus'));
            return h('a', {
              href: url,
              target: '_blank',
              rel: 'noopener',
              className: 'link',
              onClick: (e) => e.stopPropagation(),
            }, label);
          })()),
          h('td', {}, [
            h('span', {
              className: `badge ${isRunning ? 'badge--success' : 'badge--warning'}`,
              dataset: { siteStatus: site.id },
            }, isRunning ? 'Running' : 'Stopped'),
          ]),
          h('td', { dataset: { siteMode: site.id } }, site.mode || 'building'),
          h('td', {}, [
            h('span', { innerHTML: icon('chevron-right'), style: { color: 'var(--text-tertiary)' } }),
          ]),
        ]);
      });

      const table = h('div', { className: 'table-wrapper' }, [
        h('table', { className: 'table' }, [
          h('thead', {}, [
            h('tr', {}, [
              h('th', {}, 'Project'),
              h('th', {}, 'Domain'),
              h('th', {}, 'Status'),
              h('th', {}, 'Mode'),
              h('th', {}, ''),
            ]),
          ]),
          h('tbody', {}, tableRows),
        ]),
      ]);

      sitesContainer.appendChild(
        h('div', {}, [
          h('div', { className: 'flex items-center justify-between mb-4' }, [
            h('h3', {}, 'Projects'),
            h('button', {
              className: 'btn btn--primary btn--sm',
              onClick: () => navigate('/sites'),
            }, [
              h('span', { innerHTML: icon('plus') }),
              'New Project',
            ]),
          ]),
          table,
        ])
      );
    }
  } catch (err) {
    toast.error('Failed to load dashboard: ' + err.message);
    clear(statsGrid);
    statsGrid.appendChild(
      h('div', { className: 'stat-card' }, [
        h('div', { className: 'stat-card__value text-danger' }, 'Failed to load'),
        h('div', { className: 'stat-card__sub' }, err.message),
      ])
    );
  }
}
