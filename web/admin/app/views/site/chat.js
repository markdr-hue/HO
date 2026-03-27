/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Site chat view — always-visible in the left panel of the two-panel layout.
 * Shows both user-initiated chat and brain activity in a merged timeline.
 * Includes inline question cards, pinned question banner, brain status bar,
 * and answer acknowledgment flow.
 */

import { h, render } from '../../core/dom.js';
import { get } from '../../core/http.js';
import * as state from '../../core/state.js';
import { createFeed } from '../../ui/chat/feed.js';
import { createUserMessage, createAutomatedMessage, createAssistantMessage, createBrainMessage, createStreamingMessage, createToolCall, createStageTransition } from '../../ui/chat/message.js';
import { createQuestionCard, createQuestionGroup } from '../../ui/chat/cards.js';
import { startStream } from '../../ui/chat/stream.js';
import { createChatInput } from '../../ui/chat/input.js';
import { createPipelineTracker } from '../../ui/chat/pipeline-tracker.js';
import { icon } from '../../ui/icon.js';
import { formatDuration } from '../../ui/helpers.js';
import * as toast from '../../ui/toast.js';
import { isForSite } from '../../core/utils.js';

export function renderSiteChat(container, siteId) {
  const feed = createFeed();
  const tracker = createPipelineTracker();
  let currentStream = null;
  let streamingMsg = null;
  let emptyState = null;
  const unwatchers = [];
  const questionCards = {}; // Track question cards by ID
  let questionData = {};    // Track question metadata for banner
  let activeQuestionGroup = null; // Active grouped question card

  // --- Pinned question banner (between feed and input) ---
  const questionBanner = h('div', { className: 'chat-question-banner' });
  questionBanner.style.display = 'none';

  function updateQuestionBanner() {
    const pendingIds = Object.keys(questionCards);
    if (pendingIds.length === 0) {
      questionBanner.style.display = 'none';
      tracker.updateBrainStatus('idle');
      return;
    }

    questionBanner.style.display = '';
    questionBanner.innerHTML = '';

    const count = pendingIds.length;
    const badge = h('span', { className: 'badge badge--warning' },
      count > 1 ? `${count} questions` : 'Question');

    const textEl = h('div', { className: 'chat-question-banner__text' },
      count > 1 ? `${count} questions need your input` : (questionData[pendingIds[0]]?.question || 'The brain needs your input'));

    // Scroll-to-group button
    const scrollBtn = h('button', {
      className: 'btn btn--sm btn--primary',
      onClick: () => {
        if (activeQuestionGroup?.element) {
          activeQuestionGroup.element.scrollIntoView({ behavior: 'smooth', block: 'center' });
        } else {
          const latestId = pendingIds[pendingIds.length - 1];
          const card = questionCards[latestId];
          if (card?.element) card.element.scrollIntoView({ behavior: 'smooth', block: 'center' });
        }
      },
    }, 'Answer');

    questionBanner.appendChild(h('span', { innerHTML: icon('help-circle'), className: 'chat-question-banner__icon' }));
    questionBanner.appendChild(badge);
    questionBanner.appendChild(textEl);
    questionBanner.appendChild(scrollBtn);

    tracker.updateBrainStatus('waiting');
  }

  // --- Chat input ---
  const chatInput = createChatInput((text) => {
    sendMessage(text);
  });

  // --- Layout ---
  const chatContainer = h('div', { className: 'chat-container' }, [
    tracker.element,
    feed.element,
    questionBanner,
    chatInput.element,
  ]);

  render(container, chatContainer);

  // Load history, then load pending questions
  loadHistory().then(() => loadPendingQuestions());
  chatInput.focus();

  // Subscribe to live brain events for this site
  subscribeBrainEvents();

  // Listen for question answers from cards.js
  function onQuestionAnswered(e) {
    const { questionId } = e.detail;

    delete questionCards[questionId];
    delete questionData[questionId];

    // Clear group reference if all group questions are answered
    if (activeQuestionGroup && activeQuestionGroup.questionIds) {
      const groupStillPending = [...activeQuestionGroup.questionIds].some(id => questionCards[id]);
      if (!groupStillPending) {
        activeQuestionGroup = null;
      }
    }

    updateQuestionBanner();
  }
  document.addEventListener('ho:questionAnswered', onQuestionAnswered);

  // --- Empty state ---
  function showEmptyState() {
    emptyState = h('div', { className: 'chat-empty' }, [
      h('div', { className: 'chat-empty__icon', innerHTML: icon('brain') }),
      h('h3', { className: 'chat-empty__title' }, 'Your AI brain is waking up'),
      h('p', { className: 'chat-empty__text' },
        'The brain will autonomously build your website based on your description. Messages, questions, and build activity will appear here.'
      ),
      h('div', { className: 'chat-empty__hints' }, [
        h('div', { className: 'chat-empty__hint' }, [
          h('span', { innerHTML: icon('message-circle'), className: 'chat-empty__hint-icon' }),
          h('span', {}, 'Ask questions about your project anytime'),
        ]),
        h('div', { className: 'chat-empty__hint' }, [
          h('span', { innerHTML: icon('help-circle'), className: 'chat-empty__hint-icon' }),
          h('span', {}, 'The brain will ask when it needs your input'),
        ]),
        h('div', { className: 'chat-empty__hint' }, [
          h('span', { innerHTML: icon('activity'), className: 'chat-empty__hint-icon' }),
          h('span', {}, 'Build progress shows in real-time'),
        ]),
      ]),
    ]);
    feed.append(emptyState);
  }

  function removeEmptyState() {
    if (emptyState) {
      emptyState.remove();
      emptyState = null;
    }
  }

  let oldestTimestamp = null;
  let hasOlderMessages = true;
  let loadingEarlier = false;

  function renderMessages(messages, prepend = false) {
    // First pass: build a map of tool_call_id → result content from role="tool" messages
    const toolResults = {};
    for (const msg of messages) {
      if (msg.role === 'tool' && msg.tool_call_id) {
        toolResults[msg.tool_call_id] = msg.content;
      }
    }

    const elements = [];
    // Second pass: render messages, looking up tool results from the map
    for (const msg of messages) {
      if (msg.role === 'user') {
        if (msg.session_id === 'brain') {
          elements.push(createAutomatedMessage(msg.content, msg.created_at));
        } else {
          elements.push(createUserMessage(msg.content, msg.created_at));
        }
      } else if (msg.role === 'assistant') {
        const isBrain = msg.session_id === 'brain';
        if (isBrain) {
          if (msg.content) elements.push(createBrainMessage(msg.content, msg.created_at));
        } else {
          if (msg.content) elements.push(createAssistantMessage(msg.content, msg.created_at));
        }
        if (msg.tool_calls && msg.tool_calls.length > 0) {
          for (const tc of msg.tool_calls) {
            const name = tc.name || tc.function?.name || 'tool';
            let args = {};
            try {
              if (tc.arguments) args = typeof tc.arguments === 'string' ? JSON.parse(tc.arguments) : tc.arguments;
            } catch { /* ignore */ }
            // Skip question tools in history — shown as interactive question cards instead
            if (name === 'ask_question' || (name === 'manage_communication' && args.action === 'ask')) continue;
            const result = toolResults[tc.id];
            const toolEl = createToolCall(name, 'success', result, args);
            elements.push(toolEl.element);
          }
        }
      }
      // Skip role="tool" — results are shown inline with their parent assistant's tool calls
    }

    if (prepend) {
      const existing = feed.element.querySelector('.load-earlier-btn');
      if (existing) existing.remove();
      const firstChild = feed.element.firstChild;
      for (const el of elements) {
        feed.element.insertBefore(el, firstChild);
      }
    } else {
      for (const el of elements) {
        feed.append(el);
      }
    }
  }

  async function loadHistory() {
    try {
      const messages = await get(`/admin/api/chat/${siteId}/history`);

      if (!messages || !Array.isArray(messages) || messages.length === 0) {
        showEmptyState();
        hasOlderMessages = false;
        return;
      }

      if (messages.length > 0) {
        oldestTimestamp = messages[0].created_at;
        hasOlderMessages = messages.length >= 50;
      }

      // Parse stage state from history so tracker shows correct position on load
      for (const msg of messages) {
        if (msg.session_id === 'brain' && msg.content) {
          const sm = msg.content.match(/Starting stage: \*\*(\w+)\*\*/);
          if (sm) tracker.setStage(sm[1]);
          const bm = msg.content.match(/Plan ready: (\d+) pages?/);
          if (bm) tracker.updateBuildStat('totalPages', parseInt(bm[1], 10));
        }
      }

      renderMessages(messages);

      if (hasOlderMessages) {
        addLoadEarlierButton();
      }

      feed.scrollToBottom(true);
    } catch {
      // Silently handle history load failures
    }
  }

  function addLoadEarlierButton() {
    const btn = h('div', {
      className: 'load-earlier-btn',
      style: { textAlign: 'center', padding: '0.5rem' },
    }, [
      h('button', {
        className: 'btn btn--ghost btn--sm',
        onClick: loadEarlierMessages,
      }, 'Load earlier messages'),
    ]);
    feed.element.insertBefore(btn, feed.element.firstChild);
  }

  async function loadEarlierMessages() {
    if (!oldestTimestamp || !hasOlderMessages || loadingEarlier) return;
    loadingEarlier = true;

    const btnEl = feed.element.querySelector('.load-earlier-btn button');
    if (btnEl) {
      btnEl.textContent = 'Loading...';
      btnEl.disabled = true;
    }

    try {
      const messages = await get(`/admin/api/chat/${siteId}/history?before=${encodeURIComponent(oldestTimestamp)}`);
      if (!messages || messages.length === 0) {
        hasOlderMessages = false;
        const existing = feed.element.querySelector('.load-earlier-btn');
        if (existing) existing.remove();
        return;
      }
      oldestTimestamp = messages[0].created_at;
      hasOlderMessages = messages.length >= 50;

      const prevHeight = feed.element.scrollHeight;
      renderMessages(messages, true);
      feed.element.scrollTop += feed.element.scrollHeight - prevHeight;

      if (hasOlderMessages) {
        addLoadEarlierButton();
      }
    } catch {
      if (btnEl) {
        btnEl.textContent = 'Load earlier messages';
        btnEl.disabled = false;
      }
    } finally {
      loadingEarlier = false;
    }
  }

  async function loadPendingQuestions() {
    try {
      const questions = await get(`/admin/api/sites/${siteId}/questions?status=pending`);
      if (questions && questions.length > 0) {
        removeEmptyState();
        const approval = questions.filter(q => q.type === 'approval');
        const regular = questions.filter(q => q.type !== 'approval');

        if (regular.length > 0) {
          activeQuestionGroup = createQuestionGroup(regular);
          for (const q of regular) {
            questionCards[q.id] = activeQuestionGroup;
            questionData[q.id] = q;
          }
          feed.append(activeQuestionGroup.element);
        }

        for (const q of approval) {
          if (!questionCards[q.id]) {
            const qCard = createQuestionCard(q);
            questionCards[q.id] = qCard;
            questionData[q.id] = q;
            feed.append(qCard.element);
          }
        }

        feed.scrollToBottom();
        updateQuestionBanner();
      }
    } catch {
      // Questions endpoint may not have data yet
    }
  }

  function subscribeBrainEvents() {
    const brainToolCards = {};

    unwatchers.push(state.watch('brainMessage', (data) => {
      if (!isForSite(data, siteId)) return;
      if (data.content) {
        removeEmptyState();

        // Parse stage start messages to drive the tracker
        const stageMatch = data.content.match(/Starting stage: \*\*(\w+)\*\*/);
        if (stageMatch) {
          tracker.setStage(stageMatch[1]);
          tracker.setDetail('');
        }

        // Parse build progress patterns
        const planMatch = data.content.match(/Plan ready: (\d+) pages?, (\d+) endpoints?, (\d+) tables?/);
        if (planMatch) {
          tracker.updateBuildStat('totalPages', parseInt(planMatch[1], 10));
        }
        const buildDoneMatch = data.content.match(/Build complete: (\d+) tool calls/);
        if (buildDoneMatch) {
          tracker.updateBuildStat('toolCalls', parseInt(buildDoneMatch[1], 10));
        }

        // Parse phase detail
        const phaseMatch = data.content.match(/^Phase (\d+)\/(\d+):/);
        if (phaseMatch) {
          tracker.setDetail(data.content.replace(/\*\*/g, ''));
        }

        feed.append(createBrainMessage(data.content));
        feed.scrollToBottom();
        tracker.updateBrainStatus('idle');
      }
    }));

    // Watch for structured stage change events
    unwatchers.push(state.watch('brainStageChange', (data) => {
      if (!isForSite(data, siteId)) return;
      const { prev_stage, stage, duration_ms } = data;

      if (prev_stage) tracker.markCompleted(prev_stage);
      if (stage) tracker.setStage(stage);

      if (prev_stage) {
        const dur = duration_ms ? formatDuration(duration_ms) : '';
        feed.append(createStageTransition(prev_stage, dur));
        feed.scrollToBottom();
      }
    }));

    unwatchers.push(state.watch('brainToolStart', (data) => {
      if (!isForSite(data, siteId)) return;
      const toolName = data.tool || data.name || 'tool';
      const isQuestion = toolName === 'ask_question' || (toolName === 'manage_communication' && data.args?.action === 'ask');
      if (isQuestion) return;
      removeEmptyState();
      const tc = createToolCall(toolName, 'running', null, data.args || {});
      const key = data.call_id || data.tool || data.name;
      brainToolCards[key] = tc;
      feed.append(tc.element);
      feed.scrollToBottom();
    }));

    unwatchers.push(state.watch('brainToolResult', (data) => {
      if (!isForSite(data, siteId)) return;
      const toolName = data.tool || data.name || 'tool';
      const isQuestion = toolName === 'ask_question' || (toolName === 'manage_communication' && data.args?.action === 'ask');
      if (isQuestion) return;
      const key = data.call_id || data.tool || data.name;
      if (brainToolCards[key]) {
        brainToolCards[key].updateStatus(
          data.error ? 'error' : 'success',
          data.result || data.error
        );
      }

      // Track build counters
      if (!data.error && tracker.currentStageKey === 'BUILD') {
        const action = data.args?.action;
        if (toolName === 'manage_pages' && action === 'save') {
          tracker.incrementBuildStat('pages');
        } else if (toolName === 'manage_schema' && action === 'create') {
          tracker.incrementBuildStat('tables');
        } else if (toolName === 'manage_endpoints' && (action === 'create_api' || action === 'create_auth')) {
          tracker.incrementBuildStat('endpoints');
        }
      }

      feed.scrollToBottom();
    }));

    // Watch for new questions asked by brain
    unwatchers.push(state.watch('questionAsked', (data) => {
      if (!isForSite(data, siteId)) return;
      if (data.question && !questionCards[data.id]) {
        removeEmptyState();

        if (data.type === 'approval') {
          const qCard = createQuestionCard(data);
          questionCards[data.id] = qCard;
          questionData[data.id] = data;
          feed.append(qCard.element);
        } else if (activeQuestionGroup) {
          activeQuestionGroup.addQuestion(data);
          questionCards[data.id] = activeQuestionGroup;
          questionData[data.id] = data;
        } else {
          activeQuestionGroup = createQuestionGroup([data]);
          questionCards[data.id] = activeQuestionGroup;
          questionData[data.id] = data;
          feed.append(activeQuestionGroup.element);
        }

        feed.scrollToBottom();
        updateQuestionBanner();
      }
    }));

    // Watch for build progress updates
    unwatchers.push(state.watch('brainProgress', (data) => {
      if (!isForSite(data, siteId)) return;
      if (tracker.currentStageKey === 'BUILD') {
        tracker.setDetail(`${data.pages_done || 0}/${data.pages_total || 0} pages, ${data.endpoints_done || 0}/${data.endpoints_total || 0} endpoints, ${data.tables_done || 0}/${data.tables_total || 0} tables`);
      }
    }));

    // Watch for brain mode changes (e.g., entering monitoring)
    unwatchers.push(state.watch('brainModeChanged', (data) => {
      if (!isForSite(data, siteId)) return;
      if (data.mode === 'monitoring') {
        tracker.markCompleted('PLAN');
        tracker.markCompleted('BUILD');
        tracker.markCompleted('VALIDATE');
        tracker.markCompleted('COMPLETE');
        tracker.setStage('MONITORING');
      } else if (data.mode === 'paused') {
        if (tracker.currentStageKey) {
          tracker.setError(tracker.currentStageKey);
          tracker.setDetail('Pipeline paused');
        }
      }
    }));

    // Watch for questions answered elsewhere
    unwatchers.push(state.watch('questionAnswered', (data) => {
      if (!data) return;
      const qId = data.question_id;
      if (questionCards[qId]) {
        questionCards[qId].element.remove();
        delete questionCards[qId];
        delete questionData[qId];
        updateQuestionBanner();
      }
    }));
  }

  function sendMessage(text) {
    removeEmptyState();
    feed.append(createUserMessage(text));
    chatInput.disable();

    streamingMsg = createStreamingMessage();
    feed.append(streamingMsg.element);

    const toolCards = {};

    currentStream = startStream(
      `/admin/api/chat/${siteId}/stream`,
      JSON.stringify({ message: text, session_id: 'admin' }),
      {
        onToken(token) {
          if (streamingMsg) {
            streamingMsg.appendText(token);
            feed.scrollToBottom();
          }
        },
        onToolStart(data) {
          const tc = createToolCall(data.name || data.tool, 'running', null, data.args || {});
          toolCards[data.id || data.name] = tc;
          feed.append(tc.element);
          feed.scrollToBottom();
        },
        onToolResult(data) {
          const id = data.id || data.name;
          if (toolCards[id]) {
            toolCards[id].updateStatus(
              data.error ? 'error' : 'success',
              data.result || data.error
            );
          }
          feed.scrollToBottom();
        },
        onDone() {
          if (streamingMsg) {
            streamingMsg.finish();
            streamingMsg = null;
          }
          chatInput.enable();
          chatInput.focus();
          currentStream = null;
        },
        onError(err) {
          if (streamingMsg) {
            streamingMsg.appendText(`\n\n*Error: ${err}*`);
            streamingMsg.finish();
            streamingMsg = null;
          }
          toast.error('Chat error: ' + err);
          chatInput.enable();
          chatInput.focus();
          currentStream = null;
        },
      }
    );
  }

  return function cleanup() {
    if (currentStream) currentStream.abort();
    tracker.cleanup();
    document.removeEventListener('ho:questionAnswered', onQuestionAnswered);
    for (const unwatch of unwatchers) {
      unwatch();
    }
  };
}
