/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * LLM Provider management view.
 */

import { h, clear } from '../../core/dom.js';
import { get, post, put, del } from '../../core/http.js';
import { icon } from '../../ui/icon.js';
import * as toast from '../../ui/toast.js';
import * as modal from '../../ui/modal.js';
import * as state from '../../core/state.js';
import { emptyState } from '../../ui/helpers.js';

export async function renderProviders(container) {
  clear(container);

  const headerChildren = [
    h('h3', { className: 'context-panel__title context-panel__title--page' }, 'Brain Providers'),
  ];
  if (state.isAdmin()) {
    headerChildren.push(h('button', {
      className: 'btn btn--primary',
      onClick: showAddModal,
    }, [
      h('span', { innerHTML: icon('plus') }),
      'Add Provider',
    ]));
  }

  const header = h('div', { className: 'context-panel__header context-panel__header--page flex items-center justify-between' }, headerChildren);
  const listContainer = h('div', { className: 'context-panel__body context-panel__body--page' });

  container.appendChild(header);
  container.appendChild(listContainer);

  const expandedModels = new Set();

  async function loadProviders() {
    try {
      const providers = await get('/admin/api/providers');
      renderList(providers);
    } catch (err) {
      toast.error('Failed to load providers: ' + err.message);
    }
  }

  function renderList(providers) {
    clear(listContainer);

    if (providers.length === 0) {
      listContainer.appendChild(emptyState('No providers configured. Add one to enable project building.'));
      return;
    }

    const cards = providers.map(p => {
      const statusBadge = p.is_enabled !== false
        ? h('span', { className: 'badge badge--success' }, 'Active')
        : h('span', { className: 'badge badge--warning' }, 'Disabled');

      const modelsContainer = h('div', { className: 'mt-4' });

      const card = h('div', { className: 'card mb-4' }, [
        h('div', { className: 'card__header' }, [
          h('div', { className: 'flex items-center gap-3' }, [
            h('span', { innerHTML: icon(p.provider_type === 'anthropic' ? 'brain' : 'zap') }),
            h('div', {}, [
              h('h4', { className: 'card__title' }, p.name),
              h('span', { className: 'text-xs text-secondary' }, p.provider_type),
            ]),
          ]),
          h('div', { className: 'flex items-center gap-2' }, [
            statusBadge,
          ]),
        ]),
        h('div', { className: 'flex items-center gap-2 mt-4' }, [
          ...(state.isAdmin() ? [h('button', {
            className: 'btn btn--sm',
            onClick: () => testProvider(p.id),
          }, [
            h('span', { innerHTML: icon('activity') }),
            'Test',
          ])] : []),
          h('button', {
            className: 'btn btn--sm',
            onClick: () => toggleModels(p, modelsContainer),
          }, [
            h('span', { innerHTML: icon('list') }),
            'Models',
          ]),
          ...(state.isAdmin() ? [
            h('button', {
              className: 'btn btn--sm',
              onClick: () => showEditModal(p),
            }, [
              h('span', { innerHTML: icon('edit') }),
              'Edit',
            ]),
            h('button', {
              className: 'btn btn--danger btn--sm',
              onClick: () => confirmDelete(p),
            }, [
              h('span', { innerHTML: icon('trash') }),
              'Delete',
            ]),
          ] : []),
        ]),
        modelsContainer,
      ]);

      if (expandedModels.has(p.id)) {
        loadModels(p, modelsContainer);
      }

      return card;
    });

    cards.forEach(c => listContainer.appendChild(c));
  }

  function toggleModels(provider, container) {
    if (expandedModels.has(provider.id)) {
      expandedModels.delete(provider.id);
      clear(container);
    } else {
      expandedModels.add(provider.id);
      loadModels(provider, container);
    }
  }

  async function loadModels(provider, container) {
    clear(container);
    container.appendChild(h('p', { className: 'text-secondary text-sm' }, 'Loading models...'));

    try {
      const modelsList = await get(`/admin/api/providers/${provider.id}/models`);
      renderModels(provider, modelsList, container);
    } catch (err) {
      clear(container);
      container.appendChild(h('p', { className: 'text-danger text-sm' }, 'Failed to load models: ' + err.message));
    }
  }

  function renderModels(provider, modelsList, container) {
    clear(container);

    const headerItems = [h('h5', { className: 'text-sm font-medium' }, 'Models')];
    if (state.isAdmin()) {
      headerItems.push(h('button', {
        className: 'btn btn--sm btn--primary',
        onClick: () => showAddModelModal(provider, container),
      }, [
        h('span', { innerHTML: icon('plus') }),
        'Add Model',
      ]));
    }
    const hdr = h('div', { className: 'flex items-center justify-between mt-4 mb-2' }, headerItems);
    container.appendChild(hdr);

    if (!modelsList || modelsList.length === 0) {
      container.appendChild(
        h('p', { className: 'text-secondary text-sm ml-2' }, 'No models configured.')
      );
      return;
    }

    const table = h('div', { style: { border: '1px solid var(--border)', borderRadius: 'var(--radius)', overflow: 'hidden' } });

    modelsList.forEach((m, i) => {
      const defaultBadge = m.is_default
        ? h('span', { className: 'badge badge--success ml-2', style: { fontSize: '0.65rem' } }, 'Default')
        : null;

      const row = h('div', {
        className: 'flex items-center justify-between',
        style: Object.assign({ padding: '0.5rem 0.75rem' }, i > 0 ? { borderTop: '1px solid var(--border)' } : {}),
      }, [
        h('div', { className: 'flex items-center gap-2' }, [
          h('span', { className: 'text-sm font-medium' }, m.model_id),
          defaultBadge,
          m.display_name && m.display_name !== m.model_id
            ? h('span', { className: 'text-xs text-secondary' }, `(${m.display_name})`)
            : null,
        ].filter(Boolean)),
        ...(state.isAdmin() ? [h('div', { className: 'flex items-center gap-1' }, [
          !m.is_default ? h('button', {
            className: 'btn btn--sm',
            title: 'Set as default',
            onClick: async () => {
              try {
                await post(`/admin/api/providers/${provider.id}/models/${m.id}/default`);
                toast.success('Default model updated');
                loadModels(provider, container);
              } catch (err) {
                toast.error(err.message);
              }
            },
          }, [
            h('span', { innerHTML: icon('check') }),
          ]) : null,
          h('button', {
            className: 'btn btn--danger btn--sm',
            title: 'Delete model',
            onClick: () => {
              modal.confirmDanger(
                'Delete Model',
                `Delete model "${m.model_id}"?`,
                async () => {
                  try {
                    await del(`/admin/api/providers/${provider.id}/models/${m.id}`);
                    toast.success('Model deleted');
                    loadModels(provider, container);
                  } catch (err) {
                    toast.error(err.message);
                  }
                }
              );
            },
          }, [
            h('span', { innerHTML: icon('trash') }),
          ]),
        ].filter(Boolean))] : []),
      ]);

      table.appendChild(row);
    });

    container.appendChild(table);
  }

  function showAddModelModal(provider, modelsContainer) {
    const modelIdInput = h('input', { className: 'input', placeholder: 'e.g. claude-sonnet-4-20250514' });
    const displayNameInput = h('input', { className: 'input', placeholder: 'e.g. Claude Sonnet 4' });
    const maxTokensInput = h('input', { className: 'input', type: 'number', value: '4096', min: '1' });

    const supportsStreamingCheck = h('input', { type: 'checkbox', checked: true });
    const supportsToolsCheck = h('input', { type: 'checkbox', checked: true });
    const isDefaultCheck = h('input', { type: 'checkbox' });

    const content = h('div', {}, [
      h('div', { className: 'form-group' }, [
        h('label', {}, 'Model ID'),
        modelIdInput,
        h('p', { className: 'form-hint' }, 'The model identifier used by the API.'),
      ]),
      h('div', { className: 'form-group' }, [
        h('label', {}, 'Display Name'),
        displayNameInput,
        h('p', { className: 'form-hint' }, 'Optional friendly name.'),
      ]),
      h('div', { className: 'form-group' }, [
        h('label', {}, 'Max Tokens'),
        maxTokensInput,
      ]),
      h('div', { className: 'form-group' }, [
        h('label', { className: 'flex items-center gap-2' }, [
          supportsStreamingCheck,
          'Supports Streaming',
        ]),
      ]),
      h('div', { className: 'form-group' }, [
        h('label', { className: 'flex items-center gap-2' }, [
          supportsToolsCheck,
          'Supports Tools',
        ]),
      ]),
      h('div', { className: 'form-group' }, [
        h('label', { className: 'flex items-center gap-2' }, [
          isDefaultCheck,
          'Set as Default',
        ]),
      ]),
    ]);

    modal.show('Add Model', content, [
      { label: 'Cancel', onClick: () => {} },
      {
        label: 'Add Model',
        className: 'btn btn--primary',
        onClick: async () => {
          const modelId = modelIdInput.value.trim();
          if (!modelId) {
            toast.error('Model ID is required');
            return false;
          }
          try {
            await post(`/admin/api/providers/${provider.id}/models`, {
              model_id: modelId,
              display_name: displayNameInput.value.trim() || modelId,
              max_tokens: parseInt(maxTokensInput.value) || 4096,
              supports_streaming: supportsStreamingCheck.checked,
              supports_tools: supportsToolsCheck.checked,
              is_default: isDefaultCheck.checked,
            });
            toast.success('Model added');
            loadModels(provider, modelsContainer);
          } catch (err) {
            toast.error(err.message);
            return false;
          }
        },
      },
    ]);
  }

  async function testProvider(id) {
    try {
      toast.info('Testing connection...');
      const result = await post(`/admin/api/providers/${id}/test`);
      if (result.success) {
        toast.success('Provider connection successful');
      } else {
        toast.error('Connection failed: ' + (result.error || 'Unknown error'));
      }
    } catch (err) {
      toast.error('Test failed: ' + err.message);
    }
  }

  function showAddModal() {
    let selectedType = 'anthropic';

    const nameInput = h('input', { className: 'input', placeholder: 'Anthropic' });
    const apiKeyInput = h('input', { className: 'input', type: 'password', placeholder: 'Enter your API key' });
    const baseUrlInput = h('input', { className: 'input', placeholder: 'https://api.example.com/v1/chat/completions' });

    const typeSelect = h('select', { className: 'input' }, [
      h('option', { value: 'anthropic' }, 'Anthropic'),
      h('option', { value: 'openai' }, 'OpenAI-compatible'),
    ]);

    const baseUrlGroup = h('div', { className: 'form-group', style: { display: 'none' } }, [
      h('label', {}, ['Base URL', h('span', { className: 'required' }, ' *')]),
      baseUrlInput,
      h('p', { className: 'form-hint' }, 'Full API endpoint URL (required for OpenAI-compatible providers)'),
    ]);

    typeSelect.addEventListener('change', () => {
      selectedType = typeSelect.value;
      if (!nameInput.value || nameInput.value === 'Anthropic' || nameInput.value === 'OpenAI') {
        nameInput.value = selectedType === 'anthropic' ? 'Anthropic' : '';
      }
      baseUrlGroup.style.display = selectedType === 'openai' ? '' : 'none';
    });

    nameInput.value = 'Anthropic';

    const content = h('div', {}, [
      h('div', { className: 'form-group' }, [
        h('label', {}, ['Provider Type', h('span', { className: 'required' }, ' *')]),
        typeSelect,
      ]),
      h('div', { className: 'form-group' }, [
        h('label', {}, ['Name', h('span', { className: 'required' }, ' *')]),
        nameInput,
      ]),
      baseUrlGroup,
      h('div', { className: 'form-group' }, [
        h('label', {}, ['API Key', h('span', { className: 'required' }, ' *')]),
        apiKeyInput,
        h('p', { className: 'form-hint' }, 'Your key is encrypted at rest.'),
      ]),
    ]);

    modal.show('Add Provider', content, [
      { label: 'Cancel', onClick: () => {} },
      {
        label: 'Add Provider',
        className: 'btn btn--primary',
        onClick: async () => {
          const name = nameInput.value.trim();
          if (!name) {
            toast.warning('Provider name is required');
            return false;
          }
          if (selectedType === 'openai' && !baseUrlInput.value.trim()) {
            toast.warning('Base URL is required for OpenAI-compatible providers');
            return false;
          }
          try {
            await post('/admin/api/providers', {
              name,
              provider_type: typeSelect.value,
              api_key: apiKeyInput.value.trim(),
              base_url: baseUrlInput.value.trim() || null,
            });
            toast.success('Provider added');
            loadProviders();
          } catch (err) {
            toast.error(err.message);
            return false;
          }
        },
      },
    ]);
  }

  function showEditModal(provider) {
    const nameInput = h('input', { className: 'input', value: provider.name });
    const apiKeyInput = h('input', {
      className: 'input',
      type: 'password',
      placeholder: 'Leave empty to keep current key',
    });
    const baseUrlInput = h('input', {
      className: 'input',
      value: provider.base_url || '',
      placeholder: 'https://api.example.com/v1/chat/completions',
    });

    const isOpenAIType = provider.provider_type === 'openai';

    const content = h('div', {}, [
      h('div', { className: 'form-group' }, [
        h('label', {}, ['Name', h('span', { className: 'required' }, ' *')]),
        nameInput,
      ]),
      ...(isOpenAIType ? [h('div', { className: 'form-group' }, [
        h('label', {}, ['Base URL', h('span', { className: 'required' }, ' *')]),
        baseUrlInput,
        h('p', { className: 'form-hint' }, 'Full API endpoint URL'),
      ])] : []),
      h('div', { className: 'form-group' }, [
        h('label', {}, 'API Key'),
        apiKeyInput,
        h('p', { className: 'form-hint' }, 'Leave empty to keep the current key.'),
      ]),
    ]);

    modal.show('Edit Provider', content, [
      { label: 'Cancel', onClick: () => {} },
      {
        label: 'Save',
        className: 'btn btn--primary',
        onClick: async () => {
          if (isOpenAIType && !baseUrlInput.value.trim()) {
            toast.warning('Base URL is required for OpenAI-compatible providers');
            return false;
          }
          try {
            const body = {
              name: nameInput.value.trim(),
              is_enabled: true,
              base_url: baseUrlInput.value.trim() || null,
            };
            if (apiKeyInput.value.trim()) {
              body.api_key = apiKeyInput.value.trim();
            }
            await put(`/admin/api/providers/${provider.id}`, body);
            toast.success('Provider updated');
            loadProviders();
          } catch (err) {
            toast.error(err.message);
            return false;
          }
        },
      },
    ]);
  }

  function confirmDelete(provider) {
    modal.confirmDanger(
      'Delete Provider',
      `Are you sure you want to delete "${provider.name}"?`,
      async () => {
        try {
          await del(`/admin/api/providers/${provider.id}`);
          toast.success('Provider deleted');
          loadProviders();
        } catch (err) {
          toast.error(err.message);
        }
      }
    );
  }

  loadProviders();
}
