/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Pipeline stage tracker widget.
 * Displays a visual progress bar for the brain pipeline stages:
 * PLAN → BUILD → COMPLETE → MONITORING
 */

import { h } from '../../core/dom.js';
import { icon } from '../icon.js';

const STAGES = [
  { key: 'PLAN', label: 'Plan', icon: 'search' },
  { key: 'BUILD', label: 'Build', icon: 'zap' },
  { key: 'VALIDATE', label: 'Validate', icon: 'shield' },
  { key: 'COMPLETE', label: 'Complete', icon: 'check' },
  { key: 'MONITORING', label: 'Monitor', icon: 'activity' },
];

const STAGE_ALIASES = { UPDATE_PLAN: 'PLAN' };

export function createPipelineTracker() {
  let visible = false;
  let currentStageKey = null;
  let completedStages = new Set();
  let errorStage = null;
  const pendingTimers = [];

  const trackerEl = h('div', { className: 'pipeline-tracker' });
  trackerEl.style.display = 'none';

  const trackerNodes = {};
  const trackerLines = {};

  const stagesRow = h('div', { className: 'pipeline-tracker__stages' });
  STAGES.forEach((s, i) => {
    if (i > 0) {
      const line = h('div', { className: 'pipeline-tracker__line' });
      stagesRow.appendChild(line);
      trackerLines[s.key] = line;
    }
    const circle = h('div', { className: 'pipeline-tracker__circle' });
    circle.innerHTML = icon(s.icon);
    const label = h('span', { className: 'pipeline-tracker__label' }, s.label);
    const node = h('div', { className: 'pipeline-tracker__node' }, [circle, label]);
    stagesRow.appendChild(node);
    trackerNodes[s.key] = { node, circle, label, originalIcon: s.icon };
  });
  trackerEl.appendChild(stagesRow);

  function show() {
    if (!visible) {
      visible = true;
      trackerEl.style.display = '';
    }
  }

  function hide() {
    visible = false;
    trackerEl.style.display = 'none';
  }

  function reset() {
    hide();
    currentStageKey = null;
    completedStages = new Set();
    errorStage = null;
    pendingTimers.forEach(clearTimeout);
    pendingTimers.length = 0;
    for (const s of STAGES) {
      const { circle, label, originalIcon } = trackerNodes[s.key];
      circle.className = 'pipeline-tracker__circle';
      circle.innerHTML = icon(originalIcon);
      label.className = 'pipeline-tracker__label';
      if (trackerLines[s.key]) trackerLines[s.key].className = 'pipeline-tracker__line';
    }
  }

  /** Animate a stage to its completed state with a bounce + checkmark swap. */
  function animateCompletion(stageKey) {
    const { circle, label } = trackerNodes[stageKey];

    // Bounce
    circle.className = 'pipeline-tracker__circle pipeline-tracker__circle--completing';
    label.className = 'pipeline-tracker__label pipeline-tracker__label--done';

    // Swap icon to checkmark
    circle.innerHTML = icon('check');

    // After bounce finishes, settle into done state
    const timer = setTimeout(() => {
      circle.className = 'pipeline-tracker__circle pipeline-tracker__circle--done';
    }, 380);
    pendingTimers.push(timer);
  }

  function setStage(stageKey) {
    stageKey = STAGE_ALIASES[stageKey] || stageKey;
    currentStageKey = stageKey;
    show();

    const idx = STAGES.findIndex(s => s.key === stageKey);
    if (idx < 0) return;

    // Mark all earlier stages as completed
    for (let i = 0; i < idx; i++) {
      completedStages.add(STAGES[i].key);
    }

    for (let i = 0; i < STAGES.length; i++) {
      const s = STAGES[i];
      const { circle, label, originalIcon } = trackerNodes[s.key];

      if (completedStages.has(s.key)) {
        // Already done — skip if currently in bounce animation
        if (!circle.classList.contains('pipeline-tracker__circle--completing')) {
          circle.className = 'pipeline-tracker__circle pipeline-tracker__circle--done';
          label.className = 'pipeline-tracker__label pipeline-tracker__label--done';
          circle.innerHTML = icon('check');
        }
      } else if (s.key === stageKey && errorStage !== stageKey) {
        // Current active stage
        const modifier = stageKey === 'MONITORING'
          ? 'pipeline-tracker__circle--monitoring'
          : 'pipeline-tracker__circle--active';
        circle.className = 'pipeline-tracker__circle ' + modifier;
        label.className = stageKey === 'MONITORING'
          ? 'pipeline-tracker__label pipeline-tracker__label--done'
          : 'pipeline-tracker__label pipeline-tracker__label--active';
        circle.innerHTML = icon(originalIcon);
      } else if (s.key === errorStage) {
        circle.className = 'pipeline-tracker__circle pipeline-tracker__circle--error';
        label.className = 'pipeline-tracker__label';
        circle.innerHTML = icon(originalIcon);
      } else {
        // Future stage — reset
        circle.className = 'pipeline-tracker__circle';
        label.className = 'pipeline-tracker__label';
        circle.innerHTML = icon(originalIcon);
      }

      // Connector lines
      if (trackerLines[s.key]) {
        trackerLines[s.key].className = 'pipeline-tracker__line';
        if (completedStages.has(s.key) || (i <= idx && completedStages.has(STAGES[i - 1]?.key))) {
          trackerLines[s.key].classList.add('pipeline-tracker__line--done');
        }
      }
    }

    // Shimmer on the line right after the active stage
    const nextStage = STAGES[idx + 1];
    if (nextStage && trackerLines[nextStage.key] && stageKey !== 'MONITORING') {
      trackerLines[nextStage.key].classList.add('pipeline-tracker__line--active');
    }
  }

  function markCompleted(stageKey) {
    const norm = STAGE_ALIASES[stageKey] || stageKey;
    const wasNew = !completedStages.has(norm);
    completedStages.add(norm);
    if (wasNew) {
      animateCompletion(norm);
    }
  }

  function setError(stageKey) {
    errorStage = stageKey;
    if (currentStageKey) setStage(currentStageKey);
  }

  return {
    element: trackerEl,
    setStage,
    show,
    hide,
    reset,
    markCompleted,
    setError,
    setDetail() {},
    // No-op stubs so callers don't break.
    updateBuildStat() {},
    incrementBuildStat() {},
    updateBrainStatus() {},
    cleanup() {},
    get currentStageKey() { return currentStageKey; },
  };
}
