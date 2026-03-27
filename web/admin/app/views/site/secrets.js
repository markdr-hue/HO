/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Site-scoped secrets panel.
 */

import { h, clear } from '../../core/dom.js';
import { get, post, del } from '../../core/http.js';
import { icon } from '../../ui/icon.js';
import * as toast from '../../ui/toast.js';
import * as modal from '../../ui/modal.js';
import * as state from '../../core/state.js';
import { emptyState } from '../../ui/helpers.js';

export async function renderSiteSecrets(container, siteId) {
  clear(container);

  const header = h('div', { className: 'context-panel__header' }, [
    h('h3', { className: 'context-panel__title' }, 'Secrets'),
  ]);

  const body = h('div', { className: 'context-panel__body' });
  container.appendChild(header);
  container.appendChild(body);

  const unwatch = state.watch('toolExecuted', (evt) => {
    if (evt.site_id === siteId &&
        evt.tool === 'manage_secrets' && ['store', 'delete'].includes(evt.args?.action) &&
        evt.success) {
      loadSecrets(body, siteId);
    }
  });

  await loadSecrets(body, siteId);
  return () => unwatch();
}

async function loadSecrets(container, siteId) {
  try {
    const secrets = await get(`/admin/api/sites/${siteId}/secrets`);
    renderSecretsList(container, secrets, siteId);
  } catch (err) {
    toast.error('Failed to load secrets: ' + err.message);
  }
}

function renderSecretsList(container, secrets, siteId) {
  clear(container);

  container.appendChild(h('div', { className: 'flex items-center gap-2 mb-3' }, [
    h('button', {
      className: 'btn btn--primary btn--sm',
      onClick: () => showCreateSecretModal(container, siteId),
    }, [h('span', { innerHTML: icon('plus') }), ' Add Secret']),
    h('span', {
      className: 'text-sm text-secondary',
      style: { padding: '8px 12px', background: 'var(--bg-secondary)', borderRadius: '6px' },
    }, 'Values are encrypted. Only the AI can read them.'),
  ]));

  if (!secrets || secrets.length === 0) {
    container.appendChild(emptyState('No secrets stored yet.'));
    return;
  }

  for (const s of secrets) {
    const card = h('div', { className: 'card mb-3' }, [
      h('div', { className: 'card__header' }, [
        h('div', { className: 'flex items-center gap-2' }, [
          h('span', { innerHTML: icon('lock') }),
          h('h4', { className: 'card__title' }, s.name),
        ]),
        h('button', {
          className: 'btn btn--ghost btn--sm',
          title: 'Delete secret',
          onClick: () => {
            modal.confirmDanger('Delete Secret', `Delete secret "${s.name}"? The AI will no longer be able to access it.`, async () => {
              try {
                await del(`/admin/api/sites/${siteId}/secrets/${s.id}`);
                toast.success('Secret deleted');
                loadSecrets(container, siteId);
              } catch (err) {
                toast.error(err.message);
              }
            });
          },
        }, [h('span', { innerHTML: icon('trash') })]),
      ]),
      h('div', { className: 'text-xs text-secondary', style: { padding: '0 12px 12px' } },
        'Updated: ' + new Date(s.updated_at).toLocaleString()),
    ]);
    container.appendChild(card);
  }
}

function showCreateSecretModal(container, siteId) {
  const nameInput = h('input', { className: 'input', type: 'text', placeholder: 'e.g. stripe_api_key' });
  const valueInput = h('input', { className: 'input', type: 'password', placeholder: 'Secret value' });
  const toggleBtn = h('button', {
    className: 'btn btn--ghost btn--sm',
    type: 'button',
    onClick: () => {
      valueInput.type = valueInput.type === 'password' ? 'text' : 'password';
      toggleBtn.textContent = valueInput.type === 'password' ? 'Show' : 'Hide';
    },
  }, 'Show');

  const form = h('div', {}, [
    h('div', { className: 'form-group' }, [h('label', {}, 'Name'), nameInput]),
    h('div', { className: 'form-group' }, [
      h('label', {}, 'Value'),
      h('div', { className: 'flex gap-2' }, [valueInput, toggleBtn]),
    ]),
  ]);

  modal.show('Add Secret', form, [
    { label: 'Cancel', onClick: () => {} },
    {
      label: 'Store',
      className: 'btn btn--primary',
      onClick: async () => {
        const name = nameInput.value.trim();
        const value = valueInput.value;
        if (!name || !value) {
          toast.error('Name and value are required');
          return false;
        }
        try {
          await post(`/admin/api/sites/${siteId}/secrets`, { name, value });
          toast.success('Secret stored');
          loadSecrets(container, siteId);
        } catch (err) {
          toast.error(err.message);
          return false;
        }
      },
    },
  ]);
}
