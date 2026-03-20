/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Chat feed container with auto-scroll behavior.
 */

import { h, clear } from '../../core/dom.js';

/**
 * Create a chat feed container.
 * @returns {{ element: HTMLElement, append: Function, clear: Function, scrollToBottom: Function }}
 */
export function createFeed() {
  const element = h('div', { className: 'chat-messages' });

  let autoScroll = true;

  // Track scroll position to decide auto-scroll
  element.addEventListener('scroll', () => {
    const { scrollTop, scrollHeight, clientHeight } = element;
    autoScroll = scrollHeight - scrollTop - clientHeight < 100;
  });

  function scrollToBottom(force = false) {
    if (autoScroll || force) {
      requestAnimationFrame(() => {
        element.scrollTop = element.scrollHeight;
      });
    }
  }

  function append(messageEl) {
    messageEl.classList.add('chat-enter');
    element.appendChild(messageEl);
    // Trigger animation after paint
    requestAnimationFrame(() => {
      requestAnimationFrame(() => {
        messageEl.classList.remove('chat-enter');
      });
    });
    scrollToBottom();
  }

  function clearFeed() {
    clear(element);
  }

  return {
    element,
    append,
    clear: clearFeed,
    scrollToBottom,
  };
}
