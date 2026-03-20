/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Templates view — list, save from site, delete, and clone templates.
 */

import { h, clear } from '../../core/dom.js';
import { get, post, del } from '../../core/http.js';
import { navigate } from '../../core/router.js';
import { icon } from '../../ui/icon.js';
import * as toast from '../../ui/toast.js';
import * as modal from '../../ui/modal.js';
import * as state from '../../core/state.js';
import { emptyState } from '../../ui/helpers.js';
import { createModelPicker } from '../../ui/model-picker.js';

export async function renderTemplates(container) {
  clear(container);

  const headerChildren = [
    h('h3', { className: 'context-panel__title context-panel__title--page' }, 'Templates'),
  ];
  if (state.isAdmin()) {
    headerChildren.push(h('button', {
      className: 'btn btn--primary',
      onClick: () => showSaveModal(),
    }, [
      h('span', { innerHTML: icon('plus') }),
      'Save Template',
    ]));
  }

  const header = h('div', { className: 'context-panel__header context-panel__header--page flex items-center justify-between' }, headerChildren);
  const listContainer = h('div', { className: 'context-panel__body context-panel__body--page' });

  container.appendChild(header);
  container.appendChild(listContainer);

  let templates = [];

  async function loadTemplates() {
    try {
      templates = await get('/admin/api/templates') || [];
      renderList();
    } catch (err) {
      toast.error('Failed to load templates: ' + err.message);
    }
  }

  function renderList() {
    clear(listContainer);

    if (!templates || templates.length === 0) {
      listContainer.appendChild(emptyState('No templates yet. Build a site, then save it as a template to reuse later.'));
      return;
    }

    const rows = templates.map(tpl => {
      return h('tr', {}, [
        h('td', {}, [
          h('strong', {}, tpl.name),
          tpl.description ? h('div', { className: 'text-muted text-sm', style: { marginTop: '2px' } }, tpl.description) : null,
        ].filter(Boolean)),
        h('td', {}, [
          h('span', { className: 'badge' }, tpl.category || 'general'),
        ]),
        h('td', {}, new Date(tpl.created_at).toLocaleDateString()),
        h('td', {}, [
          h('div', { className: 'table__actions' }, [
            h('button', {
              className: 'btn btn--primary btn--sm',
              title: 'Clone as new project',
              onClick: () => showCloneModal(tpl),
            }, [
              h('span', { innerHTML: icon('copy') }),
              'Clone',
            ]),
            ...(state.isAdmin() ? [h('button', {
              className: 'btn btn--ghost btn--sm',
              title: 'Delete template',
              onClick: () => confirmDelete(tpl),
              innerHTML: icon('trash'),
            })] : []),
          ]),
        ]),
      ]);
    });

    const tableWrapper = h('div', { className: 'table-wrapper' }, [
      h('table', { className: 'table' }, [
        h('thead', {}, [
          h('tr', {}, [
            h('th', {}, 'Template'),
            h('th', {}, 'Category'),
            h('th', {}, 'Created'),
            h('th', {}, 'Actions'),
          ]),
        ]),
        h('tbody', {}, rows),
      ]),
    ]);

    listContainer.appendChild(tableWrapper);
  }

  async function showSaveModal() {
    // Load sites to let user pick which site to save as template
    let sites = [];
    try {
      const result = await get('/admin/api/sites?limit=100&offset=0');
      sites = result.items || [];
    } catch {
      toast.error('Failed to load sites');
      return;
    }

    if (sites.length === 0) {
      toast.warning('No sites available to save as template');
      return;
    }

    const nameInput = h('input', { className: 'input', placeholder: 'e.g. Restaurant Template' });
    const descInput = h('textarea', { className: 'input', placeholder: 'What kind of site is this template for?', rows: '2' });
    const siteSelect = h('select', { className: 'input' },
      sites.map(s => h('option', { value: String(s.id) }, `${s.name} (${s.mode})`))
    );

    const content = h('div', {}, [
      h('div', { className: 'form-group' }, [
        h('label', {}, ['Template Name', h('span', { className: 'required' }, ' *')]),
        nameInput,
      ]),
      h('div', { className: 'form-group' }, [
        h('label', {}, 'Description'),
        descInput,
      ]),
      h('div', { className: 'form-group' }, [
        h('label', {}, ['Source Project', h('span', { className: 'required' }, ' *')]),
        siteSelect,
        h('div', { className: 'text-muted text-sm', style: { marginTop: '4px' } }, 'The site must have completed its build to save as a template.'),
      ]),
    ]);

    modal.show('Save as Template', content, [
      { label: 'Cancel', onClick: () => {} },
      {
        label: 'Save Template',
        className: 'btn btn--primary',
        onClick: async () => {
          const name = nameInput.value.trim();
          if (!name) {
            toast.warning('Template name is required');
            return false;
          }
          try {
            await post('/admin/api/templates', {
              name,
              description: descInput.value.trim(),
              site_id: parseInt(siteSelect.value),
            });
            toast.success('Template saved');
            loadTemplates();
          } catch (err) {
            toast.error('Failed to save template: ' + err.message);
            return false;
          }
        },
      },
    ]);

    nameInput.focus();
  }

  async function showCloneModal(tpl) {
    let providers = [];
    try {
      providers = await get('/admin/api/providers/catalog');
    } catch { /* empty picker is fine */ }

    const nameInput = h('input', { className: 'input', placeholder: 'My New Project' });
    const domainInput = h('input', { className: 'input', placeholder: 'e.g. mysite.com', pattern: '[a-zA-Z0-9][a-zA-Z0-9.\\-]*' });
    const descInput = h('textarea', { className: 'input', placeholder: 'Optional description...', rows: '2' });
    const picker = createModelPicker(providers);

    const content = h('div', {}, [
      h('div', { className: 'form-group' }, [
        h('label', {}, 'Cloning from'),
        h('div', { className: 'text-muted', style: { padding: '0.5rem 0' } }, [
          h('strong', {}, tpl.name),
          tpl.description ? h('span', {}, ` \u2014 ${tpl.description}`) : null,
        ].filter(Boolean)),
      ]),
      h('div', { className: 'form-group' }, [
        h('label', {}, ['Project Name', h('span', { className: 'required' }, ' *')]),
        nameInput,
      ]),
      h('div', { className: 'form-group' }, [
        h('label', {}, 'Domain'),
        domainInput,
      ]),
      h('div', { className: 'form-group' }, [
        h('label', {}, 'Description'),
        descInput,
      ]),
      h('div', { className: 'form-group' }, [
        h('label', {}, ['Provider', h('span', { className: 'required' }, ' *')]),
        picker.providerSelect,
      ]),
      h('div', { className: 'form-group' }, [
        h('label', {}, ['Model', h('span', { className: 'required' }, ' *')]),
        picker.modelSelect,
      ]),
    ]);

    modal.show(`Clone Template: ${tpl.name}`, content, [
      { label: 'Cancel', onClick: () => {} },
      {
        label: 'Clone & Build',
        className: 'btn btn--primary',
        onClick: async () => {
          const name = nameInput.value.trim();
          if (!name) {
            toast.warning('Project name is required');
            return false;
          }
          const selectedModel = picker.getSelectedModel();
          if (!selectedModel) {
            toast.warning('Please select a model');
            return false;
          }
          try {
            const result = await post('/admin/api/sites/clone', {
              template_id: tpl.id,
              name,
              domain: domainInput.value.trim() || null,
              description: descInput.value.trim() || null,
              llm_model_id: selectedModel.id,
            });
            toast.success('Project created from template \u2014 build starting (PLAN skipped)');
            const siteId = result.site?.id || result.site_id;
            if (siteId) {
              navigate(`/sites/${siteId}/home`);
            } else {
              navigate('/sites');
            }
          } catch (err) {
            toast.error('Failed to clone: ' + err.message);
            return false;
          }
        },
      },
    ]);

    nameInput.focus();
  }

  function confirmDelete(tpl) {
    modal.confirmDanger(
      'Delete Template',
      `Delete template "${tpl.name}"? This won't affect sites already cloned from it.`,
      async () => {
        try {
          await del(`/admin/api/templates/${tpl.id}`);
          toast.success('Template deleted');
          loadTemplates();
        } catch (err) {
          toast.error('Failed to delete: ' + err.message);
        }
      }
    );
  }

  loadTemplates();
}
