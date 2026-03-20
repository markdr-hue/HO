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
