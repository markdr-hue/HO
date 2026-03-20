/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Scheduled tasks view for context panel.
 */

import { h, clear } from '../../core/dom.js';
import { get, post, del } from '../../core/http.js';
import { icon } from '../../ui/icon.js';
import * as toast from '../../ui/toast.js';
import * as modal from '../../ui/modal.js';
import * as state from '../../core/state.js';
import { emptyState, formatInterval } from '../../ui/helpers.js';

export async function renderSiteTasks(container, siteId) {
  clear(container);

  const header = h('div', { className: 'context-panel__header' }, [
    h('h3', { className: 'context-panel__title' }, 'Scheduled Tasks'),
  ]);

  const listContainer = h('div', { className: 'context-panel__body' });
  container.appendChild(header);
  container.appendChild(listContainer);

  async function loadTasks() {
    try {
      const tasks = await get(`/admin/api/sites/${siteId}/tasks`);
      renderTasksList(listContainer, tasks, siteId, loadTasks);
    } catch (err) {
      toast.error('Failed to load tasks: ' + err.message);
    }
  }

  loadTasks();
}

function renderTasksList(container, tasks, siteId, reload) {
  clear(container);

  if (state.isAdmin()) {
    container.appendChild(h('button', {
      className: 'btn btn--primary btn--sm mb-3',
      onClick: () => showCreateTaskModal(siteId, container.parentElement, reload),
    }, [h('span', { innerHTML: icon('plus') }), ' Create Task']));
  }

  if (!tasks || tasks.length === 0) {
    container.appendChild(emptyState('No scheduled tasks configured.'));
    return;
  }

  for (const task of tasks) {
    const enabledBadge = task.is_enabled
      ? h('span', { className: 'badge badge--success' }, 'Enabled')
      : h('span', { className: 'badge badge--warning' }, 'Disabled');

    const schedule = task.cron_expression
      ? `Cron: ${task.cron_expression}`
      : task.interval_seconds
      ? `Every ${formatInterval(task.interval_seconds)}`
      : 'No schedule';

    const toggleBtn = h('button', {
      className: `btn btn--ghost btn--sm`,
      title: task.is_enabled ? 'Disable' : 'Enable',
      onClick: async () => {
        try {
          await post(`/admin/api/sites/${siteId}/tasks/${task.id}/toggle`, {});
          toast.success(task.is_enabled ? 'Task disabled' : 'Task enabled');
          reload();
        } catch (err) {
          toast.error('Failed to toggle task: ' + err.message);
        }
      },
    }, task.is_enabled ? 'Disable' : 'Enable');

    const deleteBtn = h('button', {
      className: 'btn btn--ghost btn--sm',
      title: 'Delete',
      innerHTML: icon('trash'),
      onClick: () => {
        modal.confirmDanger(
          'Delete Task',
          `Are you sure you want to delete "${task.name}"? This action cannot be undone.`,
          async () => {
            try {
              await del(`/admin/api/sites/${siteId}/tasks/${task.id}`);
              toast.success('Task deleted');
              reload();
            } catch (err) {
              toast.error('Failed to delete task: ' + err.message);
            }
          }
        );
      },
    });

    const card = h('div', { className: 'card mb-3' }, [
      h('div', { className: 'card__header' }, [
        h('div', { className: 'flex items-center gap-2' }, [
          h('span', { innerHTML: icon('clock') }),
          h('strong', {}, task.name),
        ]),
        h('div', { className: 'flex items-center gap-2' }, [
          enabledBadge,
          ...(state.isAdmin() ? [
            h('button', {
              className: 'btn btn--sm btn--secondary',
              onClick: () => showTaskRunsModal(siteId, task),
            }, 'History'),
            toggleBtn,
            deleteBtn,
          ] : []),
        ]),
      ]),
      h('div', { className: 'mt-2' }, [
        task.description ? h('p', { className: 'text-sm text-secondary mb-2' }, task.description) : null,
        h('div', { className: 'flex gap-4 text-xs text-secondary' }, [
          h('span', {}, schedule),
          task.last_run ? h('span', {}, `Last: ${new Date(task.last_run).toLocaleString()}`) : null,
          task.next_run ? h('span', {}, `Next: ${new Date(task.next_run).toLocaleString()}`) : null,
        ].filter(Boolean)),
      ]),
    ]);

    container.appendChild(card);
  }
}

function showCreateTaskModal(siteId, container, reload) {
  const nameInput = h('input', { className: 'input', placeholder: 'Task name' });
  const descInput = h('textarea', { className: 'input', rows: 2, placeholder: 'Description (optional)' });
  const scheduleSelect = h('select', { className: 'input', onChange: () => {
    cronGroup.style.display = scheduleSelect.value === 'cron' ? '' : 'none';
    intervalGroup.style.display = scheduleSelect.value === 'interval' ? '' : 'none';
  }}, [
    h('option', { value: 'cron' }, 'Cron Expression'),
    h('option', { value: 'interval' }, 'Interval (seconds)'),
  ]);
  const cronInput = h('input', { className: 'input', placeholder: '0 */6 * * *' });
  const intervalInput = h('input', { className: 'input', type: 'number', placeholder: '3600' });
  const promptInput = h('textarea', { className: 'input', rows: 4, placeholder: 'Prompt to execute when task runs...' });
  const cronGroup = h('div', { className: 'form-group' }, [h('label', {}, 'Cron Expression'), cronInput]);
  const intervalGroup = h('div', { className: 'form-group', style: { display: 'none' } }, [h('label', {}, 'Interval (seconds)'), intervalInput]);

  const content = h('div', {}, [
    h('div', { className: 'form-group' }, [h('label', {}, ['Name', h('span', { className: 'required' }, ' *')]), nameInput]),
    h('div', { className: 'form-group' }, [h('label', {}, 'Description'), descInput]),
    h('div', { className: 'form-group' }, [h('label', {}, 'Schedule Type'), scheduleSelect]),
    cronGroup,
    intervalGroup,
    h('div', { className: 'form-group' }, [h('label', {}, ['Prompt', h('span', { className: 'required' }, ' *')]), promptInput]),
  ]);

  modal.show('Create Task', content, [
    { label: 'Cancel', onClick: () => {} },
    {
      label: 'Create',
      className: 'btn btn--primary',
      onClick: async () => {
        const body = {
          name: nameInput.value.trim(),
          description: descInput.value.trim(),
          prompt: promptInput.value.trim(),
        };
        if (scheduleSelect.value === 'cron') {
          body.cron_expression = cronInput.value.trim();
        } else {
          body.interval_seconds = parseInt(intervalInput.value) || 0;
        }
        nameInput.classList.remove('input--error');
        promptInput.classList.remove('input--error');
        cronInput.classList.remove('input--error');
        intervalInput.classList.remove('input--error');

        let valid = true;
        if (!body.name) {
          toast.error('Name is required');
          nameInput.classList.add('input--error');
          valid = false;
        }
        if (!body.prompt) {
          toast.error('Prompt is required');
          promptInput.classList.add('input--error');
          valid = false;
        }
        if (scheduleSelect.value === 'cron' && !body.cron_expression) {
          toast.error('Cron expression is required');
          cronInput.classList.add('input--error');
          valid = false;
        }
        if (scheduleSelect.value === 'interval' && (!body.interval_seconds || body.interval_seconds <= 0)) {
          toast.error('Interval must be a positive number');
          intervalInput.classList.add('input--error');
          valid = false;
        }
        if (!valid) return false;
        try {
          await post(`/admin/api/sites/${siteId}/tasks`, body);
          toast.success('Task created');
          reload();
        } catch (err) {
          toast.error(err.message);
          return false;
        }
      },
    },
  ]);
}

function showTaskRunsModal(siteId, task) {
  const content = h('div', {}, [h('p', { className: 'text-secondary' }, 'Loading...')]);

  modal.show(`Run History: ${task.name}`, content, [{ label: 'Close', onClick: () => {} }]);

  get(`/admin/api/sites/${siteId}/tasks/${task.id}/runs`).then(runs => {
    clear(content);
    if (runs.length === 0) {
      content.appendChild(h('p', { className: 'text-secondary' }, 'No runs yet.'));
      return;
    }
    for (const run of runs) {
      const statusBadge = run.status === 'completed'
        ? h('span', { className: 'badge badge--success' }, 'Completed')
        : run.status === 'failed'
        ? h('span', { className: 'badge badge--danger' }, 'Failed')
        : h('span', { className: 'badge badge--warning' }, 'Running');
      const children = [
        h('div', { className: 'flex items-center justify-between' }, [
          statusBadge,
          h('span', { className: 'text-xs text-secondary' },
            new Date(run.started_at).toLocaleString()),
        ]),
      ];
      if (run.error_message) {
        children.push(h('p', { className: 'text-sm text-danger mt-2' }, run.error_message));
      }
      content.appendChild(h('div', { className: 'card mb-2', style: { padding: '8px 12px' } }, children));
    }
  }).catch(err => {
    clear(content);
    content.appendChild(h('p', { className: 'text-danger' }, 'Failed to load runs: ' + err.message));
  });
}

