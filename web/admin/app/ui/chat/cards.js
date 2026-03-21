/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Interactive chat card factory.
 * Creates rich cards for specific tool types instead of generic "Tool: X → Done" cards.
 */

import { h } from '../../core/dom.js';
import { post } from '../../core/http.js';
import * as state from '../../core/state.js';
import { icon } from '../icon.js';
import * as toast from '../toast.js';
import { buildQuestionInput } from '../question-input.js';

// Navigation callback - set by site/index.js to control context panel
let _switchPanel = null;

export function setPanelSwitcher(fn) {
  _switchPanel = fn;
}

function switchPanel(panel) {
  if (_switchPanel) _switchPanel(panel);
}

/**
 * Route a tool call to the appropriate card renderer.
 * Returns { element, updateStatus } like createToolCall.
 */
// Action-level labels for manager tools: { toolName: { action: label } }
const managerLabels = {
  manage_pages: {
    save: 'Saving page', get: 'Reading page', list: 'Listing pages',
    delete: 'Deleting page', restore: 'Restoring page', history: 'Checking page history', search: 'Searching pages',
  },
  manage_files: {
    save: 'Saving file', get: 'Reading file', list: 'Listing files',
    delete: 'Deleting file', rename: 'Renaming file',
  },
  manage_schema: {
    create: 'Creating table', alter: 'Altering table', describe: 'Describing table',
    list: 'Listing tables', drop: 'Dropping table',
  },
  manage_data: {
    insert: 'Adding data', query: 'Querying data', update: 'Updating data',
    delete: 'Deleting data', count: 'Counting rows',
  },
  manage_endpoints: {
    create_api: 'Creating endpoint', list_api: 'Listing endpoints', delete_api: 'Removing endpoint',
    create_auth: 'Creating auth endpoint', list_auth: 'Listing auth endpoints', delete_auth: 'Removing auth endpoint',
    verify_password: 'Verifying password',
  },

  manage_communication: {
    ask: 'Asking a question', check: 'Checking answers',
  },
  manage_analytics: {
    query: 'Checking analytics', summary: 'Reading analytics summary',
  },
  manage_diagnostics: {
    health: 'Checking system health', errors: 'Reviewing errors', integrity: 'Checking integrity',
  },
  manage_webhooks: {
    create: 'Creating webhook', get: 'Reading webhook', list: 'Listing webhooks',
    delete: 'Removing webhook', update: 'Updating webhook', subscribe: 'Subscribing webhook',
  },
  manage_providers: {
    add: 'Adding service provider', list: 'Listing service providers',
    remove: 'Removing service provider', update: 'Updating service provider', request: 'Calling external API',
  },
  manage_secrets: {
    store: 'Storing secret', list: 'Listing secrets', delete: 'Removing secret',
  },
  manage_site: {
    info: 'Getting site info', set_mode: 'Changing site mode',
  },
  manage_scheduler: {
    create: 'Scheduling task', list: 'Listing tasks', update: 'Updating task', delete: 'Removing task',
  },
  manage_layout: {
    save: 'Saving layout', get: 'Reading layout', list: 'Listing layouts',
  },
};

// Fallback labels when no action is available.
const toolLabels = {
  manage_pages: 'Managing pages',
  manage_files: 'Managing files',
  manage_schema: 'Managing schema',
  manage_data: 'Managing data',
  manage_endpoints: 'Managing endpoints',

  manage_communication: 'Communication',
  manage_analytics: 'Checking analytics',
  manage_diagnostics: 'Running diagnostics',
  manage_webhooks: 'Managing webhooks',
  manage_providers: 'Managing providers',
  manage_secrets: 'Managing secrets',
  manage_site: 'Managing site',
  manage_scheduler: 'Managing scheduler',
  manage_layout: 'Managing layout',
  make_http_request: 'Making HTTP request',
};

/**
 * Get a friendly label for a tool, optionally using the action from args.
 */
export function getToolLabel(toolName, args) {
  if (args?.action && managerLabels[toolName]?.[args.action]) {
    return managerLabels[toolName][args.action];
  }
  return toolLabels[toolName] || toolName.replace(/_/g, ' ');
}

export function createToolCard(toolName, status, args, result) {
  const action = args?.action;

  switch (toolName) {
    case 'manage_pages':
      if (action === 'save' || action === 'delete' || action === 'restore') {
        return createPageCard(toolName, status, args, result);
      }
      return null;

    case 'manage_files':
      if (action === 'save' || action === 'delete') {
        return createAssetCard(toolName, status, args, result);
      }
      return null;

    case 'manage_schema':
    case 'manage_data':
      return createTableCard(toolName, status, args, result);

    case 'manage_endpoints':
      return createEndpointCard(toolName, status, args, result);

    default:
      return null; // Fallback to generic tool card
  }
}

/**
 * Create an inline question card with interactive option buttons.
 */
export function createQuestionCard(questionData) {
  const { id, question, urgency, context, type, options } = questionData;

  const card = h('div', { className: 'chat-card chat-card--question' });

  const header = h('div', { className: 'chat-card__header chat-card__header--question' }, [
    h('span', { innerHTML: icon('help-circle'), className: 'chat-card__icon chat-card__icon--question' }),
    h('span', { className: 'chat-card__label' }, 'Question'),
    urgency === 'high'
      ? h('span', { className: 'badge badge--danger' }, 'Urgent')
      : h('span', { className: 'badge badge--question' }, 'Needs your input'),
  ]);

  const body = h('div', { className: 'chat-card__body' }, [
    h('p', { className: 'chat-card__text' }, question),
  ]);

  if (context) {
    body.appendChild(h('p', { className: 'chat-card__context text-sm text-secondary' }, context));
  }

  card.appendChild(header);
  card.appendChild(body);

  // For single-choice with no explicit type, allow immediate submit on click
  const isSingleChoice = (type === 'single_choice' || !type) && options;
  const { inputEl, getValue } = buildQuestionInput(questionData);

  if (isSingleChoice) {
    // Override: single-choice option buttons submit immediately on click
    const btns = inputEl.querySelectorAll('.chat-card__option-btn');
    btns.forEach(btn => {
      btn.addEventListener('click', () => submitAnswer(id, btn.textContent, card));
    });
    card.appendChild(inputEl);
  } else if (questionData.fields) {
    // Fields: wrap with submit button
    const submitBtn = h('button', {
      className: 'btn btn--sm btn--primary',
      onClick: () => { if (getValue()) submitAnswer(id, getValue(), card); },
    }, 'Submit');
    card.appendChild(inputEl);
    card.appendChild(h('div', { className: 'chat-card__field-actions' }, [submitBtn]));
  } else if (type === 'multiple_choice') {
    // Multiple choice: submit button
    const submitBtn = h('button', {
      className: 'btn btn--sm btn--primary',
      onClick: () => { if (getValue()) submitAnswer(id, getValue(), card); },
    }, 'Submit');
    card.appendChild(inputEl);
    card.appendChild(h('div', { className: 'chat-card__custom-answer' }, [submitBtn]));
  } else {
    // Open text: input + send button + Enter key
    const input = inputEl.querySelector('input');
    if (input) {
      input.addEventListener('keydown', (e) => {
        if (e.key === 'Enter' && getValue()) submitAnswer(id, getValue(), card);
      });
    }
    card.appendChild(h('div', { className: 'chat-card__custom-answer' }, [
      inputEl,
      h('button', {
        className: 'btn btn--sm btn--primary',
        onClick: () => { if (getValue()) submitAnswer(id, getValue(), card); },
      }, 'Send'),
    ]));
  }

  return { element: card, questionId: id };
}

async function submitAnswer(questionId, answer, cardEl) {
  try {
    await post(`/admin/api/questions/${questionId}/answer`, { answer });

    // Decrement badge count for this single answered question
    const pending = Math.max(0, (state.get('pendingQuestions') || 0) - 1);
    state.set('pendingQuestions', pending);

    // Transform card to answered state
    cardEl.innerHTML = '';
    cardEl.className = 'chat-card chat-card--question chat-card--answered';

    cardEl.appendChild(h('div', { className: 'chat-card__header' }, [
      h('span', { innerHTML: icon('check'), className: 'chat-card__icon chat-card__icon--success' }),
      h('span', { className: 'chat-card__label' }, 'Answered'),
    ]));

    cardEl.appendChild(h('div', { className: 'chat-card__body' }, [
      h('p', { className: 'chat-card__answer-text' }, answer),
      h('button', {
        className: 'btn btn--ghost btn--sm mt-2',
        onClick: () => switchPanel('questions'),
      }, 'View all questions \u2192'),
    ]));

    // Notify chat view so it can show the answer bubble and update the banner
    document.dispatchEvent(new CustomEvent('ho:questionAnswered', {
      detail: { questionId, answer },
    }));

    toast.success('Answer submitted');
  } catch (err) {
    toast.error('Failed to submit answer: ' + err.message);
  }
}

/**
 * Create a grouped question card that collects answers for multiple questions
 * and submits them all at once.
 * Returns { element, addQuestion(data), questionIds }.
 */
export function createQuestionGroup(questions) {
  const items = []; // { id, data, getValue, hasValue, numberEl }
  const questionIds = new Set();

  const card = h('div', { className: 'chat-card chat-card--question-group' });

  // Header
  const countBadge = h('span', { className: 'badge badge--question' }, `${questions.length} question${questions.length > 1 ? 's' : ''}`);
  const progressEl = h('span', { className: 'question-group__progress' }, '0/' + questions.length + ' filled');

  const header = h('div', { className: 'chat-card__header chat-card__header--question' }, [
    h('span', { innerHTML: icon('help-circle'), className: 'chat-card__icon chat-card__icon--question' }),
    h('span', { className: 'chat-card__label' }, 'Questions'),
    countBadge,
    progressEl,
  ]);
  card.appendChild(header);

  const list = h('div', { className: 'question-group__list' });
  card.appendChild(list);

  // Submit footer
  const submitProgress = h('span', { className: 'question-group__submit-progress' });
  const submitBtn = h('button', {
    className: 'btn btn--primary',
    disabled: true,
    onClick: () => submitAll(),
  }, 'Submit All');

  const footer = h('div', { className: 'question-group__submit' }, [submitProgress, submitBtn]);
  card.appendChild(footer);

  function updateProgress() {
    const filled = items.filter(i => i.hasValue()).length;
    const total = items.length;
    progressEl.textContent = `${filled}/${total} filled`;
    submitProgress.textContent = `${filled} of ${total} answered`;
    submitBtn.disabled = filled < total;
    // Update number badges
    for (const item of items) {
      item.numberEl.classList.toggle('question-group__number--filled', item.hasValue());
    }
  }

  function addItem(qData) {
    const { id, question, context: ctx } = qData;
    if (questionIds.has(id)) return;
    questionIds.add(id);

    const idx = items.length + 1;
    const numberEl = h('span', { className: 'question-group__number' }, String(idx));
    const questionText = h('span', { className: 'question-group__question-text' }, question);

    const itemEl = h('div', { className: 'question-group__item' });
    itemEl.appendChild(h('div', { className: 'question-group__item-header' }, [numberEl, questionText]));

    if (ctx) {
      itemEl.appendChild(h('p', { className: 'text-sm text-secondary', style: { paddingLeft: '32px', marginBottom: '8px' } }, ctx));
    }

    const { inputEl, getValue, hasValue } = buildQuestionInput(qData, {
      onInput: () => updateProgress(),
      wrapClass: 'question-group__input',
    });

    itemEl.appendChild(inputEl);
    list.appendChild(itemEl);

    items.push({ id, data: qData, getValue, hasValue, numberEl });

    // Update counts
    countBadge.textContent = `${items.length} question${items.length > 1 ? 's' : ''}`;
    updateProgress();
  }

  async function submitAll() {
    // Collect answers
    const answers = [];
    for (const item of items) {
      const val = item.getValue();
      if (!val) return; // shouldn't happen, button is disabled
      answers.push({ questionId: item.id, answer: val });
    }

    submitBtn.disabled = true;
    submitBtn.textContent = 'Submitting...';

    try {
      let succeeded = 0;
      let failed = 0;
      for (const { questionId, answer } of answers) {
        try {
          await post(`/admin/api/questions/${questionId}/answer`, { answer });
          succeeded++;
        } catch (err) {
          failed++;
          console.error(`Failed to submit answer for question ${questionId}:`, err);
        }
      }

      if (failed > 0) {
        toast.error(`${failed} answer(s) failed to submit`);
        if (succeeded === 0) {
          submitBtn.disabled = false;
          submitBtn.textContent = 'Submit All';
          return;
        }
      }

      // Clear badge — all questions submitted (or at least attempted)
      state.set('pendingQuestions', Math.max(0, (state.get('pendingQuestions') || 0) - succeeded));

      // Transform to answered state
      card.className = 'chat-card chat-card--question-group chat-card--answered';
      card.innerHTML = '';

      card.appendChild(h('div', { className: 'chat-card__header' }, [
        h('span', { innerHTML: icon('check'), className: 'chat-card__icon chat-card__icon--success' }),
        h('span', { className: 'chat-card__label' }, `${answers.length} Questions Answered`),
      ]));

      const summary = h('div', { className: 'question-group__answered' });
      for (let i = 0; i < items.length; i++) {
        summary.appendChild(h('div', { className: 'question-group__answered-item' }, [
          h('div', { className: 'question-group__answered-q' }, items[i].data.question),
          h('div', { className: 'question-group__answered-a' }, answers[i].answer),
        ]));
      }
      card.appendChild(summary);

      card.appendChild(h('div', { className: 'chat-card__body' }, [
        h('button', {
          className: 'btn btn--ghost btn--sm',
          onClick: () => switchPanel('questions'),
        }, 'View all questions \u2192'),
      ]));

      // Notify for each answered question
      for (const { questionId, answer } of answers) {
        document.dispatchEvent(new CustomEvent('ho:questionAnswered', {
          detail: { questionId, answer },
        }));
      }

      toast.success(`${answers.length} answers submitted`);
    } catch (err) {
      submitBtn.disabled = false;
      submitBtn.textContent = 'Submit All';
      toast.error('Failed to submit answers: ' + err.message);
    }
  }

  // Add initial questions
  for (const q of questions) {
    addItem(q);
  }

  return {
    element: card,
    addQuestion: (data) => {
      addItem(data);
      card.scrollIntoView({ behavior: 'smooth', block: 'end' });
    },
    get questionIds() { return questionIds; },
  };
}

// --- Page card ---
function createPageCard(toolName, status, args, result) {
  const path = args?.path || 'page';
  const pageTitle = args?.title || path;
  const actionLabel = getToolLabel(toolName, args);

  const card = h('div', { className: `chat-card chat-card--page${status === 'running' ? ' chat-card--running' : ''}` }, [
    h('div', { className: 'chat-card__header' }, [
      h('span', { innerHTML: icon('file-text'), className: 'chat-card__icon chat-card__icon--page' }),
      h('span', { className: 'chat-card__label' }, actionLabel),
      createStatusBadge(status),
    ]),
    h('div', { className: 'chat-card__body' }, [
      h('strong', {}, pageTitle),
      h('code', { className: 'text-sm text-secondary ml-2' }, path),
    ]),
    h('div', { className: 'chat-card__actions' }, [
      h('button', {
        className: 'btn btn--ghost btn--sm',
        onClick: () => switchPanel('pages'),
      }, 'View Pages \u2192'),
    ]),
  ]);

  makeCollapsible(card);

  return { element: card, updateStatus: bindStatusUpdater(card) };
}

// --- File type detection ---
const fileTypeMap = {
  image:  { ext: ['png','jpg','jpeg','gif','svg','webp','ico','bmp','avif'], icon: 'image', panel: 'assets', label: 'image' },
  code:   { ext: ['js','ts','jsx','tsx','mjs','json','xml','yml','yaml','toml'], icon: 'code', panel: 'files', label: 'script' },
  style:  { ext: ['css','scss','sass','less'], icon: 'code', panel: 'files', label: 'stylesheet' },
  font:   { ext: ['woff','woff2','ttf','otf','eot'], icon: 'file', panel: 'assets', label: 'font' },
  video:  { ext: ['mp4','webm','ogg','mov'], icon: 'file', panel: 'assets', label: 'video' },
  audio:  { ext: ['mp3','wav','flac','aac','m4a'], icon: 'file', panel: 'assets', label: 'audio' },
};

function getFileType(filename) {
  const ext = (filename || '').split('.').pop()?.toLowerCase() || '';
  for (const type of Object.values(fileTypeMap)) {
    if (type.ext.includes(ext)) return type;
  }
  return { icon: 'file', panel: 'files', label: 'file' };
}

// --- Asset/file card ---
function createAssetCard(toolName, status, args, result) {
  const filename = args?.filename || 'asset';
  const ft = getFileType(filename);
  const actionLabel = getToolLabel(toolName, args);

  const card = h('div', { className: `chat-card chat-card--asset${status === 'running' ? ' chat-card--running' : ''}` }, [
    h('div', { className: 'chat-card__header' }, [
      h('span', { innerHTML: icon(ft.icon), className: 'chat-card__icon chat-card__icon--asset' }),
      h('span', { className: 'chat-card__label' }, actionLabel),
      createStatusBadge(status),
    ]),
    h('div', { className: 'chat-card__body' }, [
      h('strong', {}, filename),
    ]),
    h('div', { className: 'chat-card__actions' }, [
      h('button', {
        className: 'btn btn--ghost btn--sm',
        onClick: () => switchPanel(ft.panel),
      }, ft.panel === 'files' ? 'View Files \u2192' : 'View Assets \u2192'),
    ]),
  ]);

  makeCollapsible(card);

  return { element: card, updateStatus: bindStatusUpdater(card) };
}

// --- Table card ---
function createTableCard(toolName, status, args, result) {
  const tableName = args?.table_name || args?.table || 'table';

  const card = h('div', { className: `chat-card chat-card--table${status === 'running' ? ' chat-card--running' : ''}` }, [
    h('div', { className: 'chat-card__header' }, [
      h('span', { innerHTML: icon('database'), className: 'chat-card__icon chat-card__icon--table' }),
      h('span', { className: 'chat-card__label' }, getToolLabel(toolName, args)),
      createStatusBadge(status),
    ]),
    h('div', { className: 'chat-card__body' }, [
      h('strong', {}, tableName),
    ]),
    h('div', { className: 'chat-card__actions' }, [
      h('button', {
        className: 'btn btn--ghost btn--sm',
        onClick: () => switchPanel('tables'),
      }, 'View Tables \u2192'),
    ]),
  ]);

  makeCollapsible(card);

  return { element: card, updateStatus: bindStatusUpdater(card) };
}

// --- API Endpoint card ---
function createEndpointCard(toolName, status, args, result) {
  const path = args?.path || 'endpoint';
  const tableName = args?.table_name || '';
  const method = args?.method ? args.method.toUpperCase() : '';
  const card = h('div', { className: `chat-card chat-card--endpoint${status === 'running' ? ' chat-card--running' : ''}` }, [
    h('div', { className: 'chat-card__header' }, [
      h('span', { innerHTML: icon('zap'), className: 'chat-card__icon chat-card__icon--endpoint' }),
      h('span', { className: 'chat-card__label' }, getToolLabel(toolName, args)),
      createStatusBadge(status),
    ]),
    h('div', { className: 'chat-card__body' }, [
      method ? h('span', { className: 'badge badge--sm' }, method) : null,
      h('code', { style: { fontSize: '0.85rem' } }, `/api/${path}`),
      tableName ? h('span', { className: 'text-sm text-secondary ml-2' }, `\u2192 ${tableName}`) : null,
    ].filter(Boolean)),
  ]);

  makeCollapsible(card);

  return { element: card, updateStatus: bindStatusUpdater(card) };
}

// Helper to bind a status updater to a card's header badge.
function bindStatusUpdater(card) {
  return (newStatus) => {
    const badge = card.querySelector('.chat-card__header .badge:last-child');
    if (badge) {
      badge.className = newStatus === 'success' ? 'badge badge--success' : 'badge badge--danger';
      badge.textContent = newStatus === 'success' ? 'Done' : 'Error';
    }
    // Remove running shimmer, add flash
    card.classList.remove('chat-card--running');
    const flashClass = newStatus === 'success' ? 'chat-card--success-flash' : 'chat-card--error-flash';
    card.classList.add(flashClass);
    setTimeout(() => card.classList.remove(flashClass), 700);
  };
}

// Helper to create a running/success/error badge
function createStatusBadge(status) {
  if (status === 'success') return h('span', { className: 'badge badge--success' }, 'Done');
  if (status === 'error') return h('span', { className: 'badge badge--danger' }, 'Error');
  return h('span', { className: 'badge badge--info' }, 'Running');
}

/**
 * Wrap a card with collapsible header + detail section.
 * Auto-expands when status is 'running', collapsed otherwise.
 */
function makeCollapsible(card) {
  const header = card.querySelector('.chat-card__header');
  if (!header) return;

  // Add chevron to the front of the header
  const chevron = h('span', { innerHTML: icon('chevron-right'), className: 'chat-card__chevron' });
  header.insertBefore(chevron, header.firstChild);

  // Wrap body + actions in a detail container
  const detail = h('div', { className: 'chat-card__detail' });
  const children = Array.from(card.children).filter(c => c !== header);
  for (const child of children) {
    detail.appendChild(child);
  }
  card.appendChild(detail);

  // Auto-expand if running
  const badge = header.querySelector('.badge');
  const isRunning = badge && badge.classList.contains('badge--info');
  if (isRunning) {
    header.classList.add('expanded');
    detail.classList.add('visible');
  }

  // Toggle on header click
  header.addEventListener('click', () => {
    header.classList.toggle('expanded');
    detail.classList.toggle('visible');
  });
}
