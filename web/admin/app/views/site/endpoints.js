/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * API & Auth Endpoints manager view for context panel.
 */

import { h, clear } from '../../core/dom.js';
import { get, post, put, del } from '../../core/http.js';
import { icon } from '../../ui/icon.js';
import * as toast from '../../ui/toast.js';
import * as modal from '../../ui/modal.js';
import * as state from '../../core/state.js';
import { emptyState } from '../../ui/helpers.js';
import { safeJsonParse } from '../../core/utils.js';

export async function renderSiteEndpoints(container, siteId) {
  clear(container);

  const header = h('div', { className: 'context-panel__header' }, [
    h('h3', { className: 'context-panel__title' }, 'Endpoints'),
  ]);

  const listContainer = h('div', { className: 'context-panel__body' });
  container.appendChild(header);
  container.appendChild(listContainer);

  const unwatch = state.watch('toolExecuted', (evt) => {
    if (evt.site_id === siteId && evt.tool === 'manage_endpoints' && evt.success) {
      loadEndpoints(listContainer, siteId);
    }
  });

  await loadEndpoints(listContainer, siteId);
  return () => unwatch();
}

async function loadEndpoints(container, siteId) {
  try {
    const [apiEndpoints, authEndpoints] = await Promise.all([
      get(`/admin/api/sites/${siteId}/endpoints`),
      get(`/admin/api/sites/${siteId}/auth-endpoints`),
    ]);
    renderEndpointsList(container, apiEndpoints, authEndpoints, siteId);
  } catch (err) {
    toast.error('Failed to load endpoints: ' + err.message);
  }
}

function renderEndpointsList(container, apiEndpoints, authEndpoints, siteId) {
  clear(container);

  const hasApi = apiEndpoints && apiEndpoints.length > 0;
  const hasAuth = authEndpoints && authEndpoints.length > 0;

  container.appendChild(h('button', {
    className: 'btn btn--primary btn--sm mb-3',
    onClick: () => showCreateEndpointModal(container, siteId),
  }, [h('span', { innerHTML: icon('plus') }), ' Create Endpoint']));

  if (!hasApi && !hasAuth) {
    container.appendChild(emptyState('No endpoints created yet.'));
    return;
  }

  // API Endpoints
  if (hasApi) {
    container.appendChild(h('h4', { className: 'text-sm text-secondary mb-2', style: { padding: '0 4px' } }, 'API Endpoints'));
    for (const ep of apiEndpoints) {
      container.appendChild(renderApiCard(ep, siteId, container));
    }
  }

  // Auth Endpoints
  if (hasAuth) {
    container.appendChild(h('h4', {
      className: 'text-sm text-secondary mb-2',
      style: { padding: '0 4px', marginTop: hasApi ? '16px' : '0' },
    }, 'Auth Endpoints'));
    for (const ep of authEndpoints) {
      container.appendChild(renderAuthCard(ep, siteId, container));
    }
  }
}

function renderApiCard(ep, siteId, container) {
  const methods = safeJsonParse(ep.methods, []);

  const card = h('div', { className: 'card mb-3', style: { cursor: 'pointer' } }, [
    h('div', { className: 'card__header' }, [
      h('div', { className: 'flex items-center gap-2' }, [
        h('span', { innerHTML: icon('zap') }),
        h('code', { style: { fontSize: 'var(--text-sm)' } }, `/api/${ep.path}`),
      ]),
      h('div', { className: 'flex items-center gap-1' }, [
        h('button', {
          className: 'btn btn--ghost btn--sm',
          title: 'Edit endpoint',
          onClick: (e) => {
            e.stopPropagation();
            showEditModal(container, ep, siteId);
          },
        }, [h('span', { innerHTML: icon('edit') })]),
        h('button', {
          className: 'btn btn--ghost btn--sm',
          title: 'Delete endpoint',
          onClick: (e) => {
            e.stopPropagation();
            confirmDeleteApi(container, ep, siteId);
          },
        }, [h('span', { innerHTML: icon('trash') })]),
      ]),
    ]),
    h('div', { style: { padding: '0 12px 12px' } }, [
      h('div', { className: 'flex items-center gap-2 mb-2' }, [
        h('span', { className: 'text-sm text-secondary' }, 'Table:'),
        h('span', { className: 'badge badge--info' }, ep.table_name),
      ]),
      h('div', { className: 'flex items-center gap-2 mb-2' },
        methods.map(m => h('span', { className: 'badge' }, m))
      ),
      h('div', { className: 'flex items-center gap-3 text-sm text-secondary' }, [
        ep.requires_auth
          ? h('span', { className: 'flex items-center gap-1' }, [
              h('span', { innerHTML: icon('lock') }), 'Auth required',
            ])
          : h('span', { className: 'flex items-center gap-1' }, [
              h('span', { innerHTML: icon('globe') }), 'Public',
            ]),
        h('span', {}, `${ep.rate_limit}/min`),
      ]),
    ]),
  ]);

  card.addEventListener('click', () => showEditModal(container, ep, siteId));
  return card;
}

function renderAuthCard(ep, siteId, container) {
  const routes = ep.routes || [];

  return h('div', { className: 'card mb-3' }, [
    h('div', { className: 'card__header' }, [
      h('div', { className: 'flex items-center gap-2' }, [
        h('span', { innerHTML: icon('key') }),
        h('code', { style: { fontSize: 'var(--text-sm)' } }, `/api/${ep.path}`),
        h('span', { className: 'badge badge--warning' }, 'Auth'),
      ]),
      h('button', {
        className: 'btn btn--ghost btn--sm',
        title: 'Delete auth endpoint',
        onClick: (e) => {
          e.stopPropagation();
          confirmDeleteAuth(container, ep, siteId);
        },
      }, [h('span', { innerHTML: icon('trash') })]),
    ]),
    h('div', { style: { padding: '0 12px 12px' } }, [
      h('div', { className: 'flex items-center gap-2 mb-2' }, [
        h('span', { className: 'text-sm text-secondary' }, 'Table:'),
        h('span', { className: 'badge badge--info' }, ep.table_name),
      ]),
      h('div', { className: 'mb-2' },
        routes.map(r => h('div', { className: 'text-sm', style: { fontFamily: 'var(--font-mono)', padding: '2px 0' } }, r))
      ),
      h('div', { className: 'flex items-center gap-3 text-sm text-secondary' }, [
        h('span', {}, `Login: ${ep.username_column}`),
      ]),
    ]),
  ]);
}

const ALL_METHODS = ['GET', 'POST', 'PUT', 'DELETE', 'PATCH'];

function showEditModal(container, ep, siteId) {
  let methods = safeJsonParse(ep.methods, []);

  // Method checkboxes
  const methodChecks = ALL_METHODS.map(m => {
    const cb = h('input', { type: 'checkbox', checked: methods.includes(m) });
    return h('label', { className: 'flex items-center gap-1', style: { marginRight: '12px' } }, [cb, m]);
  });

  const authCheck = h('input', { type: 'checkbox', checked: ep.requires_auth });
  const publicReadCheck = h('input', { type: 'checkbox', checked: ep.public_read });
  const roleInput = h('input', { className: 'input', value: ep.required_role || '', placeholder: 'e.g. admin (empty = any role)' });
  const rateInput = h('input', { className: 'input', type: 'number', value: ep.rate_limit, min: '1' });

  const form = h('div', {}, [
    h('div', { className: 'form-group mb-3' }, [
      h('label', { className: 'text-sm text-secondary' }, 'Path'),
      h('div', { className: 'text-sm', style: { fontFamily: 'var(--font-mono)', padding: '6px 0' } }, `/api/${ep.path}`),
    ]),
    h('div', { className: 'form-group mb-3' }, [
      h('label', { className: 'text-sm text-secondary' }, 'Table'),
      h('div', { className: 'text-sm', style: { padding: '6px 0' } }, ep.table_name),
    ]),
    h('div', { className: 'form-group mb-3' }, [
      h('label', { className: 'text-sm text-secondary mb-1' }, 'Allowed Methods'),
      h('div', { className: 'flex items-center', style: { flexWrap: 'wrap' } }, methodChecks),
    ]),
    h('div', { className: 'form-group mb-3' }, [
      h('label', { className: 'flex items-center gap-2' }, [authCheck, 'Requires Authentication']),
    ]),
    h('div', { className: 'form-group mb-3' }, [
      h('label', { className: 'flex items-center gap-2' }, [publicReadCheck, 'Public Read (GET without auth)']),
    ]),
    h('div', { className: 'form-group mb-3' }, [
      h('label', { className: 'text-sm text-secondary' }, 'Required Role'),
      roleInput,
    ]),
    h('div', { className: 'form-group mb-3' }, [
      h('label', { className: 'text-sm text-secondary' }, 'Rate Limit (requests/min)'),
      rateInput,
    ]),
  ]);

  modal.show(`Edit /api/${ep.path}`, form, [
    { label: 'Cancel', onClick: () => {} },
    {
      label: 'Save',
      className: 'btn btn--primary',
      onClick: async () => {
        const selectedMethods = ALL_METHODS.filter((_, i) =>
          methodChecks[i].querySelector('input').checked
        );
        if (selectedMethods.length === 0) {
          toast.error('Select at least one method');
          return false;
        }

        try {
          await put(`/admin/api/sites/${siteId}/endpoints/${ep.id}`, {
            methods: selectedMethods,
            requires_auth: authCheck.checked,
            public_read: publicReadCheck.checked,
            required_role: roleInput.value.trim(),
            rate_limit: parseInt(rateInput.value) || 60,
          });
          toast.success('Endpoint updated');
          loadEndpoints(container, siteId);
        } catch (err) {
          toast.error(err.message);
          return false;
        }
      },
    },
  ]);
}

function confirmDeleteApi(container, endpoint, siteId) {
  modal.confirmDanger('Delete API Endpoint', `Delete "/api/${endpoint.path}"? This cannot be undone.`, async () => {
    try {
      await del(`/admin/api/sites/${siteId}/endpoints/${endpoint.id}`);
      toast.success('Endpoint deleted');
      loadEndpoints(container, siteId);
    } catch (err) {
      toast.error(err.message);
    }
  });
}

function confirmDeleteAuth(container, endpoint, siteId) {
  modal.confirmDanger('Delete Auth Endpoint', `Delete auth endpoint "/api/${endpoint.path}"? This cannot be undone.`, async () => {
    try {
      await del(`/admin/api/sites/${siteId}/auth-endpoints/${endpoint.id}`);
      toast.success('Auth endpoint deleted');
      loadEndpoints(container, siteId);
    } catch (err) {
      toast.error(err.message);
    }
  });
}

async function showCreateEndpointModal(container, siteId) {
  // Load tables for dropdown.
  let tables = [];
  try {
    tables = await get(`/admin/api/sites/${siteId}/tables`);
  } catch (e) { /* ignore */ }

  const pathInput = h('input', { className: 'input', placeholder: 'e.g. posts, users, products' });
  const tableSelect = h('select', { className: 'input' }, [
    h('option', { value: '' }, '-- Select table --'),
    ...tables.map(t => h('option', { value: t.table_name }, t.table_name)),
  ]);

  const methodChecks = {};
  const methodsRow = h('div', { className: 'flex gap-3' },
    ['GET', 'POST', 'PUT', 'DELETE'].map(m => {
      const cb = h('input', { type: 'checkbox', checked: true });
      methodChecks[m] = cb;
      return h('label', { className: 'flex items-center gap-1 text-sm' }, [cb, m]);
    })
  );

  const authCheck = h('input', { type: 'checkbox' });
  const publicReadCheck = h('input', { type: 'checkbox' });
  const ownerInput = h('input', { className: 'input', placeholder: 'e.g. user_id (optional)' });

  const form = h('div', {}, [
    h('div', { className: 'form-group' }, [h('label', {}, 'Path'), pathInput]),
    h('div', { className: 'form-group' }, [h('label', {}, 'Table'), tableSelect]),
    h('div', { className: 'form-group' }, [h('label', {}, 'Methods'), methodsRow]),
    h('div', { className: 'form-group' }, [h('label', { className: 'flex items-center gap-2' }, [authCheck, 'Requires Auth'])]),
    h('div', { className: 'form-group' }, [h('label', { className: 'flex items-center gap-2' }, [publicReadCheck, 'Public Read (GET without auth)'])]),
    h('div', { className: 'form-group' }, [h('label', {}, 'Owner Column (row-level security)'), ownerInput]),
  ]);

  modal.show('Create API Endpoint', form, [
    { label: 'Cancel', onClick: () => {} },
    {
      label: 'Create',
      className: 'btn btn--primary',
      onClick: async () => {
        const path = pathInput.value.trim();
        const tableName = tableSelect.value;
        if (!path || !tableName) {
          toast.error('Path and table are required');
          return false;
        }
        const methods = Object.entries(methodChecks).filter(([, cb]) => cb.checked).map(([m]) => m);
        if (methods.length === 0) {
          toast.error('Select at least one method');
          return false;
        }
        try {
          await post(`/admin/api/sites/${siteId}/endpoints`, {
            path,
            table_name: tableName,
            methods,
            requires_auth: authCheck.checked,
            public_read: publicReadCheck.checked,
            owner_column: ownerInput.value.trim(),
          });
          toast.success('Endpoint created');
          loadEndpoints(container, siteId);
        } catch (err) {
          toast.error(err.message);
          return false;
        }
      },
    },
  ]);
}
