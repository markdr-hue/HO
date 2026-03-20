/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Site-scoped layouts panel.
 */

import { h, clear } from '../../core/dom.js';
import { get, del } from '../../core/http.js';
import { icon } from '../../ui/icon.js';
import * as toast from '../../ui/toast.js';
import * as modal from '../../ui/modal.js';
import * as state from '../../core/state.js';
import { emptyState } from '../../ui/helpers.js';

export async function renderSiteLayouts(container, siteId) {
  clear(container);

  const header = h('div', { className: 'context-panel__header' }, [
    h('h3', { className: 'context-panel__title' }, 'Layouts'),
  ]);

  const body = h('div', { className: 'context-panel__body' });
  container.appendChild(header);
  container.appendChild(body);

  const unwatch = state.watch('toolExecuted', (evt) => {
    if (evt.site_id === siteId &&
        evt.tool === 'manage_layout' && evt.args?.action === 'save' &&
        evt.success) {
      loadLayouts(body, siteId);
    }
  });

  await loadLayouts(body, siteId);
  return () => unwatch();
}

async function loadLayouts(container, siteId) {
  try {
    const layouts = await get(`/admin/api/sites/${siteId}/layouts`);
    renderLayoutsList(container, layouts, siteId);
  } catch (err) {
    toast.error('Failed to load layouts: ' + err.message);
  }
}

function renderLayoutsList(container, layouts, siteId) {
  clear(container);

  if (!layouts || layouts.length === 0) {
    container.appendChild(emptyState('No layouts created. The AI creates layouts during the design phase.'));
    return;
  }

  for (const layout of layouts) {
    const isDefault = layout.name === 'default';

    const card = h('div', { className: 'card mb-3' }, [
      h('div', { className: 'card__header' }, [
        h('div', { className: 'flex items-center gap-2' }, [
          h('span', { innerHTML: icon('layers') }),
          h('h4', { className: 'card__title' }, layout.name),
          ...(isDefault ? [h('span', { className: 'badge badge--info' }, 'Default')] : []),
        ]),
        h('div', { className: 'flex gap-2' }, [
          h('button', {
            className: 'btn btn--sm btn--secondary',
            onClick: () => showLayoutDetail(layout),
          }, 'View'),
          ...(!isDefault ? [h('button', {
            className: 'btn btn--ghost btn--sm',
            title: 'Delete layout',
            onClick: () => {
              modal.confirmDanger('Delete Layout', `Delete layout "${layout.name}"?`, async () => {
                try {
                  await del(`/admin/api/sites/${siteId}/layouts/${layout.id}`);
                  toast.success('Layout deleted');
                  loadLayouts(container, siteId);
                } catch (err) {
                  toast.error(err.message);
                }
              });
            },
          }, [h('span', { innerHTML: icon('trash') })])] : []),
        ]),
      ]),
      h('div', { className: 'text-xs text-secondary', style: { padding: '0 12px 12px' } },
        'Updated: ' + new Date(layout.updated_at).toLocaleString()),
    ]);
    container.appendChild(card);
  }
}

function showLayoutDetail(layout) {
  const sections = [
    { label: 'Head Content', content: layout.head_content },
    { label: 'Template', content: layout.template },
  ];

  const content = h('div', {},
    sections.map(s => h('div', { className: 'mb-3' }, [
      h('p', { className: 'text-sm', style: { fontWeight: 600, marginBottom: '4px' } }, s.label),
      s.content
        ? h('pre', {
            style: {
              fontSize: 'var(--text-xs)',
              padding: '8px',
              background: 'var(--bg-secondary)',
              borderRadius: '4px',
              overflow: 'auto',
              maxHeight: '200px',
              whiteSpace: 'pre-wrap',
              wordBreak: 'break-word',
            },
          }, s.content)
        : h('p', { className: 'text-secondary text-sm' }, '(empty)'),
    ]))
  );

  modal.show(`Layout: ${layout.name}`, content, [{ label: 'Close', onClick: () => {} }]);
}
