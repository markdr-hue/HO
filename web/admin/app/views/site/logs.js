/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * LLM request/response logs with stats, filtering, color-coding, and CSV export.
 */

import { h, clear } from '../../core/dom.js';
import { get } from '../../core/http.js';
import { icon } from '../../ui/icon.js';
import * as toast from '../../ui/toast.js';
import * as state from '../../core/state.js';
import { emptyState, formatNum } from '../../ui/helpers.js';
import { safeJsonParse } from '../../core/utils.js';
import { getToken } from '../../core/http.js';

export async function renderSiteLogs(container, siteId) {
  clear(container);

  let currentSource = '';
  let currentModel = '';
  let errorsOnly = false;
  let currentLimit = 50;
  let currentOffset = 0;
  let expandedRowId = null;

  // --- Stats bar ---
  const statsContainer = h('div', { className: 'llm-log-stats' });

  // --- Toolbar ---
  const sourceSelect = h('select', {
    className: 'input',
    style: { maxWidth: '130px' },
    onChange: () => { currentSource = sourceSelect.value; currentOffset = 0; loadLogs(); },
  }, [
    h('option', { value: '' }, 'All Sources'),
    h('option', { value: 'brain' }, 'Brain'),
    h('option', { value: 'chat' }, 'Chat'),
  ]);

  const modelSelect = h('select', {
    className: 'input',
    style: { maxWidth: '200px' },
    onChange: () => { currentModel = modelSelect.value; currentOffset = 0; loadLogs(); },
  }, [
    h('option', { value: '' }, 'All Models'),
  ]);

  const errorsCheckbox = h('input', {
    type: 'checkbox',
    onChange: () => { errorsOnly = errorsCheckbox.checked; currentOffset = 0; loadLogs(); },
  });

  const csvBtn = h('button', {
    className: 'btn btn--sm',
    onClick: exportCSV,
  }, [
    h('span', { innerHTML: icon('download') }),
    'Export CSV',
  ]);

  const refreshBtn = h('button', {
    className: 'btn btn--sm',
    onClick: () => { loadStats(); loadLogs(); },
  }, [
    h('span', { innerHTML: icon('loader') }),
    'Refresh',
  ]);

  const toolbar = h('div', { className: 'llm-log-toolbar' }, [
    sourceSelect,
    modelSelect,
    h('label', { className: 'flex items-center gap-1 text-xs' }, [
      errorsCheckbox,
      'Errors only',
    ]),
    h('div', { style: { flex: '1' } }),
    csvBtn,
    refreshBtn,
  ]);

  // --- Table + pagination ---
  const logsContainer = h('div');
  const paginationContainer = h('div', { className: 'llm-log-pagination' });

  const header = h('div', { className: 'context-panel__header flex items-center justify-between' }, [
    h('h3', { className: 'context-panel__title' }, 'LLM Logs'),
  ]);

  const body = h('div', { className: 'context-panel__body' }, [
    statsContainer,
    toolbar,
    logsContainer,
    paginationContainer,
  ]);

  container.appendChild(header);
  container.appendChild(body);

  // --- Load stats ---
  async function loadStats() {
    try {
      const stats = await get(`/admin/api/logs/${siteId}/llm/stats`);
      renderStats(stats);
      populateModelFilter(stats.by_model || []);
    } catch {
      // stats not critical
    }
  }

  function renderStats(stats) {
    clear(statsContainer);
    const cards = [
      { label: 'Total Calls', value: formatNum(stats.total_calls) },
      { label: 'Input Tokens', value: formatNum(stats.total_input_tokens) },
      { label: 'Output Tokens', value: formatNum(stats.total_output_tokens) },
      { label: 'Errors', value: formatNum(stats.total_errors), danger: stats.total_errors > 0 },
    ];
    cards.forEach(c => {
      const cls = 'llm-log-stat-card' + (c.danger ? ' llm-log-stat-card--danger' : '');
      statsContainer.appendChild(
        h('div', { className: cls }, [
          h('div', { className: 'llm-log-stat-card__value' }, c.value),
          h('div', { className: 'llm-log-stat-card__label' }, c.label),
        ])
      );
    });
  }

  function populateModelFilter(models) {
    // Keep current selection
    const cur = modelSelect.value;
    while (modelSelect.options.length > 1) modelSelect.remove(1);
    models.forEach(m => {
      const opt = h('option', { value: m.model }, m.model);
      modelSelect.appendChild(opt);
    });
    modelSelect.value = cur;
  }

  // --- Load logs ---
  async function loadLogs() {
    try {
      let url = `/admin/api/logs/${siteId}/llm?limit=${currentLimit}&offset=${currentOffset}`;
      if (currentSource) url += `&source=${encodeURIComponent(currentSource)}`;
      if (currentModel) url += `&model=${encodeURIComponent(currentModel)}`;
      if (errorsOnly) url += `&has_error=true`;
      const logs = await get(url);
      renderLogs(logs);
      renderPagination(logs.length);
    } catch (err) {
      toast.error('Failed to load logs: ' + err.message);
    }
  }

  function renderLogs(logs) {
    clear(logsContainer);
    expandedRowId = null;

    if (logs.length === 0 && currentOffset === 0) {
      logsContainer.appendChild(emptyState('No LLM logs yet. Logs will appear once the brain or chat makes LLM calls.'));
      return;
    }

    const tbody = h('tbody');
    logs.forEach(log => {
      const row = createLogRow(log, tbody);
      tbody.appendChild(row);
    });

    logsContainer.appendChild(
      h('div', { className: 'table-wrapper' }, [
        h('table', { className: 'table' }, [
          h('thead', {}, [
            h('tr', {}, [
              h('th', {}, 'Time'),
              h('th', {}, 'Source'),
              h('th', {}, 'Model'),
              h('th', {}, 'Iter'),
              h('th', {}, 'In Tokens'),
              h('th', {}, 'Out Tokens'),
              h('th', {}, 'Duration'),
              h('th', {}, 'Status'),
            ]),
          ]),
          tbody,
        ]),
      ])
    );
  }

  function createLogRow(log, tbody) {
    const hasError = !!log.error_message;
    const stopReason = log.response_stop_reason || '';
    const isMaxTokens = stopReason === 'max_tokens';

    let rowClass = 'llm-log-row';
    if (hasError) rowClass += ' llm-log-row--error';
    else if (isMaxTokens) rowClass += ' llm-log-row--warning';

    const time = new Date(log.created_at).toLocaleString();
    const duration = log.duration_ms ? `${(log.duration_ms / 1000).toFixed(1)}s` : '--';

    // Source badge
    const sourceBadge = log.source === 'brain'
      ? h('span', { className: 'badge badge--accent' }, 'brain')
      : h('span', { className: 'badge badge--info' }, 'chat');

    // Status badge
    let statusBadge;
    if (hasError) {
      const errText = log.error_message.length > 40
        ? log.error_message.slice(0, 40) + '...'
        : log.error_message;
      statusBadge = h('span', { className: 'badge badge--danger' }, errText);
    } else if (stopReason === 'end_turn') {
      statusBadge = h('span', { className: 'badge badge--success' }, 'end_turn');
    } else if (stopReason === 'tool_use') {
      statusBadge = h('span', { className: 'badge badge--warning' }, 'tool_use');
    } else if (isMaxTokens) {
      statusBadge = h('span', { className: 'badge badge--danger' }, 'max_tokens');
    } else if (stopReason) {
      statusBadge = h('span', { className: 'badge' }, stopReason);
    } else {
      statusBadge = h('span', { className: 'text-secondary' }, '--');
    }

    const row = h('tr', {
      className: rowClass,
      onClick: () => toggleDetail(log.id, row, tbody),
    }, [
      h('td', {}, h('span', { className: 'text-xs text-secondary' }, time)),
      h('td', {}, sourceBadge),
      h('td', {}, h('span', { className: 'text-xs' }, log.model || '--')),
      h('td', {}, String(log.iteration)),
      h('td', {}, formatNum(log.input_tokens)),
      h('td', {}, formatNum(log.output_tokens)),
      h('td', {}, duration),
      h('td', {}, statusBadge),
    ]);

    return row;
  }

  async function toggleDetail(logId, row, tbody) {
    // If already expanded, collapse
    const existingDetail = row.nextElementSibling;
    if (existingDetail && existingDetail.dataset.detailFor === String(logId)) {
      existingDetail.remove();
      expandedRowId = null;
      return;
    }

    // Collapse any open detail
    const openDetail = tbody.querySelector('[data-detail-for]');
    if (openDetail) openDetail.remove();

    // Fetch full detail
    try {
      const detail = await get(`/admin/api/logs/${siteId}/llm/${logId}`);
      const detailRow = createDetailRow(detail);
      row.after(detailRow);
      expandedRowId = logId;
    } catch (err) {
      toast.error('Failed to load log detail: ' + err.message);
    }
  }

  function createDetailRow(detail) {
    const colCount = 8;
    const cell = h('td', { colSpan: String(colCount), className: 'llm-log-detail' });

    // System prompt section
    if (detail.request_system) {
      cell.appendChild(createCollapsibleSection('System Prompt', detail.request_system, true));
    }

    // Messages section
    if (detail.request_messages) {
      try {
        const msgs = JSON.parse(detail.request_messages);
        const section = h('div', { className: 'llm-log-detail__section' }, [
          h('div', { className: 'llm-log-detail__section-title' }, `Messages (${msgs.length})`),
        ]);
        msgs.forEach(msg => {
          const roleClass = `llm-log-msg llm-log-msg--${msg.role || 'user'}`;
          const content = msg.content || '';
          const displayContent = content.length > 2000
            ? content.slice(0, 2000) + '\n... (truncated)'
            : content;
          section.appendChild(
            h('div', { className: roleClass }, [
              h('div', { className: 'llm-log-msg__role' }, msg.role || 'unknown'),
              h('div', { className: 'llm-log-msg__content' }, displayContent),
            ])
          );
        });
        cell.appendChild(section);
      } catch {
        cell.appendChild(createCollapsibleSection('Messages (raw)', detail.request_messages, false));
      }
    }

    // Response content section
    if (detail.response_content) {
      cell.appendChild(createCollapsibleSection('Response Content', detail.response_content, false));
    }

    // Tool calls section
    if (detail.response_tool_calls) {
      try {
        const toolCalls = JSON.parse(detail.response_tool_calls);
        if (toolCalls.length > 0) {
          const section = h('div', { className: 'llm-log-detail__section' }, [
            h('div', { className: 'llm-log-detail__section-title' }, `Tool Calls (${toolCalls.length})`),
          ]);
          toolCalls.forEach(tc => {
            let argsFormatted = tc.arguments || '{}';
            const parsed = safeJsonParse(argsFormatted);
            if (parsed) argsFormatted = JSON.stringify(parsed, null, 2);
            section.appendChild(
              h('div', { className: 'llm-log-msg llm-log-msg--tool' }, [
                h('div', { className: 'llm-log-msg__role' }, `${tc.name} (${tc.id})`),
                h('pre', {}, argsFormatted),
              ])
            );
          });
          cell.appendChild(section);
        }
      } catch {
        cell.appendChild(createCollapsibleSection('Tool Calls (raw)', detail.response_tool_calls, false));
      }
    }

    // Tools available
    if (detail.request_tools) {
      const section = h('div', { className: 'llm-log-detail__section' }, [
        h('div', { className: 'llm-log-detail__section-title' }, 'Available Tools'),
        h('div', { className: 'text-xs text-secondary' }, detail.request_tools),
      ]);
      cell.appendChild(section);
    }

    const detailRow = h('tr', { dataset: { detailFor: String(detail.id) } }, [cell]);
    return detailRow;
  }

  function createCollapsibleSection(title, content, startCollapsed) {
    const section = h('div', { className: 'llm-log-detail__section' });
    const pre = h('pre', {}, content);

    if (startCollapsed && content.length > 500) {
      pre.style.maxHeight = '100px';
      pre.style.overflow = 'hidden';
      const toggle = h('span', {
        className: 'llm-log-detail__toggle',
        onClick: (e) => {
          e.stopPropagation();
          if (pre.style.maxHeight === '100px') {
            pre.style.maxHeight = '400px';
            pre.style.overflow = 'auto';
            toggle.textContent = '[collapse]';
          } else {
            pre.style.maxHeight = '100px';
            pre.style.overflow = 'hidden';
            toggle.textContent = '[expand]';
          }
        },
      }, '[expand]');
      section.appendChild(
        h('div', { className: 'llm-log-detail__section-title' }, [title, toggle])
      );
    } else {
      section.appendChild(h('div', { className: 'llm-log-detail__section-title' }, title));
    }

    section.appendChild(pre);
    return section;
  }

  // --- Pagination ---
  function renderPagination(resultCount) {
    clear(paginationContainer);
    const page = Math.floor(currentOffset / currentLimit) + 1;

    if (currentOffset > 0) {
      paginationContainer.appendChild(
        h('button', {
          className: 'btn btn--sm btn--ghost',
          onClick: () => { currentOffset = Math.max(0, currentOffset - currentLimit); loadLogs(); },
        }, 'Previous')
      );
    }

    paginationContainer.appendChild(
      h('span', { className: 'text-xs text-secondary' }, `Page ${page}`)
    );

    if (resultCount >= currentLimit) {
      paginationContainer.appendChild(
        h('button', {
          className: 'btn btn--sm btn--ghost',
          onClick: () => { currentOffset += currentLimit; loadLogs(); },
        }, 'Next')
      );
    }
  }

  // --- CSV export ---
  function exportCSV() {
    let url = `/admin/api/logs/${siteId}/llm/csv?limit=50000`;
    if (currentSource) url += `&source=${encodeURIComponent(currentSource)}`;
    if (currentModel) url += `&model=${encodeURIComponent(currentModel)}`;
    if (errorsOnly) url += `&has_error=true`;

    // Open in new tab — the backend sets Content-Disposition: attachment
    const token = getToken();
    // Need to add auth header — use fetch + blob download
    fetch(url, {
      headers: { 'Authorization': `Bearer ${token}` },
    })
    .then(res => {
      if (!res.ok) throw new Error('Export failed');
      return res.blob();
    })
    .then(blob => {
      const anchor = document.createElement('a');
      anchor.href = URL.createObjectURL(blob);
      anchor.download = `llm_log_${siteId}.csv`;
      anchor.click();
      URL.revokeObjectURL(anchor.href);
    })
    .catch(err => toast.error('CSV export failed: ' + err.message));
  }

  // --- Real-time SSE watchers ---
  let statsTimer = null;
  const unwatchTool = state.watch('toolExecuted', (evt) => {
    if (!evt || evt.site_id !== siteId) return;
    if (statsTimer) clearTimeout(statsTimer);
    statsTimer = setTimeout(() => loadStats(), 3000);
  });

  // --- Initialize ---
  loadStats();
  loadLogs();

  return function cleanup() {
    unwatchTool();
    if (statsTimer) clearTimeout(statsTimer);
  };
}
