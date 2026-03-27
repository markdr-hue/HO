/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * System settings editor.
 */

import { h, clear } from '../../core/dom.js';
import { get, put } from '../../core/http.js';
import * as toast from '../../ui/toast.js';
import * as state from '../../core/state.js';

export async function renderSettings(container) {
  clear(container);

  const header = h('div', { className: 'context-panel__header context-panel__header--page' }, [
    h('h3', { className: 'context-panel__title context-panel__title--page' }, 'System Settings'),
  ]);

  const formContainer = h('div', { className: 'context-panel__body context-panel__body--page' });

  container.appendChild(header);
  container.appendChild(formContainer);

  try {
    const settings = await get('/admin/api/settings');
    renderForm(settings);
  } catch (err) {
    toast.error('Failed to load settings: ' + err.message);
  }

  function renderForm(settings) {
    clear(formContainer);
    const readOnly = !state.isAdmin();

    // Keys managed by the Chat Providers page — exclude from this view
    const chatProviderKeys = new Set(['telegram_bot_token', 'telegram_allowed_users']);
    const otherKeys = Object.keys(settings).filter(k => !chatProviderKeys.has(k));
    const otherFields = {};

    let settingsCard = null;
    if (otherKeys.length > 0) {
      settingsCard = h('div', { className: 'card' }, [
        h('h4', { className: 'card__title mb-4' }, 'Settings'),
      ]);
      for (const key of otherKeys) {
        const input = h('input', {
          className: 'input',
          value: settings[key] || '',
          disabled: readOnly,
        });
        otherFields[key] = input;
        settingsCard.appendChild(
          h('div', { className: 'form-group' }, [
            h('label', {}, key),
            input,
          ])
        );
      }
    }

    // --- Save button ---
    const saveBtn = h('button', {
      className: 'btn btn--primary',
      onClick: async () => {
        saveBtn.disabled = true;
        saveBtn.textContent = 'Saving...';
        try {
          const payload = {};
          for (const [key, input] of Object.entries(otherFields)) {
            const val = input.value.trim();
            if (val) payload[key] = val;
          }
          await put('/admin/api/settings', payload);
          toast.success('Settings saved');
        } catch (err) {
          toast.error('Failed to save: ' + err.message);
        }
        saveBtn.disabled = false;
        saveBtn.textContent = 'Save Settings';
      },
    }, 'Save Settings');

    if (settingsCard) {
      formContainer.appendChild(settingsCard);
    } else {
      formContainer.appendChild(h('div', { className: 'card' }, [
        h('p', { className: 'text-secondary' }, 'No additional settings configured.'),
      ]));
    }

    if (state.isAdmin() && otherKeys.length > 0) {
      formContainer.appendChild(h('div', { className: 'mt-4' }, [saveBtn]));
    }
  }
}
