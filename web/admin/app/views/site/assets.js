/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Assets manager view for context panel.
 * Supports listing, creating (code editor), uploading, and deleting assets.
 */

import { h, clear } from '../../core/dom.js';
import { get, post, del, getToken } from '../../core/http.js';
import { icon } from '../../ui/icon.js';
import * as toast from '../../ui/toast.js';
import * as modal from '../../ui/modal.js';
import * as state from '../../core/state.js';
import { emptyState, formatBytes } from '../../ui/helpers.js';

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
    renderAssetsList(container, assets, siteId);
  } catch (err) {
    toast.error('Failed to load assets: ' + err.message);
  }
}

function renderAssetsList(container, assets, siteId) {
  clear(container);

  const actions = h('div', { className: 'flex items-center gap-2 mb-3' }, [
    h('button', {
      className: 'btn btn--primary btn--sm',
      onClick: () => showCreateModal(container, siteId),
    }, [h('span', { innerHTML: icon('plus') }), ' Create']),
    h('button', {
      className: 'btn btn--sm btn--secondary',
      onClick: () => showUploadModal(container, siteId),
    }, [h('span', { innerHTML: icon('upload') }), ' Upload']),
  ]);
  container.appendChild(actions);

  if (!assets || assets.length === 0) {
    container.appendChild(emptyState('No assets created yet.'));
    return;
  }

  const grid = h('div', { className: 'assets-grid' });

  for (const asset of assets) {
    const isImage = asset.content_type && asset.content_type.startsWith('image/');
    const isText = isTextType(asset.content_type);
    const sizeStr = asset.size ? formatBytes(asset.size) : '\u2014';

    const card = h('div', { className: 'asset-card' }, [
      h('div', { className: 'asset-card__preview' }, [
        h('div', { className: 'asset-card__img-placeholder' }, [
          h('span', { innerHTML: icon(isImage ? 'image' : 'file') }),
        ]),
      ]),
      h('div', { className: 'asset-card__info' }, [
        h('span', { className: 'asset-card__name' }, asset.filename),
        h('span', { className: 'text-xs text-secondary' }, `${asset.content_type || 'unknown'} \u00b7 ${sizeStr}`),
      ]),
      h('div', { className: 'asset-card__actions' }, [
        isText ? h('button', {
          className: 'btn btn--ghost btn--sm',
          title: 'Edit',
          onClick: (e) => {
            e.stopPropagation();
            showEditModal(container, asset, siteId);
          },
        }, [h('span', { innerHTML: icon('edit') })]) : null,
        h('button', {
          className: 'btn btn--ghost btn--sm',
          title: 'Delete',
          onClick: (e) => {
            e.stopPropagation();
            confirmDelete(container, asset, siteId);
          },
        }, [h('span', { innerHTML: icon('trash') })]),
      ].filter(Boolean)),
    ]);

    grid.appendChild(card);
  }

  container.appendChild(grid);
}

function showCreateModal(container, siteId) {
  const filenameInput = h('input', {
    className: 'input',
    type: 'text',
    placeholder: 'e.g. styles.css, app.js, header.html',
  });
  const contentArea = h('textarea', {
    className: 'input',
    rows: 12,
    style: { fontFamily: 'monospace', fontSize: 'var(--text-sm)', tabSize: '2' },
    placeholder: 'Enter file content...',
  });

  const form = h('div', {}, [
    h('div', { className: 'form-group' }, [
      h('label', {}, 'Filename'),
      filenameInput,
    ]),
    h('div', { className: 'form-group' }, [
      h('label', {}, 'Content'),
      contentArea,
    ]),
  ]);

  modal.show('Create Asset', form, [
    { label: 'Cancel', onClick: () => {} },
    {
      label: 'Create',
      className: 'btn btn--primary',
      onClick: async () => {
        const filename = filenameInput.value.trim();
        if (!filename) {
          toast.error('Filename is required');
          return false;
        }
        try {
          await post(`/admin/api/sites/${siteId}/assets`, {
            filename,
            content: contentArea.value,
          });
          toast.success('Asset created');
          await loadAssets(container, siteId);
        } catch (err) {
          toast.error(err.message);
          return false;
        }
      },
    },
  ]);
}

function showUploadModal(container, siteId) {
  const fileInput = h('input', { type: 'file', className: 'input' });

  const form = h('div', {}, [
    h('div', { className: 'form-group' }, [
      h('label', {}, 'File'),
      fileInput,
    ]),
  ]);

  modal.show('Upload Asset', form, [
    { label: 'Cancel', onClick: () => {} },
    {
      label: 'Upload',
      className: 'btn btn--primary',
      onClick: async () => {
        const file = fileInput.files && fileInput.files[0];
        if (!file) {
          toast.error('Please select a file');
          return false;
        }

        const formData = new FormData();
        formData.append('file', file);

        try {
          const token = getToken();
          const res = await fetch(`/admin/api/sites/${siteId}/assets`, {
            method: 'POST',
            headers: token ? { 'Authorization': `Bearer ${token}` } : {},
            body: formData,
          });
          if (!res.ok) {
            const err = await res.json().catch(() => ({}));
            throw new Error(err.error || `Upload failed (${res.status})`);
          }
          toast.success('Asset uploaded');
          await loadAssets(container, siteId);
        } catch (err) {
          toast.error(err.message);
          return false;
        }
      },
    },
  ]);
}

async function showEditModal(container, asset, siteId) {
  let content = '';
  try {
    const res = await fetch(`/admin/api/sites/${siteId}/assets/${asset.id}/content`, {
      headers: { 'Authorization': `Bearer ${getToken()}` },
    });
    if (res.ok) content = await res.text();
  } catch (err) { console.warn('Asset content load failed:', err); }

  const contentArea = h('textarea', {
    className: 'input',
    rows: 14,
    style: { fontFamily: 'monospace', fontSize: 'var(--text-sm)', tabSize: '2' },
    value: content,
  });

  const form = h('div', {}, [
    h('div', { className: 'form-group' }, [
      h('label', {}, asset.filename),
      contentArea,
    ]),
  ]);

  modal.show('Edit Asset', form, [
    { label: 'Cancel', onClick: () => {} },
    {
      label: 'Save',
      className: 'btn btn--primary',
      onClick: async () => {
        try {
          await post(`/admin/api/sites/${siteId}/assets`, {
            filename: asset.filename,
            content: contentArea.value,
          });
          toast.success('Asset updated');
          await loadAssets(container, siteId);
        } catch (err) {
          toast.error(err.message);
          return false;
        }
      },
    },
  ]);
}

function confirmDelete(container, asset, siteId) {
  modal.show('Delete Asset', h('p', {}, `Delete "${asset.filename}"? This cannot be undone.`), [
    { label: 'Cancel', onClick: () => {} },
    {
      label: 'Delete',
      className: 'btn btn--danger',
      onClick: async () => {
        try {
          await del(`/admin/api/sites/${siteId}/assets/${asset.id}`);
          toast.success('Asset deleted');
          loadAssets(container, siteId);
        } catch (err) {
          toast.error(err.message);
          return false;
        }
      },
    },
  ]);
}

function isTextType(contentType) {
  if (!contentType) return false;
  return contentType.startsWith('text/') ||
    contentType === 'application/javascript' ||
    contentType === 'application/json' ||
    contentType === 'application/xml';
}

