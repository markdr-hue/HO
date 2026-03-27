/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Global service providers list (read-only overview across all sites).
 */

import { h, clear } from '../../core/dom.js';
import { get } from '../../core/http.js';
import * as toast from '../../ui/toast.js';
import { emptyState } from '../../ui/helpers.js';

export async function renderServiceProviders(container) {
  clear(container);

  const header = h('div', { className: 'context-panel__header context-panel__header--page' }, [
    h('h3', { className: 'context-panel__title context-panel__title--page' }, 'Service Providers'),
  ]);

  const body = h('div', { className: 'context-panel__body context-panel__body--page' });
  const listContainer = h('div');
  body.appendChild(listContainer);

  container.appendChild(header);
  container.appendChild(body);

  try {
    // Load all sites, then load providers per site.
    const sites = await get('/admin/api/sites');
    const allProviders = [];

    for (const site of sites) {
      try {
        const providers = await get(`/admin/api/sites/${site.id}/service-providers`);
        for (const p of providers) {
          p._siteName = site.name || `Site #${site.id}`;
          allProviders.push(p);
        }
      } catch (err) { console.warn(`Failed to load providers for site ${site.id}:`, err); }
    }

    if (allProviders.length === 0) {
      listContainer.appendChild(
        emptyState('No service providers configured on any project. The AI will set these up as needed.')
      );
      return;
    }

    const rows = allProviders.map(prov => {
      const enabledBadge = prov.is_enabled
        ? h('span', { className: 'badge badge--success' }, 'Enabled')
        : h('span', { className: 'badge badge--danger' }, 'Disabled');

      return h('tr', {}, [
        h('td', {}, [
          h('strong', {}, prov.name),
          h('p', { className: 'text-xs text-secondary mt-1' }, prov.description || ''),
        ]),
        h('td', {}, prov._siteName),
        h('td', { className: 'text-xs' }, prov.base_url),
        h('td', {}, prov.auth_type),
        h('td', {}, [enabledBadge]),
      ]);
    });

    listContainer.appendChild(
      h('div', { className: 'table-wrapper' }, [
        h('table', { className: 'table' }, [
          h('thead', {}, [
            h('tr', {}, [
              h('th', {}, 'Provider'),
              h('th', {}, 'Project'),
              h('th', {}, 'Base URL'),
              h('th', {}, 'Auth'),
              h('th', {}, 'Status'),
            ]),
          ]),
          h('tbody', {}, rows),
        ]),
      ])
    );
  } catch (err) {
    toast.error('Failed to load service providers: ' + err.message);
  }
}
