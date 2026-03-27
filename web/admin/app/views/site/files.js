/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Files manager view for context panel.
 */

import { h, clear } from '../../core/dom.js';
import { get, put, del } from '../../core/http.js';
import * as toast from '../../ui/toast.js';
import * as state from '../../core/state.js';
import { formatBytes } from '../../ui/helpers.js';
import {
  isTextEditable,
  renderResourceGrid,
  showUploadModal,
} from '../../ui/resource-cards.js';

export async function renderSiteFiles(container, siteId) {
  clear(container);

  const header = h('div', { className: 'context-panel__header' }, [
    h('h3', { className: 'context-panel__title' }, 'Files'),
  ]);

  const listContainer = h('div', { className: 'context-panel__body' });
  container.appendChild(header);
  container.appendChild(listContainer);

  const unwatch = state.watch('toolExecuted', (evt) => {
    if (evt.site_id === siteId &&
        ['save_file', 'delete_file'].includes(evt.tool) &&
        evt.success) {
      loadFiles(listContainer, siteId);
    }
  });

  await loadFiles(listContainer, siteId);
  return () => unwatch();
}

async function loadFiles(container, siteId) {
  try {
    const files = await get(`/admin/api/sites/${siteId}/files`);
    const config = {
      siteId,
      getName: (f) => f.filename,
      getMeta: (f) => {
        const parts = [`${f.content_type || 'unknown'} \u00b7 ${f.size ? formatBytes(f.size) : '\u2014'}`];
        if (f.description) parts.push(f.description);
        return parts.join(' \u2014 ');
      },
      getIcon: (f) => f.content_type && f.content_type.startsWith('image/') ? 'image' : 'file',
      isEditable: (f) => isTextEditable(f.content_type, f.filename),
      contentUrl: (f) => `/admin/api/sites/${siteId}/files/${f.id}/content`,
      saveContent: (f, content) => put(`/admin/api/sites/${siteId}/files/${f.id}/content`, { content }),
      // No versioning for files
      versionsUrl: null,
      revertUrl: null,
      deleteItem: (f) => del(`/admin/api/sites/${siteId}/files/${f.id}`),
      onReload: () => loadFiles(container, siteId),
      emptyMessage: 'No files uploaded yet.',
      showCreate: false,
      showUpload: true,
      onUpload: () => showUploadModal({
        uploadTitle: 'Upload File',
        uploadUrl: `/admin/api/sites/${siteId}/files`,
        uploadSuccessMsg: 'File uploaded',
        showDescription: true,
        onReload: () => loadFiles(container, siteId),
      }),
    };
    renderResourceGrid(container, files, config);
  } catch (err) {
    toast.error('Failed to load files: ' + err.message);
  }
}
