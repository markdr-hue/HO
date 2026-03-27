/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Site-specific service providers view.
 */

import { h, clear } from '../../core/dom.js';
import { get, post, del } from '../../core/http.js';
import * as toast from '../../ui/toast.js';
import * as modal from '../../ui/modal.js';
import { emptyState } from '../../ui/helpers.js';

export async function renderSiteProviders(container, siteId) {
  clear(container);

  const header = h('div', { className: 'context-panel__header' }, [
    h('h3', { className: 'context-panel__title' }, 'Service Providers'),
  ]);

  const listContainer = h('div', { className: 'context-panel__body' });
  container.appendChild(header);
  container.appendChild(listContainer);

  try {
    const providers = await get(`/admin/api/sites/${siteId}/service-providers`);

    if (!providers || providers.length === 0) {
      listContainer.appendChild(emptyState('No service providers configured. The AI will set these up as needed when building your project.'));
      return;
    }

    const cards = providers.map(prov => {
      const enabledBadge = prov.is_enabled
        ? h('span', { className: 'badge badge--success' }, 'Enabled')
        : h('span', { className: 'badge badge--danger' }, 'Disabled');

      const secretInfo = prov.secret_name
        ? h('span', { className: 'text-xs text-secondary' }, `Key: ${prov.secret_name}`)
        : h('span', { className: 'text-xs text-secondary', style: { opacity: 0.5 } }, 'No auth');

      return h('div', { className: 'card mb-4' }, [
        h('div', { className: 'card__header' }, [
          h('div', {}, [
            h('h4', { className: 'card__title' }, prov.name),
            h('p', { className: 'text-sm text-secondary mt-1' }, prov.description || ''),
          ]),
          enabledBadge,
        ]),
        h('div', { style: { padding: '0 16px 12px' } }, [
          h('div', { className: 'text-xs text-secondary' }, [
            h('span', {}, prov.base_url),
            h('span', { style: { margin: '0 8px', opacity: 0.3 } }, '|'),
            h('span', {}, prov.auth_type),
            h('span', { style: { margin: '0 8px', opacity: 0.3 } }, '|'),
            secretInfo,
          ]),
        ]),
        prov.api_docs ? h('div', {
          style: { padding: '0 16px 12px' }
        }, [
          h('details', {}, [
            h('summary', {
              className: 'text-xs text-secondary',
              style: { cursor: 'pointer', fontWeight: 600 }
            }, 'API Notes (cached by AI)'),
            h('pre', {
              className: 'text-xs',
              style: {
                background: 'var(--bg-secondary)',
                padding: '8px 12px',
                borderRadius: '6px',
                marginTop: '4px',
                whiteSpace: 'pre-wrap',
                wordBreak: 'break-word',
                maxHeight: '200px',
                overflow: 'auto'
              }
            }, prov.api_docs),
          ]),
        ]) : null,
        h('div', { className: 'flex gap-2', style: { padding: '0 16px 16px' } }, [
          h('button', {
            className: `btn btn--sm ${prov.is_enabled ? 'btn--secondary' : 'btn--primary'}`,
            onClick: async () => {
              await post(`/admin/api/sites/${siteId}/service-providers/${prov.id}/toggle`);
              toast.success(prov.is_enabled ? 'Disabled' : 'Enabled');
              renderSiteProviders(container, siteId);
            },
          }, prov.is_enabled ? 'Disable' : 'Enable'),
          h('button', {
            className: 'btn btn--sm btn--danger',
            onClick: () => {
              modal.confirmDanger('Remove Provider', `Remove provider "${prov.name}"?`, async () => {
                await del(`/admin/api/sites/${siteId}/service-providers/${prov.id}`);
                toast.success('Provider removed');
                renderSiteProviders(container, siteId);
              });
            },
          }, 'Remove'),
        ]),
      ].filter(Boolean));
    });

    cards.forEach(card => listContainer.appendChild(card));
  } catch (err) {
    toast.error('Failed to load service providers: ' + err.message);
  }
}
