/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Pages manager view for context panel.
 */

import { h, clear } from '../../core/dom.js';
import { get, del } from '../../core/http.js';
import { icon } from '../../ui/icon.js';
import * as toast from '../../ui/toast.js';
import * as modal from '../../ui/modal.js';
import * as state from '../../core/state.js';
import { emptyState } from '../../ui/helpers.js';

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
    renderPagesList(container, pages, siteId);
  } catch (err) {
    toast.error('Failed to load pages: ' + err.message);
  }
}

function renderPagesList(container, pages, siteId) {
  clear(container);

  if (!pages || pages.length === 0) {
    container.appendChild(emptyState('No pages created yet.'));
    return;
  }

  const table = h('table', { className: 'table' }, [
    h('thead', {}, [
      h('tr', {}, [
        h('th', {}, 'Path'),
        h('th', {}, 'Title'),
        h('th', {}, 'Status'),
        h('th', {}, 'Updated'),
        h('th', {}, ''),
      ]),
    ]),
  ]);

  const tbody = h('tbody');

  for (const page of pages) {
    const statusBadge = page.status === 'published'
      ? h('span', { className: 'badge badge--success' }, 'Published')
      : h('span', { className: 'badge badge--warning' }, page.status || 'Draft');

    const deleteBtn = h('button', {
      className: 'btn btn--ghost btn--sm btn--danger',
      title: 'Delete page',
      onClick: (e) => {
        e.stopPropagation();
        deletePage(container, page, siteId);
      },
    }, [h('span', { innerHTML: icon('trash-2') })]);

    const row = h('tr', { className: 'table__row--clickable', onClick: () => showPagePreview(container, page, siteId) }, [
      h('td', {}, [
        h('code', { className: 'text-sm' }, page.path),
      ]),
      h('td', {}, page.title || '\u2014'),
      h('td', {}, [statusBadge]),
      h('td', { className: 'text-sm text-secondary' }, new Date(page.updated_at).toLocaleDateString()),
      h('td', { className: 'text-right' }, [deleteBtn]),
    ]);
    tbody.appendChild(row);
  }

  table.appendChild(tbody);
  container.appendChild(table);
}

function deletePage(container, page, siteId) {
  modal.confirmDanger('Delete Page', `Delete page "${page.path}"? This can be restored later.`, async () => {
    try {
      await del(`/admin/api/sites/${siteId}/pages/${page.id}`);
      toast.success(`Page "${page.path}" deleted.`);
      loadPages(container, siteId);
    } catch (err) {
      toast.error('Failed to delete page: ' + err.message);
    }
  });
}

async function showPagePreview(container, page, siteId) {
  try {
    const full = await get(`/admin/api/sites/${siteId}/pages/${page.id}`);
    clear(container);

    const back = h('button', {
      className: 'btn btn--ghost btn--sm mb-3',
      onClick: () => renderSitePages(container.parentElement, siteId),
    }, [
      h('span', { innerHTML: icon('chevron-right'), style: { transform: 'rotate(180deg)', display: 'inline-flex' } }),
      ' Back to pages',
    ]);

    const deleteBtn = h('button', {
      className: 'btn btn--danger btn--sm',
      onClick: () => {
        deletePage(container, full, siteId);
        renderSitePages(container.parentElement, siteId);
      },
    }, 'Delete');

    const preview = h('iframe', {
      sandbox: '',
      className: 'page-preview',
      style: { width: '100%', border: 'none', minHeight: '400px', background: '#fff', borderRadius: '6px' },
    });
    preview.srcdoc = full.content || '<p style="opacity:0.5">No content</p>';

    container.appendChild(back);
    container.appendChild(h('div', { className: 'flex items-center gap-2 mb-2' }, [
      h('h4', { style: { flex: '1' } }, full.title || full.path),
      deleteBtn,
    ]));
    container.appendChild(h('div', { className: 'badge mb-3 ' + (full.status === 'published' ? 'badge--success' : 'badge--warning') }, full.status));
    container.appendChild(preview);
  } catch (err) {
    toast.error('Failed to load page: ' + err.message);
  }
}
