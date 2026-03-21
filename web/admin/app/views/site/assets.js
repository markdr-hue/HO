/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Assets manager view for context panel.
 */

import { h, clear } from '../../core/dom.js';
import { get, post, del } from '../../core/http.js';
import * as toast from '../../ui/toast.js';
import * as state from '../../core/state.js';
import { formatBytes } from '../../ui/helpers.js';
import {
  isTextEditable,
  renderResourceGrid,
  showUploadModal,
  showCreateModal,
} from '../../ui/resource-cards.js';

export async function renderSiteAssets(container, siteId) {
  clear(container);

  const header = h('div', { className: 'context-panel__header' }, [
    h('h3', { className: 'context-panel__title' }, 'Assets'),
  ]);

  const listContainer = h('div', { className: 'context-panel__body' });
  container.appendChild(header);
  container.appendChild(listContainer);

  const unwatch = state.watch('toolExecuted', (evt) => {
    if (evt.site_id === siteId &&
        ['save_file', 'delete_file'].includes(evt.tool) &&
        evt.success) {
      loadAssets(listContainer, siteId);
    }
  });

  await loadAssets(listContainer, siteId);
  return () => unwatch();
}

async function loadAssets(container, siteId) {
  try {
    const assets = await get(`/admin/api/sites/${siteId}/assets`);
    const config = {
      siteId,
      getName: (a) => a.filename,
      getMeta: (a) => `${a.content_type || 'unknown'} \u00b7 ${a.size ? formatBytes(a.size) : '\u2014'}`,
      getIcon: (a) => a.content_type && a.content_type.startsWith('image/') ? 'image' : 'file',
      isEditable: (a) => isTextEditable(a.content_type, a.filename),
      contentUrl: (a) => `/admin/api/sites/${siteId}/assets/${a.id}/content`,
      saveContent: (a, content) => post(`/admin/api/sites/${siteId}/assets`, { filename: a.filename, content }),
      versionsUrl: (a) => `/admin/api/sites/${siteId}/assets/${a.id}/versions`,
      revertUrl: (a, ver) => `/admin/api/sites/${siteId}/assets/${a.id}/revert/${ver}`,
      deleteItem: (a) => del(`/admin/api/sites/${siteId}/assets/${a.id}`),
      onReload: () => loadAssets(container, siteId),
      emptyMessage: 'No assets created yet.',
      showCreate: true,
      showUpload: true,
      onCreate: () => showCreateModal({
        createTitle: 'Create Asset',
        filenamePlaceholder: 'e.g. styles.css, app.js, header.html',
        createItem: (filename, content) => post(`/admin/api/sites/${siteId}/assets`, { filename, content }),
        onReload: () => loadAssets(container, siteId),
      }),
      onUpload: () => showUploadModal({
        uploadTitle: 'Upload Asset',
        uploadUrl: `/admin/api/sites/${siteId}/assets`,
        uploadSuccessMsg: 'Asset uploaded',
        onReload: () => loadAssets(container, siteId),
      }),
    };
    renderResourceGrid(container, assets, config);
  } catch (err) {
    toast.error('Failed to load assets: ' + err.message);
  }
}
