/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Site-scoped layouts panel.
 */

import { h, clear } from '../../core/dom.js';
import { get, post, put, del } from '../../core/http.js';
import * as toast from '../../ui/toast.js';
import * as modal from '../../ui/modal.js';
import * as state from '../../core/state.js';
import { renderResourceGrid } from '../../ui/resource-cards.js';

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
    const config = {
      siteId,
      getName: (l) => l.name,
      getMeta: (l) => 'Updated: ' + new Date(l.updated_at).toLocaleString(),
      getIcon: () => 'layers',
      getBadge: (l) => l.name === 'default' ? { text: 'Default', className: 'badge--info' } : null,
      onEdit: (l) => showLayoutEditor(l, siteId, container),
      versionsUrl: (l) => `/admin/api/sites/${siteId}/layouts/${l.id}/versions`,
      revertUrl: (l, ver) => `/admin/api/sites/${siteId}/layouts/${l.id}/revert/${ver}`,
      canDelete: (l) => l.name !== 'default',
      deleteItem: (l) => del(`/admin/api/sites/${siteId}/layouts/${l.id}`),
      onReload: () => loadLayouts(container, siteId),
      emptyMessage: 'No layouts created. The AI creates layouts during the design phase.',
      showCreate: false,
      showUpload: false,
    };
    renderResourceGrid(container, layouts, config);
  } catch (err) {
    toast.error('Failed to load layouts: ' + err.message);
  }
}

async function showLayoutEditor(layout, siteId, container) {
  const headInput = h('textarea', {
    className: 'input',
    rows: 4,
    style: { fontFamily: 'monospace', fontSize: 'var(--text-sm)' },
    value: layout.head_content || '',
  });

  const editorContainer = h('div');
  let editor = null;

  const form = h('div', {}, [
    h('div', { className: 'form-group' }, [
      h('label', {}, 'Head Content (fonts, meta, CDN links)'),
      headInput,
    ]),
    h('div', { className: 'form-group' }, [
      h('label', {}, 'Template'),
      editorContainer,
    ]),
  ]);

  import('../../ui/code-editor.js').then(async ({ createEditor }) => {
    editor = await createEditor(editorContainer, {
      value: layout.template || '',
      filename: 'layout.html',
      minHeight: 300,
    });
  }).catch(() => {
    const fallback = h('textarea', {
      className: 'input',
      rows: 12,
      style: { fontFamily: 'monospace', fontSize: 'var(--text-sm)' },
      value: layout.template || '',
    });
    editorContainer.appendChild(fallback);
    editor = { getValue: () => fallback.value, destroy: () => {} };
  });

  modal.show(`Edit Layout: ${layout.name}`, form, [
    { label: 'Cancel', onClick: () => { if (editor) editor.destroy(); } },
    {
      label: 'Save',
      className: 'btn btn--primary',
      onClick: async () => {
        try {
          await put(`/admin/api/sites/${siteId}/layouts/${layout.id}`, {
            head_content: headInput.value,
            template: editor ? editor.getValue() : layout.template,
          });
          toast.success('Layout updated');
          if (editor) editor.destroy();
          loadLayouts(container, siteId);
        } catch (err) {
          toast.error(err.message);
          return false;
        }
      },
    },
  ], { wide: true });
}
