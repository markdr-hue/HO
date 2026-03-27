/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Site-scoped webhooks panel.
 */

import { h, clear } from '../../core/dom.js';
import { get, post, del } from '../../core/http.js';
import { icon } from '../../ui/icon.js';
import * as toast from '../../ui/toast.js';
import * as modal from '../../ui/modal.js';
import { emptyState } from '../../ui/helpers.js';

export async function renderSiteWebhooks(container, siteId) {
  clear(container);

  const header = h('div', { className: 'context-panel__header' }, [
    h('h3', { className: 'context-panel__title' }, 'Webhooks'),
  ]);

  const body = h('div', { className: 'context-panel__body' });
  container.appendChild(header);
  container.appendChild(body);

  try {
    const webhooks = await get(`/admin/api/sites/${siteId}/webhooks`);

    body.appendChild(h('button', {
      className: 'btn btn--primary btn--sm mb-3',
      onClick: () => showCreateWebhookModal(container, siteId),
    }, [h('span', { innerHTML: icon('plus') }), ' Create Webhook']));

    if (webhooks.length === 0) {
      body.appendChild(emptyState('No webhooks configured.'));
      return;
    }

    for (const wh of webhooks) {
      const dirBadge = wh.direction === 'outgoing'
        ? h('span', { className: 'badge badge--info' }, 'Outgoing')
        : h('span', { className: 'badge badge--success' }, 'Incoming');

      const enabledBadge = wh.is_enabled
        ? h('span', { className: 'badge badge--success' }, 'Enabled')
        : h('span', { className: 'badge badge--danger' }, 'Disabled');

      const details = [];

      if (wh.url) {
        details.push(h('div', { className: 'text-sm text-secondary mt-2' }, [
          h('strong', {}, 'URL: '),
          h('code', { style: { fontSize: 'var(--text-xs)', wordBreak: 'break-all' } }, wh.url),
        ]));
      }

      if (wh.secret) {
        details.push(h('div', { className: 'text-sm text-secondary mt-1' }, [
          h('strong', {}, 'Secret: '),
          h('code', { style: { fontSize: 'var(--text-xs)' } }, wh.secret.slice(0, 8) + '...'),
        ]));
      }

      if (wh.last_triggered) {
        details.push(h('div', { className: 'text-xs text-secondary mt-1' },
          'Last triggered: ' + new Date(wh.last_triggered).toLocaleString()));
      }

      const actions = h('div', { className: 'flex gap-2 mt-3' }, [
        h('button', {
          className: 'btn btn--sm btn--secondary',
          onClick: () => showWebhookDetail(siteId, wh),
        }, 'Details'),
        h('button', {
          className: `btn btn--sm ${wh.is_enabled ? 'btn--warning' : 'btn--success'}`,
          onClick: async () => {
            try {
              await post(`/admin/api/sites/${siteId}/webhooks/${wh.id}/toggle`);
              toast.success(wh.is_enabled ? 'Disabled' : 'Enabled');
              renderSiteWebhooks(container, siteId);
            } catch (err) {
              toast.error('Failed to toggle webhook: ' + err.message);
            }
          },
        }, wh.is_enabled ? 'Disable' : 'Enable'),
        h('button', {
          className: 'btn btn--sm btn--danger',
          onClick: () => {
            modal.confirmDanger('Delete Webhook', `Delete webhook "${wh.name}"?`, async () => {
              try {
                await del(`/admin/api/sites/${siteId}/webhooks/${wh.id}`);
                toast.success('Deleted');
                renderSiteWebhooks(container, siteId);
              } catch (err) {
                toast.error('Failed to delete webhook: ' + err.message);
              }
            });
          },
        }, 'Delete'),
      ]);

      body.appendChild(h('div', { className: 'card mb-3' }, [
        h('div', { className: 'card__header' }, [
          h('div', { className: 'flex items-center gap-2' }, [
            h('span', { innerHTML: icon('link') }),
            h('h4', { className: 'card__title' }, wh.name),
          ]),
          h('div', { className: 'flex gap-2' }, [dirBadge, enabledBadge]),
        ]),
        ...details,
        actions,
      ]));
    }
  } catch (err) {
    toast.error('Failed to load webhooks: ' + err.message);
  }
}

async function showWebhookDetail(siteId, wh) {
  const content = h('div', {}, [h('p', { className: 'text-secondary' }, 'Loading...')]);
  modal.show(`Webhook: ${wh.name}`, content, [{ label: 'Close', onClick: () => {} }]);

  try {
    const detail = await get(`/admin/api/sites/${siteId}/webhooks/${wh.id}`);
    clear(content);

    // Subscriptions
    if (detail.subscriptions && detail.subscriptions.length > 0) {
      content.appendChild(h('div', { className: 'mb-3' }, [
        h('p', { className: 'text-sm', style: { fontWeight: 600, marginBottom: '4px' } }, 'Subscribed Events'),
        h('div', { className: 'flex gap-1', style: { flexWrap: 'wrap' } },
          detail.subscriptions.map(s => h('span', { className: 'badge badge--info' }, s))),
      ]));
    }

    // Logs
    const logs = detail.logs || [];
    content.appendChild(h('p', { className: 'text-sm', style: { fontWeight: 600, marginBottom: '4px' } }, `Recent Logs (${logs.length})`));
    if (logs.length === 0) {
      content.appendChild(h('p', { className: 'text-secondary text-sm' }, 'No delivery logs yet.'));
    } else {
      for (const log of logs) {
        const statusBadge = log.success
          ? h('span', { className: 'badge badge--success' }, `${log.status_code || 'OK'}`)
          : h('span', { className: 'badge badge--danger' }, `${log.status_code || 'Error'}`);
        content.appendChild(h('div', {
          style: { padding: '4px 8px', background: 'var(--bg-secondary)', borderRadius: '4px', marginBottom: '2px' },
          className: 'flex items-center justify-between',
        }, [
          h('div', { className: 'flex items-center gap-2' }, [
            statusBadge,
            h('span', { className: 'text-xs' }, log.event_type),
          ]),
          h('span', { className: 'text-xs text-secondary' },
            new Date(log.created_at).toLocaleString()),
        ]));
      }
    }

    // Test button for outgoing webhooks.
    if (detail.direction === 'outgoing' && detail.url) {
      const testBtn = h('button', {
        className: 'btn btn--sm btn--secondary mt-3',
        onClick: async (e) => {
          e.target.disabled = true;
          e.target.textContent = 'Sending...';
          try {
            const res = await fetch(detail.url, {
              method: 'POST',
              headers: { 'Content-Type': 'application/json' },
              body: JSON.stringify({ event: 'test', webhook: detail.name, timestamp: new Date().toISOString() }),
            });
            toast.success(`Test sent \u2014 response: ${res.status}`);
          } catch (err) {
            toast.error('Test failed: ' + err.message);
          } finally {
            e.target.disabled = false;
            e.target.textContent = 'Send Test';
          }
        },
      }, 'Send Test');
      content.appendChild(testBtn);
    }

    // cURL example for incoming
    if (detail.direction === 'incoming') {
      content.appendChild(h('div', { className: 'mt-3' }, [
        h('p', { className: 'text-sm', style: { fontWeight: 600, marginBottom: '4px' } }, 'cURL Example'),
        h('pre', {
          style: { fontSize: 'var(--text-xs)', padding: '8px', background: 'var(--bg-secondary)', borderRadius: '4px', overflow: 'auto' },
        }, `curl -X POST \\
  -H "Content-Type: application/json" \\
  -H "X-Webhook-Signature: sha256=<hmac>" \\
  -d '{"event":"test"}' \\
  ${window.location.protocol}//${window.location.hostname}/webhooks/${detail.name}`),
      ]));
    }
  } catch (err) {
    clear(content);
    content.appendChild(h('p', { className: 'text-danger' }, 'Failed to load details: ' + err.message));
  }
}

function showCreateWebhookModal(container, siteId) {
  const nameInput = h('input', { className: 'input', placeholder: 'e.g. stripe-webhook' });
  const dirSelect = h('select', { className: 'input', onChange: () => {
    urlGroup.style.display = dirSelect.value === 'outgoing' ? '' : 'none';
  }}, [
    h('option', { value: 'incoming' }, 'Incoming'),
    h('option', { value: 'outgoing' }, 'Outgoing'),
  ]);
  const urlInput = h('input', { className: 'input', placeholder: 'https://...' });
  const secretInput = h('input', { className: 'input', placeholder: 'HMAC signing secret (optional)' });
  const eventsInput = h('input', { className: 'input', placeholder: 'data.insert, data.update, payment.completed' });
  const urlGroup = h('div', { className: 'form-group', style: { display: 'none' } }, [h('label', {}, 'URL'), urlInput]);

  const form = h('div', {}, [
    h('div', { className: 'form-group' }, [h('label', {}, 'Name'), nameInput]),
    h('div', { className: 'form-group' }, [h('label', {}, 'Direction'), dirSelect]),
    urlGroup,
    h('div', { className: 'form-group' }, [h('label', {}, 'Secret'), secretInput]),
    h('div', { className: 'form-group' }, [h('label', {}, 'Event Subscriptions (comma-separated)'), eventsInput]),
  ]);

  modal.show('Create Webhook', form, [
    { label: 'Cancel', onClick: () => {} },
    {
      label: 'Create',
      className: 'btn btn--primary',
      onClick: async () => {
        const name = nameInput.value.trim();
        if (!name) { toast.error('Name is required'); return false; }
        const events = eventsInput.value.split(',').map(e => e.trim()).filter(Boolean);
        try {
          await post(`/admin/api/sites/${siteId}/webhooks`, {
            name,
            direction: dirSelect.value,
            url: urlInput.value.trim(),
            secret: secretInput.value.trim(),
            events,
          });
          toast.success('Webhook created');
          renderSiteWebhooks(container, siteId);
        } catch (err) {
          toast.error(err.message);
          return false;
        }
      },
    },
  ]);
}
