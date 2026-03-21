/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Pages manager view for context panel.
 */

import { h, clear } from '../../core/dom.js';
import { get, put, del } from '../../core/http.js';
import * as toast from '../../ui/toast.js';
import * as state from '../../core/state.js';
import { renderResourceGrid } from '../../ui/resource-cards.js';

export async function renderSitePages(container, siteId) {
  clear(container);

  const header = h('div', { className: 'context-panel__header' }, [
    h('h3', { className: 'context-panel__title' }, 'Pages'),
  ]);

  const listContainer = h('div', { className: 'context-panel__body' });
  container.appendChild(header);
  container.appendChild(listContainer);

  const unwatch = state.watch('toolExecuted', (evt) => {
    if (evt.site_id === siteId &&
        ['save_page', 'delete_page', 'restore_page'].includes(evt.tool) &&
        evt.success) {
      loadPages(listContainer, siteId);
    }
  });

  await loadPages(listContainer, siteId);
  return () => unwatch();
}

async function loadPages(container, siteId) {
  try {
    const pages = await get(`/admin/api/sites/${siteId}/pages`);
    const config = {
      siteId,
      getName: (p) => p.path,
      getMeta: (p) => {
        const title = p.title || '\u2014';
        const date = new Date(p.updated_at).toLocaleDateString();
        return `${title} \u00b7 ${date}`;
      },
      getIcon: () => 'file-text',
      getBadge: (p) => ({
        text: p.status === 'published' ? 'Published' : (p.status || 'Draft'),
        className: p.status === 'published' ? 'badge--success' : 'badge--warning',
      }),
      isEditable: () => true, // Pages are always editable (HTML)
      editorFilename: () => 'page.html',
      contentUrl: (p) => `/admin/api/sites/${siteId}/pages/${p.id}`,
      contentExtractor: (json) => json.content || '',
      saveContent: async (p, content) => {
        await put(`/admin/api/sites/${siteId}/pages/${p.id}`, { content });
      },
      versionsUrl: (p) => `/admin/api/sites/${siteId}/pages/${p.id}/versions`,
      revertUrl: (p, ver) => `/admin/api/sites/${siteId}/pages/${p.id}/revert/${ver}`,
      deleteItem: (p) => del(`/admin/api/sites/${siteId}/pages/${p.id}`),
      onReload: () => loadPages(container, siteId),
      emptyMessage: 'No pages created yet.',
      showCreate: false,
      showUpload: false,
    };

    renderResourceGrid(container, pages, config);
  } catch (err) {
    toast.error('Failed to load pages: ' + err.message);
  }
}
