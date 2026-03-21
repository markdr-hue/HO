/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Shared question card renderers for both site-scoped and global question views.
 * Provides grouped form, approval card, and answered card components.
 */

import { h } from '../core/dom.js';
import { icon } from './icon.js';
import { buildQuestionInput } from './question-input.js';

/**
 * Create a grouped form card for multiple pending questions.
 *
 * @param {Array} questions — Array of question objects
 * @param {Object} opts
 * @param {Function} opts.onSubmit — Called with array of { questionId, answer } on submit
 * @returns {HTMLElement}
 */
export function createGroupedForm(questions, { onSubmit }) {
  const items = []; // { id, getValue, hasValue, numberEl }

  const card = h('div', { className: 'questions-group-card' });

  const progressEl = h('span', { className: 'text-xs text-secondary', style: { marginLeft: 'auto' } },
    `0/${questions.length} filled`);

  card.appendChild(h('div', { className: 'questions-group-card__header' }, [
    h('span', { className: 'badge badge--warning' }, `${questions.length} pending`),
    h('span', {}, 'Answer all questions below'),
    progressEl,
  ]));

  const listEl = h('div', { className: 'questions-group-card__body' });

  function updateProgress() {
    const filled = items.filter(i => i.hasValue()).length;
    progressEl.textContent = `${filled}/${items.length} filled`;
    submitBtn.disabled = filled < items.length;
    for (const item of items) {
      item.numberEl.classList.toggle('question-group__number--filled', item.hasValue());
    }
  }

  for (let i = 0; i < questions.length; i++) {
    const q = questions[i];
    const numberEl = h('span', { className: 'question-group__number' }, String(i + 1));
    const itemEl = h('div', { className: 'questions-group-card__item' });

    itemEl.appendChild(h('div', { className: 'questions-group-card__item-header' }, [
      numberEl,
      h('div', { style: { flex: '1' } }, [
        h('p', { style: { fontWeight: '600', marginBottom: '4px' } }, q.question),
        q.type === 'secret'
          ? h('span', { className: 'badge badge--info', style: { fontSize: 'var(--text-xs)' } }, 'Secret')
          : null,
      ].filter(Boolean)),
    ]));

    const { inputEl, getValue, hasValue } = buildQuestionInput(q, {
      onInput: updateProgress,
      wrapClass: 'question-group__input',
    });
    itemEl.appendChild(inputEl);
    listEl.appendChild(itemEl);

    items.push({ id: q.id, getValue, hasValue, numberEl });
  }

  card.appendChild(listEl);

  // Attach progress listener to inputs
  listEl.addEventListener('input', updateProgress);
  listEl.addEventListener('click', () => setTimeout(updateProgress, 0));

  const submitBtn = h('button', {
    className: 'btn btn--primary',
    disabled: true,
    onClick: async () => {
      const answers = [];
      for (const item of items) {
        const val = item.getValue();
        if (!val) return;
        answers.push({ questionId: item.id, answer: val });
      }

      submitBtn.disabled = true;
      submitBtn.textContent = 'Submitting...';

      try {
        await onSubmit(answers);
      } catch {
        submitBtn.disabled = false;
        submitBtn.textContent = 'Submit All';
      }
    },
  }, 'Submit All');

  card.appendChild(h('div', { className: 'questions-group-card__footer' }, [
    h('span', { className: 'text-xs text-secondary' }),
    submitBtn,
  ]));

  return card;
}

/**
 * Create an approval card for a question that needs approve/deny.
 *
 * @param {Object} question
 * @param {Object} opts
 * @param {Function} opts.onRespond — Called with (questionId, 'Approve'|'Deny')
 * @returns {HTMLElement}
 */
export function createApprovalCard(question, { onRespond }) {
  return h('div', { className: 'card mb-3 card--approval' }, [
    h('div', { className: 'flex items-center justify-between mb-2' }, [
      h('span', { className: 'badge badge--danger' }, 'Approval Needed'),
      h('span', { className: 'text-xs text-secondary' }, new Date(question.created_at).toLocaleString()),
    ]),
    h('p', { style: { marginBottom: '8px' } }, question.question),
    h('div', { className: 'flex items-center gap-2 mt-3' }, [
      h('button', { className: 'btn btn--success btn--sm', onClick: () => onRespond(question.id, 'Approve') }, 'Approve'),
      h('button', { className: 'btn btn--danger btn--sm', onClick: () => onRespond(question.id, 'Deny') }, 'Deny'),
    ]),
  ]);
}

/**
 * Create a read-only card showing an answered question.
 *
 * @param {Object} question
 * @returns {HTMLElement}
 */
export function createAnsweredCard(question) {
  return h('div', { className: 'card mb-3' }, [
    h('div', { className: 'flex items-center justify-between mb-2' }, [
      h('span', { className: 'badge badge--success' }, 'Answered'),
      h('span', { className: 'text-xs text-secondary' }, new Date(question.created_at).toLocaleString()),
    ]),
    h('p', { style: { marginBottom: '8px' } }, question.question),
    question.answer ? h('div', { style: { opacity: '0.7', fontSize: 'var(--text-sm)' } }, [
      h('strong', {}, 'Answer: '),
      h('span', {}, question.answer),
    ]) : null,
  ].filter(Boolean));
}
