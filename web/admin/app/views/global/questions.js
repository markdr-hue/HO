/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Questions management view.
 * Groups pending questions per site with one submit button per group.
 */

import { h, clear } from '../../core/dom.js';
import { get, post } from '../../core/http.js';
import { icon } from '../../ui/icon.js';
import * as toast from '../../ui/toast.js';
import { emptyState } from '../../ui/helpers.js';
import { createGroupedForm, createApprovalCard, createAnsweredCard } from '../../ui/question-cards.js';

export async function renderQuestions(container) {
  clear(container);

  const header = h('div', { className: 'context-panel__header context-panel__header--page' }, [
    h('h3', { className: 'context-panel__title context-panel__title--page' }, 'Questions'),
  ]);

  const listContainer = h('div', { className: 'context-panel__body context-panel__body--page' });

  container.appendChild(header);
  container.appendChild(listContainer);

  async function loadQuestions() {
    try {
      const questions = await get('/admin/api/questions');
      renderList(questions);
    } catch (err) {
      toast.error('Failed to load questions: ' + err.message);
    }
  }

  function renderList(questions) {
    clear(listContainer);

    if (questions.length === 0) {
      listContainer.appendChild(emptyState('No questions yet.'));
      return;
    }

    // Group by site
    const bySite = {};
    for (const q of questions) {
      (bySite[q.site_id] ??= []).push(q);
    }

    for (const [siteId, siteQuestions] of Object.entries(bySite)) {
      const pending = siteQuestions.filter(q => q.status === 'pending');
      const answered = siteQuestions.filter(q => q.status !== 'pending');
      const pendingRegular = pending.filter(q => q.type !== 'approval');
      const pendingApproval = pending.filter(q => q.type === 'approval');

      // Site section header
      listContainer.appendChild(h('div', { className: 'flex items-center gap-2 mb-3 mt-4' }, [
        h('span', { innerHTML: icon('globe') }),
        h('h4', {}, `Site #${siteId}`),
        pending.length > 0
          ? h('span', { className: 'badge badge--warning' }, `${pending.length} pending`)
          : null,
      ].filter(Boolean)));

      if (pendingRegular.length > 0) {
        listContainer.appendChild(createGroupedForm(pendingRegular, {
          onSubmit: async (answers) => {
            for (const { questionId, answer } of answers) {
              await post(`/admin/api/questions/${questionId}/answer`, { answer });
            }
            toast.success(`${answers.length} answers submitted`);
            loadQuestions();
          },
        }));
      }

      for (const q of pendingApproval) {
        listContainer.appendChild(createApprovalCard(q, { onRespond: submitSingle }));
      }

      for (const q of answered) {
        listContainer.appendChild(createAnsweredCard(q));
      }
    }
  }

  async function submitSingle(questionId, answer) {
    try {
      await post(`/admin/api/questions/${questionId}/answer`, { answer });
      toast.success(`Response submitted: ${answer}`);
      loadQuestions();
    } catch (err) {
      toast.error(err.message);
    }
  }

  loadQuestions();
}
