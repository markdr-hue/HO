/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Files manager view for context panel.
 */

import { h, clear } from '../../core/dom.js';
import { get, del, getToken } from '../../core/http.js';
import { icon } from '../../ui/icon.js';
import * as toast from '../../ui/toast.js';
import * as modal from '../../ui/modal.js';
import * as state from '../../core/state.js';
import { emptyState, formatBytes } from '../../ui/helpers.js';

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
    renderFilesList(container, files, siteId);
  } catch (err) {
    toast.error('Failed to load files: ' + err.message);
  }
}

function renderFilesList(container, files, siteId) {
  clear(container);

  const uploadBtn = h('button', {
    className: 'btn btn--primary btn--sm mb-3',
    onClick: () => showUploadModal(container, siteId),
  }, [h('span', { innerHTML: icon('upload') }), ' Upload File']);
  container.appendChild(uploadBtn);

  if (!files || files.length === 0) {
    container.appendChild(emptyState('No files uploaded yet.'));
    return;
  }

  for (const file of files) {
    const sizeStr = file.size ? formatBytes(file.size) : '\u2014';

    const card = h('div', { className: 'card mb-3' }, [
      h('div', { className: 'card__header' }, [
        h('div', { className: 'flex items-center gap-2' }, [
          h('span', { innerHTML: icon('file') }),
          h('strong', {}, file.filename),
        ]),
        h('button', {
          className: 'btn btn--ghost btn--sm',
          title: 'Delete file',
          onClick: (e) => {
            e.stopPropagation();
            confirmDelete(container, file, siteId);
          },
        }, [h('span', { innerHTML: icon('trash') })]),
      ]),
      h('div', { style: { padding: '0 12px 12px' } }, [
        h('span', { className: 'text-sm text-secondary' },
          `${file.content_type || 'unknown'} \u00b7 ${sizeStr}`),
        file.description
          ? h('p', { className: 'text-sm mt-1' }, file.description)
          : null,
      ].filter(Boolean)),
    ]);
    container.appendChild(card);
  }
}

function showUploadModal(container, siteId) {
  const fileInput = h('input', { type: 'file', className: 'input' });
  const descInput = h('input', {
    type: 'text',
    className: 'input',
    placeholder: 'Optional description',
  });

  const form = h('div', {}, [
    h('div', { className: 'form-group' }, [
      h('label', {}, 'File'),
      fileInput,
    ]),
    h('div', { className: 'form-group' }, [
      h('label', {}, 'Description'),
      descInput,
    ]),
  ]);

  modal.show('Upload File', form, [
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
        if (descInput.value.trim()) {
          formData.append('description', descInput.value.trim());
        }

        try {
          const token = getToken();
          const res = await fetch(`/admin/api/sites/${siteId}/files`, {
            method: 'POST',
            headers: token ? { 'Authorization': `Bearer ${token}` } : {},
            body: formData,
          });
          if (!res.ok) {
            const err = await res.json().catch(() => ({}));
            throw new Error(err.error || `Upload failed (${res.status})`);
          }
          toast.success('File uploaded');
          await loadFiles(container, siteId);
        } catch (err) {
          toast.error(err.message);
          return false;
        }
      },
    },
  ]);
}

function confirmDelete(container, file, siteId) {
  modal.show('Delete File',
    h('p', {}, `Delete "${file.filename}"? This cannot be undone.`),
    [
      { label: 'Cancel', onClick: () => {} },
      {
        label: 'Delete',
        className: 'btn btn--danger',
        onClick: async () => {
          try {
            await del(`/admin/api/sites/${siteId}/files/${file.id}`);
            toast.success('File deleted');
            loadFiles(container, siteId);
          } catch (err) {
            toast.error(err.message);
            return false;
          }
        },
      },
    ]
  );
}

