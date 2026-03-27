/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Chat providers management view (Telegram, etc.).
 */

import { h, clear } from '../../core/dom.js';
import { get, put } from '../../core/http.js';
import { icon } from '../../ui/icon.js';
import * as toast from '../../ui/toast.js';
import * as state from '../../core/state.js';

export async function renderChatProviders(container) {
  clear(container);

  const header = h('div', { className: 'context-panel__header context-panel__header--page' }, [
    h('h3', { className: 'context-panel__title context-panel__title--page' }, 'Chat Providers'),
  ]);

  const body = h('div', { className: 'context-panel__body context-panel__body--page' });

  container.appendChild(header);
  container.appendChild(body);

  try {
    const settings = await get('/admin/api/settings');
    renderProviders(settings);
  } catch (err) {
    toast.error('Failed to load settings: ' + err.message);
  }

  function renderProviders(settings) {
    clear(body);
    const readOnly = !state.isAdmin();

    // --- Telegram ---
    const tokenValue = settings['telegram_bot_token'] || '';
    const usersValue = settings['telegram_allowed_users'] || '';
    const isConnected = !!tokenValue;

    const tokenInput = h('input', {
      className: 'input',
      type: 'password',
      value: tokenValue,
      placeholder: 'Paste token from @BotFather',
      disabled: readOnly,
    });

    const toggleBtn = h('button', {
      className: 'btn btn--ghost btn--sm',
      style: { marginTop: '4px' },
      onClick: () => {
        if (tokenInput.type === 'password') {
          tokenInput.type = 'text';
          toggleBtn.textContent = 'Hide';
        } else {
          tokenInput.type = 'password';
          toggleBtn.textContent = 'Show';
        }
      },
    }, 'Show');

    const usersInput = h('input', {
      className: 'input',
      value: usersValue,
      placeholder: '123456789, 987654321',
      disabled: readOnly,
    });

    const statusBadge = isConnected
      ? h('span', { className: 'badge badge--success' }, 'Connected')
      : h('span', { className: 'badge badge--secondary' }, 'Not configured');

    const saveBtn = h('button', {
      className: 'btn btn--primary',
      onClick: async () => {
        const token = tokenInput.value.trim();
        const users = usersInput.value.trim();

        // Warn if token set but no allowed users
        if (token && !users) {
          toast.warning('Allowed users is required when a bot token is set — the bot will deny all access without it.');
          usersInput.classList.add('input--error');
          return;
        }
        usersInput.classList.remove('input--error');

        saveBtn.disabled = true;
        saveBtn.textContent = 'Saving...';
        try {
          const payload = {};
          if (token) payload['telegram_bot_token'] = token;
          if (users) payload['telegram_allowed_users'] = users;
          await put('/admin/api/settings', payload);
          toast.success('Telegram settings saved');

          // Reload to reflect new status
          const updated = await get('/admin/api/settings');
          renderProviders(updated);
        } catch (err) {
          toast.error('Failed to save: ' + err.message);
        }
        saveBtn.disabled = false;
        saveBtn.textContent = 'Save';
      },
    }, 'Save');

    const telegramCard = h('div', { className: 'card' }, [
      h('div', { className: 'card__header' }, [
        h('div', { className: 'flex items-center gap-3' }, [
          h('span', { innerHTML: icon('send') }),
          h('div', {}, [
            h('h4', { className: 'card__title' }, 'Telegram'),
            h('span', { className: 'text-xs text-secondary' }, 'Control HO via Telegram bot'),
          ]),
        ]),
        statusBadge,
      ]),
      h('div', { className: 'mt-4' }, [
        h('div', { className: 'form-group' }, [
          h('label', {}, ['Bot Token', h('span', { className: 'required' }, ' *')]),
          h('div', { className: 'flex items-center gap-2' }, [tokenInput, toggleBtn]),
          h('p', { className: 'form-hint' }, 'Create a bot via @BotFather on Telegram, then paste the token here.'),
        ]),
        h('div', { className: 'form-group' }, [
          h('label', {}, ['Allowed Users', h('span', { className: 'required' }, ' *')]),
          usersInput,
          h('p', { className: 'form-hint' }, 'Comma-separated Telegram user IDs. Send /start to the bot to see your ID. The bot denies access if this is empty.'),
        ]),
        ...(state.isAdmin() ? [h('div', { className: 'mt-4' }, [saveBtn])] : []),
      ]),
    ]);

    body.appendChild(telegramCard);
  }
}
