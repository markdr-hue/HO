/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Shared resource card grid component for Pages, Assets, and Files.
 * Provides unified card layout, edit modal, version history, upload, and create.
 */

import { h, clear } from '../core/dom.js';
import { get, post, put, del, getToken } from '../core/http.js';
import { icon } from './icon.js';
import * as toast from './toast.js';
import * as modal from './modal.js';
import { emptyState, formatBytes } from './helpers.js';

const TEXT_EXTENSIONS = new Set([
  'txt', 'csv', 'css', 'js', 'jsx', 'ts', 'tsx', 'json', 'xml', 'svg',
  'html', 'htm', 'md', 'yaml', 'yml', 'toml', 'env', 'cfg', 'ini',
  'sh', 'bat', 'sql', 'graphql', 'py', 'rb', 'php', 'go', 'rs', 'lua',
]);

const TEXT_CONTENT_TYPES = new Set([
  'application/javascript', 'application/json', 'application/xml',
  'image/svg+xml', 'application/x-yaml', 'application/toml',
]);

/**
 * Check if a file is editable as text.
 */
export function isTextEditable(contentType, filename) {
  if (filename) {
    const ext = filename.split('.').pop().toLowerCase();
    if (TEXT_EXTENSIONS.has(ext)) return true;
  }
  if (!contentType) return false;
  if (contentType.startsWith('text/')) return true;
  return TEXT_CONTENT_TYPES.has(contentType);
}

/**
 * Render a resource grid with action bar and cards.
 *
 * @param {HTMLElement} container
 * @param {Array} items
 * @param {Object} config
 * @param {string} config.siteId
 * @param {Function} config.getName - (item) => display name
 * @param {Function} config.getMeta - (item) => secondary text
 * @param {Function} config.getIcon - (item) => icon name
 * @param {Function} [config.getBadge] - (item) => { text, className } or null
 * @param {Function} [config.isEditable] - (item) => boolean
 * @param {Function} [config.contentUrl] - (item) => URL to fetch content
 * @param {Function} [config.saveContent] - (item, content) => Promise
 * @param {Function} [config.versionsUrl] - (item) => URL or null
 * @param {Function} [config.revertUrl] - (item, version) => URL
 * @param {Function} config.deleteItem - (item) => Promise
 * @param {Function} config.onReload - () => reload list
 * @param {string} config.emptyMessage
 * @param {boolean} [config.showCreate]
 * @param {boolean} [config.showUpload]
 * @param {Function} [config.onCreate] - () => show create modal
 * @param {Function} [config.onUpload] - () => show upload modal
 */
export function renderResourceGrid(container, items, config) {
  clear(container);

  // Action bar
  const buttons = [];
  if (config.showCreate && config.onCreate) {
    buttons.push(h('button', {
      className: 'btn btn--primary btn--sm',
      onClick: config.onCreate,
    }, [h('span', { innerHTML: icon('plus') }), ' Create']));
  }
  if (config.showUpload && config.onUpload) {
    buttons.push(h('button', {
      className: 'btn btn--sm btn--secondary',
      onClick: config.onUpload,
    }, [h('span', { innerHTML: icon('upload') }), ' Upload']));
  }
  if (buttons.length > 0) {
    container.appendChild(h('div', { className: 'flex items-center gap-2 mb-3' }, buttons));
  }

  if (!items || items.length === 0) {
    container.appendChild(emptyState(config.emptyMessage));
    return;
  }

  const grid = h('div', { className: 'assets-grid' });

  for (const item of items) {
    grid.appendChild(renderResourceCard(item, config));
  }

  container.appendChild(grid);
}

/**
 * Render a single resource card.
 */
function renderResourceCard(item, config) {
  const name = config.getName(item);
  const meta = config.getMeta(item);
  const iconName = config.getIcon(item);
  const editable = config.isEditable ? config.isEditable(item) : false;
  const badge = config.getBadge ? config.getBadge(item) : null;
  const hasVersions = item.version_count > 0 && config.versionsUrl;

  const actionBtns = [];

  if (config.onEdit) {
    actionBtns.push(h('button', {
      className: 'btn btn--ghost btn--sm',
      title: 'Edit',
      onClick: (e) => {
        e.stopPropagation();
        config.onEdit(item);
      },
    }, [h('span', { innerHTML: icon('edit') })]));
  } else if (editable && config.contentUrl) {
    actionBtns.push(h('button', {
      className: 'btn btn--ghost btn--sm',
      title: 'Edit',
      onClick: (e) => {
        e.stopPropagation();
        showEditModal(item, config);
      },
    }, [h('span', { innerHTML: icon('edit') })]));
  }

  if (hasVersions) {
    actionBtns.push(h('button', {
      className: 'btn btn--ghost btn--sm',
      title: 'Version history',
      onClick: (e) => {
        e.stopPropagation();
        showVersionHistory(item, config);
      },
    }, [h('span', { innerHTML: icon('clock') })]));
  }

  const canDelete = config.canDelete ? config.canDelete(item) : true;
  if (canDelete) {
    actionBtns.push(h('button', {
      className: 'btn btn--ghost btn--sm',
      title: 'Delete',
      onClick: (e) => {
        e.stopPropagation();
        confirmDelete(item, config);
      },
    }, [h('span', { innerHTML: icon('trash') })]));
  }

  const previewChildren = [
    h('div', { className: 'asset-card__img-placeholder' }, [
      h('span', { innerHTML: icon(iconName) }),
    ]),
  ];
  if (badge) {
    previewChildren.push(
      h('span', { className: `badge badge--sm ${badge.className || ''}` }, badge.text)
    );
  }

  return h('div', { className: 'asset-card' }, [
    h('div', { className: 'asset-card__preview' }, previewChildren),
    h('div', { className: 'asset-card__info' }, [
      h('span', { className: 'asset-card__name' }, name),
      h('span', { className: 'text-xs text-secondary' }, meta),
    ]),
    h('div', { className: 'asset-card__actions' }, actionBtns),
  ]);
}

/**
 * Show a wide modal with CodeMirror editor for editing a resource.
 */
async function showEditModal(item, config) {
  const name = config.getName(item);
  let content = '';
  try {
    const res = await fetch(config.contentUrl(item), {
      headers: { 'Authorization': `Bearer ${getToken()}` },
    });
    if (res.ok) {
      if (config.contentExtractor) {
        const json = await res.json();
        content = config.contentExtractor(json);
      } else {
        content = await res.text();
      }
    }
  } catch (_) {
    // Content load failed — editor will open empty.
  }

  const editorContainer = h('div');
  let editor = null;

  const form = h('div', {}, [
    h('div', { className: 'form-group' }, [
      h('label', {}, name),
      editorContainer,
    ]),
  ]);

  const editorFilename = config.editorFilename ? config.editorFilename(item) : name;
  import('./code-editor.js').then(async ({ createEditor }) => {
    editor = await createEditor(editorContainer, {
      value: content,
      filename: editorFilename,
      minHeight: 350,
    });
  }).catch(() => {
    const fallback = h('textarea', {
      className: 'input',
      rows: 14,
      style: { fontFamily: 'monospace', fontSize: 'var(--text-sm)', tabSize: '2' },
      value: content,
    });
    editorContainer.appendChild(fallback);
    editor = { getValue: () => fallback.value, destroy: () => {} };
  });

  modal.show(`Edit: ${name}`, form, [
    { label: 'Cancel', onClick: () => { if (editor) editor.destroy(); } },
    {
      label: 'Save',
      className: 'btn btn--primary',
      onClick: async () => {
        const newContent = editor ? editor.getValue() : content;
        try {
          await config.saveContent(item, newContent);
          toast.success(`${name} updated`);
          if (editor) editor.destroy();
          config.onReload();
        } catch (err) {
          toast.error(err.message);
          return false;
        }
      },
    },
  ], { wide: true });
}

/**
 * Show version history modal with revert buttons.
 */
async function showVersionHistory(item, config) {
  const name = config.getName(item);
  let versions;
  try {
    versions = await get(config.versionsUrl(item));
  } catch (err) {
    toast.error('Failed to load history: ' + err.message);
    return;
  }

  if (!versions || versions.length === 0) {
    toast.info(`No version history for ${name}.`);
    return;
  }

  const list = h('div', { className: 'version-list' });
  for (const v of versions) {
    const date = new Date(v.created_at).toLocaleString();
    const row = h('div', {
      className: 'version-row flex items-center gap-2',
      style: { padding: '8px 0', borderBottom: '1px solid var(--color-border)' },
    }, [
      h('span', { className: 'badge badge--sm' }, `v${v.version_number}`),
      h('span', { className: 'text-sm text-secondary', style: { flex: '1' } },
        `${v.changed_by} \u00b7 ${date}`),
      h('button', {
        className: 'btn btn--sm btn--secondary',
        onClick: async () => {
          try {
            await post(config.revertUrl(item, v.version_number));
            toast.success(`Reverted ${name} to v${v.version_number}`);
            modal.close();
            config.onReload();
          } catch (err) {
            toast.error('Revert failed: ' + err.message);
          }
        },
      }, 'Revert'),
    ]);
    list.appendChild(row);
  }

  modal.show(`History: ${name}`, list, [
    { label: 'Close', onClick: () => {} },
  ]);
}

/**
 * Show a delete confirmation modal.
 */
function confirmDelete(item, config) {
  const name = config.getName(item);
  modal.show('Delete', h('p', {}, `Delete "${name}"? This cannot be undone.`), [
    { label: 'Cancel', onClick: () => {} },
    {
      label: 'Delete',
      className: 'btn btn--danger',
      onClick: async () => {
        try {
          await config.deleteItem(item);
          toast.success(`${name} deleted`);
          config.onReload();
        } catch (err) {
          toast.error(err.message);
          return false;
        }
      },
    },
  ]);
}

/**
 * Show upload modal with file input and optional description.
 */
export function showUploadModal(config) {
  const fileInput = h('input', { type: 'file', className: 'input' });
  const descInput = config.showDescription ? h('input', {
    type: 'text',
    className: 'input',
    placeholder: 'Optional description',
  }) : null;

  const fields = [
    h('div', { className: 'form-group' }, [
      h('label', {}, 'File'),
      fileInput,
    ]),
  ];
  if (descInput) {
    fields.push(h('div', { className: 'form-group' }, [
      h('label', {}, 'Description'),
      descInput,
    ]));
  }

  const form = h('div', {}, fields);

  modal.show(config.uploadTitle || 'Upload', form, [
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
        if (descInput && descInput.value.trim()) {
          formData.append('description', descInput.value.trim());
        }

        try {
          const token = getToken();
          const res = await fetch(config.uploadUrl, {
            method: 'POST',
            headers: token ? { 'Authorization': `Bearer ${token}` } : {},
            body: formData,
          });
          if (!res.ok) {
            const err = await res.json().catch(() => ({}));
            throw new Error(err.error || `Upload failed (${res.status})`);
          }
          toast.success(config.uploadSuccessMsg || 'Uploaded');
          config.onReload();
        } catch (err) {
          toast.error(err.message);
          return false;
        }
      },
    },
  ]);
}

/**
 * Show create modal with filename input and CodeMirror editor.
 */
export function showCreateModal(config) {
  const filenameInput = h('input', {
    className: 'input',
    type: 'text',
    placeholder: config.filenamePlaceholder || 'e.g. styles.css, app.js',
  });
  const editorContainer = h('div', { style: { marginTop: '8px' } });
  let editor = null;

  filenameInput.addEventListener('change', async () => {
    const fn = filenameInput.value.trim();
    if (editor) editor.destroy();
    editorContainer.innerHTML = '';
    const { createEditor } = await import('./code-editor.js');
    editor = await createEditor(editorContainer, { filename: fn, minHeight: 250 });
  });

  const form = h('div', {}, [
    h('div', { className: 'form-group' }, [
      h('label', {}, config.filenameLabel || 'Filename'),
      filenameInput,
    ]),
    h('div', { className: 'form-group' }, [
      h('label', {}, 'Content'),
      editorContainer,
    ]),
  ]);

  const fallbackArea = h('textarea', {
    className: 'input',
    rows: 10,
    style: { fontFamily: 'monospace', fontSize: 'var(--text-sm)', tabSize: '2' },
    placeholder: 'Enter filename first to activate syntax highlighting...',
  });
  editorContainer.appendChild(fallbackArea);

  modal.show(config.createTitle || 'Create', form, [
    { label: 'Cancel', onClick: () => { if (editor) editor.destroy(); } },
    {
      label: 'Create',
      className: 'btn btn--primary',
      onClick: async () => {
        const filename = filenameInput.value.trim();
        if (!filename) {
          toast.error('Filename is required');
          return false;
        }
        const content = editor ? editor.getValue() : fallbackArea.value;
        try {
          await config.createItem(filename, content);
          toast.success(`${filename} created`);
          if (editor) editor.destroy();
          config.onReload();
        } catch (err) {
          toast.error(err.message);
          return false;
        }
      },
    },
  ], { wide: true });
}
