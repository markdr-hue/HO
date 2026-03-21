/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Dynamic tables manager view for context panel.
 * Supports listing tables, viewing rows, and full CRUD operations.
 */

import { h, clear } from '../../core/dom.js';
import { get, post, put, del, getToken } from '../../core/http.js';
import { icon } from '../../ui/icon.js';
import * as toast from '../../ui/toast.js';
import * as modal from '../../ui/modal.js';
import * as state from '../../core/state.js';
import { emptyState } from '../../ui/helpers.js';
import { safeJsonParse } from '../../core/utils.js';

export async function renderSiteTables(container, siteId) {
  clear(container);

  const header = h('div', { className: 'context-panel__header' }, [
    h('h3', { className: 'context-panel__title' }, 'Database Tables'),
  ]);

  const listContainer = h('div', { className: 'context-panel__body' });
  container.appendChild(header);
  container.appendChild(listContainer);

  const unwatch = state.watch('toolExecuted', (evt) => {
    if (evt.site_id === siteId &&
        ['create_table', 'insert_row', 'update_row', 'delete_row'].includes(evt.tool) &&
        evt.success) {
      loadAndRenderTables(listContainer, siteId);
    }
  });

  await loadAndRenderTables(listContainer, siteId);
  return () => unwatch();
}

async function loadAndRenderTables(container, siteId) {
  try {
    const tables = await get(`/admin/api/sites/${siteId}/tables`);
    renderTablesList(container, tables, siteId);
  } catch (err) {
    toast.error('Failed to load tables: ' + err.message);
  }
}

function renderTablesList(container, tables, siteId) {
  clear(container);

  if (!tables || tables.length === 0) {
    container.appendChild(emptyState('No dynamic tables created yet.'));
    return;
  }

  for (const table of tables) {
    const card = h('div', { className: 'card mb-3', style: { cursor: 'pointer' } }, [
      h('div', { className: 'card__header' }, [
        h('div', { className: 'flex items-center gap-2' }, [
          h('span', { innerHTML: icon('database') }),
          h('strong', {}, table.table_name),
        ]),
        h('span', { className: 'badge badge--info' }, `${table.row_count} rows`),
      ]),
    ]);

    card.addEventListener('click', () => showTableRows(container, table, siteId));
    container.appendChild(card);
  }
}

async function showTableRows(container, table, siteId) {
  clear(container);

  const back = h('button', {
    className: 'btn btn--ghost btn--sm mb-3',
    onClick: () => loadAndRenderTables(container, siteId),
  }, [
    h('span', { innerHTML: icon('chevron-right'), style: { transform: 'rotate(180deg)', display: 'inline-flex' } }),
    ` Back to tables`,
  ]);

  const headerRow = h('div', { className: 'flex items-center justify-between mb-3' }, [
    h('h4', {}, table.table_name),
    h('div', { className: 'flex gap-2' }, [
      h('button', {
        className: 'btn btn--sm btn--secondary',
        title: 'Export as CSV',
        onClick: async () => {
          try {
            const token = getToken();
            const res = await fetch(`/admin/api/sites/${siteId}/tables/${table.table_name}/csv`, {
              headers: token ? { 'Authorization': `Bearer ${token}` } : {},
            });
            if (!res.ok) throw new Error(`Export failed (${res.status})`);
            const blob = await res.blob();
            const url = URL.createObjectURL(blob);
            const a = document.createElement('a');
            a.href = url;
            a.download = `${table.table_name}.csv`;
            a.click();
            URL.revokeObjectURL(url);
            toast.success('CSV exported');
          } catch (err) {
            toast.error(err.message);
          }
        },
      }, [h('span', { innerHTML: icon('download') }), ' CSV']),
      h('button', {
        className: 'btn btn--sm btn--secondary',
        title: 'Add column',
        onClick: () => showAddColumnModal(container, siteId, table),
      }, [h('span', { innerHTML: icon('plus') }), ' Column']),
      h('button', {
        className: 'btn btn--primary btn--sm',
        onClick: () => showAddRowModal(container, siteId, table),
      }, [h('span', { innerHTML: icon('plus') }), ' Add Row']),
    ]),
  ]);

  container.appendChild(back);
  container.appendChild(headerRow);

  const dataContainer = h('div');
  container.appendChild(dataContainer);

  await loadRows(dataContainer, siteId, table, 0, 50);
}

async function loadRows(container, siteId, table, offset, limit) {
  try {
    const data = await get(`/admin/api/sites/${siteId}/tables/${table.table_name}/rows?limit=${limit}&offset=${offset}`);
    renderRows(container, data, siteId, table);
  } catch (err) {
    toast.error('Failed to load rows: ' + err.message);
  }
}

function renderRows(container, data, siteId, table) {
  clear(container);

  if (!data.rows || data.rows.length === 0) {
    container.appendChild(h('p', { className: 'text-secondary' }, 'No rows in this table.'));
    return;
  }

  const cols = data.columns || [];
  const secureCols = data.secure_columns || {};

  const tableEl = h('table', { className: 'table table--compact' });
  const thead = h('thead', {}, [
    h('tr', {}, [
      ...cols.map(col => h('th', {}, col)),
      h('th', { style: { width: '100px' } }, 'Actions'),
    ]),
  ]);
  tableEl.appendChild(thead);

  const tbody = h('tbody');
  for (const row of data.rows) {
    const tr = h('tr', {}, [
      ...cols.map(col => {
        const val = row[col];
        const display = val === null ? '\u2014' : String(val);
        return h('td', { className: 'text-sm' }, display.length > 80 ? display.substring(0, 80) + '...' : display);
      }),
      h('td', {}, [
        h('div', { className: 'flex items-center gap-1' }, [
          h('button', {
            className: 'btn btn--ghost btn--sm',
            title: 'Edit',
            onClick: () => showEditRowModal(container, siteId, table, row, data),
          }, [h('span', { innerHTML: icon('edit') })]),
          h('button', {
            className: 'btn btn--ghost btn--sm',
            title: 'Delete',
            onClick: () => confirmDeleteRow(container, siteId, table, row),
          }, [h('span', { innerHTML: icon('trash') })]),
        ]),
      ]),
    ]);
    tbody.appendChild(tr);
  }
  tableEl.appendChild(tbody);

  container.appendChild(h('div', { style: { overflowX: 'auto' } }, [tableEl]));
  container.appendChild(h('p', { className: 'text-sm text-secondary mt-2' }, `Showing ${data.rows.length} of ${data.total} rows`));
}

function buildRowForm(schema, secureCols, existingData) {
  const fields = [];
  if (!schema) return fields;

  for (const [colName, colType] of Object.entries(schema)) {
    const isPassword = secureCols && secureCols[colName] === 'hash';
    const isEncrypted = secureCols && secureCols[colName] === 'encrypt';
    const upperType = (colType || '').toUpperCase();

    let input;
    if (upperType === 'BOOLEAN') {
      input = h('input', {
        className: 'input',
        type: 'checkbox',
        name: colName,
      });
      if (existingData) input.checked = !!existingData[colName];
    } else {
      let inputType = 'text';
      if (upperType === 'INTEGER' || upperType === 'REAL') inputType = 'number';
      if (isPassword) inputType = 'password';

      const currentValue = existingData ? existingData[colName] : '';

      input = h('input', {
        className: 'input',
        type: inputType,
        name: colName,
        value: (isPassword || isEncrypted) ? '' : (currentValue != null ? String(currentValue) : ''),
        placeholder: (isPassword || isEncrypted)
          ? (existingData ? '(leave blank to keep current)' : '')
          : '',
      });
    }

    let label = colName;
    if (isPassword) label += ' (password)';
    if (isEncrypted) label += ' (encrypted)';

    fields.push(h('div', { className: 'form-group' }, [
      h('label', {}, label),
      input,
    ]));
  }
  return fields;
}

function collectFormData(form, schema, secureCols) {
  const data = {};
  if (!schema) return data;

  for (const [colName, colType] of Object.entries(schema)) {
    const isPassword = secureCols && secureCols[colName] === 'hash';
    const isEncrypted = secureCols && secureCols[colName] === 'encrypt';
    const upperType = (colType || '').toUpperCase();

    const input = form.querySelector(`[name="${colName}"]`);
    if (!input) continue;

    if (upperType === 'BOOLEAN') {
      data[colName] = input.checked ? 1 : 0;
    } else if (isPassword || isEncrypted) {
      if (input.value !== '') {
        data[colName] = input.value;
      }
    } else if (input.value !== '') {
      data[colName] = (upperType === 'INTEGER' || upperType === 'REAL')
        ? Number(input.value) : input.value;
    }
  }
  return data;
}

function showAddRowModal(container, siteId, table) {
  const schema = parseSchema(table.schema_def);
  const secureCols = parseSchema(table.secure_columns);
  const fields = buildRowForm(schema, secureCols, null);
  const form = h('div', {}, fields);

  modal.show('Add Row', form, [
    { label: 'Cancel', onClick: () => {} },
    {
      label: 'Add',
      className: 'btn btn--primary',
      onClick: async () => {
        const data = collectFormData(form, schema, secureCols);
        if (Object.keys(data).length === 0) {
          toast.error('At least one field is required');
          return false;
        }
        try {
          await post(`/admin/api/sites/${siteId}/tables/${table.table_name}/rows`, { data });
          toast.success('Row added');
          await loadRows(container, siteId, table, 0, 50);
        } catch (err) {
          toast.error(err.message);
          return false;
        }
      },
    },
  ]);
}

function showEditRowModal(container, siteId, table, row, responseData) {
  const schema = responseData.schema || parseSchema(table.schema_def);
  const secureCols = responseData.secure_columns || parseSchema(table.secure_columns);
  const fields = buildRowForm(schema, secureCols, row);
  const form = h('div', {}, fields);

  modal.show('Edit Row', form, [
    { label: 'Cancel', onClick: () => {} },
    {
      label: 'Save',
      className: 'btn btn--primary',
      onClick: async () => {
        const data = collectFormData(form, schema, secureCols);
        if (Object.keys(data).length === 0) {
          toast.error('No changes to save');
          return false;
        }
        try {
          await put(`/admin/api/sites/${siteId}/tables/${table.table_name}/rows/${row.id}`, { data });
          toast.success('Row updated');
          await loadRows(container, siteId, table, 0, 50);
        } catch (err) {
          toast.error(err.message);
          return false;
        }
      },
    },
  ]);
}

function confirmDeleteRow(container, siteId, table, row) {
  modal.show('Delete Row',
    h('p', {}, `Delete row #${row.id}? This cannot be undone.`),
    [
      { label: 'Cancel', onClick: () => {} },
      {
        label: 'Delete',
        className: 'btn btn--danger',
        onClick: async () => {
          try {
            await del(`/admin/api/sites/${siteId}/tables/${table.table_name}/rows/${row.id}`);
            toast.success('Row deleted');
            await loadRows(container, siteId, table, 0, 50);
          } catch (err) {
            toast.error(err.message);
            return false;
          }
        },
      },
    ]
  );
}

function parseSchema(raw) {
  if (typeof raw === 'object' && raw !== null) return raw;
  return safeJsonParse(raw, {});
}

function showAddColumnModal(container, siteId, table) {
  const nameInput = h('input', { className: 'input', placeholder: 'column_name' });
  const typeSelect = h('select', { className: 'input' }, [
    h('option', { value: 'TEXT' }, 'TEXT'),
    h('option', { value: 'INTEGER' }, 'INTEGER'),
    h('option', { value: 'REAL' }, 'REAL'),
    h('option', { value: 'BOOLEAN' }, 'BOOLEAN'),
    h('option', { value: 'PASSWORD' }, 'PASSWORD (bcrypt)'),
    h('option', { value: 'ENCRYPTED' }, 'ENCRYPTED (AES)'),
  ]);
  const notNullCheck = h('input', { type: 'checkbox' });

  const form = h('div', {}, [
    h('div', { className: 'form-group' }, [h('label', {}, 'Column Name'), nameInput]),
    h('div', { className: 'form-group' }, [h('label', {}, 'Type'), typeSelect]),
    h('div', { className: 'form-group' }, [
      h('label', { className: 'flex items-center gap-2' }, [notNullCheck, 'NOT NULL']),
    ]),
  ]);

  modal.show(`Add Column to ${table.table_name}`, form, [
    { label: 'Cancel', onClick: () => {} },
    {
      label: 'Add Column',
      className: 'btn btn--primary',
      onClick: async () => {
        const colName = nameInput.value.trim();
        if (!colName) { toast.error('Column name required'); return false; }
        try {
          await post(`/admin/api/sites/${siteId}/tables/${table.table_name}/columns`, {
            column_name: colName,
            column_type: typeSelect.value,
            not_null: notNullCheck.checked,
          });
          toast.success(`Column "${colName}" added`);
          showTableRows(container, table, siteId);
        } catch (err) {
          toast.error(err.message);
          return false;
        }
      },
    },
  ]);
}
