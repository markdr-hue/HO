/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Site settings tab - name, domain, mode, prompts, model, danger zone.
 */

import { h, clear } from '../../core/dom.js';
import { get, post, put, del } from '../../core/http.js';
import { navigate } from '../../core/router.js';
import { icon } from '../../ui/icon.js';
import * as toast from '../../ui/toast.js';
import * as modal from '../../ui/modal.js';
import * as state from '../../core/state.js';
import { createModelPicker } from '../../ui/model-picker.js';

export async function renderSiteSettings(container, siteId, site) {
  clear(container);

  const readOnly = !state.isAdmin();
  const nameInput = h('input', { className: 'input', value: site.name, disabled: readOnly, required: true });
  const domainInput = h('input', { className: 'input', value: site.domain || '', placeholder: 'example.com', disabled: readOnly, pattern: '[a-zA-Z0-9][a-zA-Z0-9.\\-]*' });
  const descInput = h('textarea', {
    className: 'input',
    rows: '3',
    value: site.description || '',
    placeholder: 'Brief description of this project',
    disabled: readOnly,
  });

  // Model picker
  let providers = [];
  try {
    providers = await get('/admin/api/providers/catalog');
  } catch {
    // ignore
  }

  const picker = createModelPicker(providers, { currentModelId: site.llm_model_id, disabled: readOnly });
  const { providerSelect, modelSelect } = picker;

  const saveBtn = h('button', {
    className: 'btn btn--primary',
    onClick: async () => {
      nameInput.classList.remove('input--error');
      const name = nameInput.value.trim();
      if (!name) {
        toast.warning('Project name is required');
        nameInput.classList.add('input--error');
        return;
      }
      const selectedModel = picker.getSelectedModel();
      if (!selectedModel) {
        toast.warning('Please select a model');
        return;
      }
      saveBtn.disabled = true;
      saveBtn.textContent = 'Saving...';
      try {
        await put(`/admin/api/sites/${siteId}`, {
          name,
          domain: domainInput.value.trim() || null,
          description: descInput.value.trim() || null,
          llm_model_id: selectedModel.id,
        });
        toast.success('Project settings saved');

        const sites = await get('/admin/api/sites');
        state.set('sites', sites);
      } catch (err) {
        toast.error('Failed to save: ' + err.message);
      }
      saveBtn.disabled = false;
      saveBtn.textContent = 'Save Changes';
    },
  }, 'Save Changes');

  const header = h('div', { className: 'context-panel__header' }, [
    h('h3', { className: 'context-panel__title' }, 'Settings'),
  ]);

  const body = h('div', { className: 'context-panel__body' }, [
    h('div', { className: 'card' }, [
      h('h4', { className: 'card__title mb-4' }, 'General'),
      h('div', { className: 'form-group' }, [
        h('label', {}, ['Project Name', h('span', { className: 'required' }, ' *')]),
        nameInput,
      ]),
      h('div', { className: 'form-group' }, [
        h('label', {}, 'Domain'),
        domainInput,
        h('p', { className: 'form-hint' }, 'The domain this project is served on'),
      ]),
      h('div', { className: 'form-group' }, [
        h('label', {}, 'Description'),
        descInput,
      ]),
      h('div', { className: 'form-group' }, [
        h('label', {}, ['Provider', h('span', { className: 'required' }, ' *')]),
        providerSelect,
      ]),
      h('div', { className: 'form-group' }, [
        h('label', {}, ['Model', h('span', { className: 'required' }, ' *')]),
        modelSelect,
      ]),
      ...(state.isAdmin() ? [h('div', { className: 'mt-4' }, [saveBtn])] : []),
    ]),

    // Share with the world (admin only)
    ...(state.isAdmin() ? [buildShareCard(siteId, site)] : []),

    // Site visibility (admin only)
    ...(state.isAdmin() ? [h('div', { className: 'card' }, (() => {
      const isActive = site.status === 'active';
      const statusBadge = h('span', {
        className: `badge ${isActive ? 'badge--success' : 'badge--secondary'}`,
      }, isActive ? 'Live' : 'Disabled');

      const toggleBtn = h('button', {
        className: `btn ${isActive ? 'btn--danger' : 'btn--success'} btn--sm`,
        onClick: async () => {
          toggleBtn.disabled = true;
          try {
            const updated = await post(`/admin/api/sites/${siteId}/toggle-status`);
            toast.success(updated.status === 'active' ? 'Project is now live' : 'Project disabled');
            // Re-render with updated site data
            const sites = await get('/admin/api/sites');
            state.set('sites', sites);
            renderSiteSettings(container, siteId, updated);
          } catch (err) {
            toast.error('Failed to toggle: ' + err.message);
            toggleBtn.disabled = false;
          }
        },
      }, isActive ? 'Disable Project' : 'Enable Project');

      return [
        h('div', { className: 'card__header' }, [
          h('h4', { className: 'card__title' }, 'Project Visibility'),
          statusBadge,
        ]),
        h('p', { className: 'form-hint mt-2' },
          'Disabled projects return 404 to all visitors. The brain continues running independently.'
        ),
        h('div', { className: 'mt-3' }, [toggleBtn]),
      ];
    })())] : []),

    // Danger zone (admin only)
    ...(state.isAdmin() ? [h('div', { className: 'danger-zone' }, [
      h('h4', { className: 'danger-zone__title' }, 'Danger Zone'),
      h('p', { className: 'danger-zone__desc' },
        'Deleting a project will permanently remove all its data, pages, and chat history.'
      ),
      h('button', {
        className: 'btn btn--danger',
        onClick: () => {
          modal.confirmDanger(
            'Delete Project',
            `Are you sure you want to delete "${site.name}"? This action cannot be undone.`,
            async () => {
              try {
                await del(`/admin/api/sites/${siteId}`);
                toast.success('Project deleted');
                navigate('/sites');
              } catch (err) {
                toast.error('Failed to delete: ' + err.message);
              }
            }
          );
        },
      }, [
        h('span', { innerHTML: icon('trash') }),
        'Delete Project',
      ]),
    ])] : []),
  ]);

  container.appendChild(header);
  container.appendChild(body);
}

/**
 * Builds the "Share with the World" card for tunnel sharing.
 */
function buildShareCard(siteId, site) {
  const card = h('div', { className: 'card' });
  const titleRow = h('div', { className: 'card__header' }, [
    h('h4', { className: 'card__title' }, 'Share with the World'),
    h('span', { innerHTML: icon('globe'), className: 'card__icon' }),
  ]);
  card.appendChild(titleRow);

  const content = h('div', { className: 'mt-3' });
  card.appendChild(content);

  // Derive subdomain from the .localhost domain (same slug used at creation time).
  // e.g. "myblog.localhost" → "myblog", or fall back to slugified site name.
  const domainStr = site.domain || '';
  const defaultSub = domainStr.endsWith('.localhost')
    ? domainStr.replace('.localhost', '')
    : (site.name || '').toLowerCase().replace(/[^a-z0-9]/g, '').slice(0, 63) || 'my-project';

  const subInput = h('input', {
    className: 'input',
    value: defaultSub,
    placeholder: 'my-project',
    pattern: '[a-z0-9][a-z0-9\\-]{1,61}[a-z0-9]',
    style: 'flex:1;min-width:0',
  });
  subInput.addEventListener('input', () => {
    subInput.value = subInput.value.toLowerCase().replace(/[^a-z0-9-]/g, '');
  });

  const suffix = h('span', {
    style: 'padding:0.5rem;color:var(--text-secondary);white-space:nowrap;font-size:0.9rem',
  }, '.humansout.com');

  const inputRow = h('div', { style: 'display:flex;align-items:center;gap:0' }, [subInput, suffix]);

  const statusArea = h('div', { className: 'mt-3' });
  const buttonArea = h('div', { className: 'mt-3' });

  content.appendChild(h('p', { className: 'form-hint mb-3' },
    'Share your project instantly. No domain or server needed. Anyone with the link can visit your project.'
  ));
  content.appendChild(h('div', { className: 'form-group' }, [
    h('label', {}, 'Subdomain'),
    inputRow,
  ]));
  content.appendChild(statusArea);
  content.appendChild(buttonArea);

  // Load current tunnel status.
  loadTunnelStatus(siteId, subInput, statusArea, buttonArea);

  return card;
}

async function loadTunnelStatus(siteId, subInput, statusArea, buttonArea) {
  try {
    const status = await get(`/admin/api/sites/${siteId}/tunnel/status`);
    renderTunnelState(siteId, subInput, statusArea, buttonArea, status);
  } catch {
    renderTunnelState(siteId, subInput, statusArea, buttonArea, { active: false });
  }
}

function renderTunnelState(siteId, subInput, statusArea, buttonArea, status) {
  clear(statusArea);
  clear(buttonArea);

  if (status.active) {
    subInput.value = status.subdomain;
    subInput.disabled = true;

    const urlLink = h('a', {
      href: status.url,
      target: '_blank',
      rel: 'noopener',
      className: 'link',
    }, status.url);

    const copyBtn = h('button', {
      className: 'btn btn--ghost btn--sm',
      onClick: () => {
        navigator.clipboard.writeText(status.url);
        toast.success('Link copied');
      },
    }, [h('span', { innerHTML: icon('link') }), 'Copy Link']);

    const stopBtn = h('button', {
      className: 'btn btn--danger btn--sm',
      onClick: async () => {
        stopBtn.disabled = true;
        try {
          await post(`/admin/api/sites/${siteId}/tunnel/stop`);
          toast.success('Sharing stopped');
          subInput.disabled = false;
          renderTunnelState(siteId, subInput, statusArea, buttonArea, { active: false });
        } catch (err) {
          toast.error('Failed to stop: ' + err.message);
          stopBtn.disabled = false;
        }
      },
    }, 'Stop Sharing');

    statusArea.appendChild(h('div', {
      style: 'display:flex;align-items:center;gap:0.5rem;padding:0.75rem;background:var(--bg-secondary);border-radius:8px',
    }, [
      h('span', { style: 'color:var(--success)' }, '\u2713'),
      h('span', {}, 'Live at '),
      urlLink,
    ]));

    buttonArea.appendChild(h('div', { style: 'display:flex;gap:0.5rem' }, [copyBtn, stopBtn]));
  } else {
    const startBtn = h('button', {
      className: 'btn btn--primary',
      onClick: async () => {
        const sub = subInput.value.trim();
        if (!sub || sub.length < 3) {
          toast.warning('Subdomain must be at least 3 characters');
          return;
        }
        startBtn.disabled = true;
        startBtn.textContent = 'Connecting...';
        try {
          const result = await post(`/admin/api/sites/${siteId}/tunnel/start`, { subdomain: sub });
          toast.success('Sharing started!');
          renderTunnelState(siteId, subInput, statusArea, buttonArea, {
            active: true,
            subdomain: sub,
            url: result.url,
          });
        } catch (err) {
          toast.error(err.message || 'Failed to start sharing');
          startBtn.disabled = false;
          startBtn.textContent = 'Start Sharing';
        }
      },
    }, 'Start Sharing');

    buttonArea.appendChild(startBtn);
  }
}
