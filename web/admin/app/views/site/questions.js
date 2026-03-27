/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Site-scoped questions panel.
 * Groups pending questions into a single form with one submit button.
 */

import { h, clear } from '../../core/dom.js';
import { get, post } from '../../core/http.js';
import * as state from '../../core/state.js';
import * as toast from '../../ui/toast.js';
import { emptyState } from '../../ui/helpers.js';
import { createGroupedForm, createApprovalCard, createAnsweredCard } from '../../ui/question-cards.js';

export async function renderSiteQuestions(container, siteId) {
  clear(container);

  const header = h('div', { className: 'context-panel__header' }, [
    h('h3', { className: 'context-panel__title' }, 'Questions'),
  ]);

  const body = h('div', { className: 'context-panel__body' });
  container.appendChild(header);
  container.appendChild(body);

  try {
    const questions = await get(`/admin/api/sites/${siteId}/questions`);
    renderList(questions);
  } catch (err) {
    toast.error('Failed to load questions: ' + err.message);
  }

  function renderList(questions) {
    clear(body);

    if (questions.length === 0) {
      body.appendChild(emptyState('No questions yet. Questions from the AI will appear here.'));
      return;
    }

    const pending = questions.filter(q => q.status === 'pending');
    const answered = questions.filter(q => q.status !== 'pending');
    const pendingRegular = pending.filter(q => q.type !== 'approval');
    const pendingApproval = pending.filter(q => q.type === 'approval');

    if (pendingRegular.length > 0) {
      body.appendChild(createGroupedForm(pendingRegular, {
        onSubmit: async (answers) => {
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
          if (failed > 0) toast.error(`${failed} answer(s) failed to submit`);
          if (succeeded > 0) toast.success(`${succeeded} answer(s) submitted`);
          // Refresh and update badge from actual server state
          const fresh = await get(`/admin/api/sites/${siteId}/questions`);
          const pendingCount = fresh.filter(q => q.status === 'pending').length;
          const map = Object.assign({}, state.get('pendingQuestions') || {});
          map[siteId] = pendingCount;
          state.set('pendingQuestions', map);
          renderList(fresh);
        },
      }));
    }

    for (const q of pendingApproval) {
      body.appendChild(createApprovalCard(q, { onRespond: submitSingle }));
    }

    for (const q of answered) {
      body.appendChild(createAnsweredCard(q));
    }
  }

  async function submitSingle(questionId, answer) {
    try {
      await post(`/admin/api/questions/${questionId}/answer`, { answer });
      toast.success(`Response submitted: ${answer}`);
      const questions = await get(`/admin/api/sites/${siteId}/questions`);
      // Update badge from actual server state
      const pendingCount = questions.filter(q => q.status === 'pending').length;
      const map = Object.assign({}, state.get('pendingQuestions') || {});
      map[siteId] = pendingCount;
      state.set('pendingQuestions', map);
      renderList(questions);
    } catch (err) {
      toast.error(err.message);
    }
  }
}
