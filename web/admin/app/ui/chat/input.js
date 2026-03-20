/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Chat input bar with auto-expanding textarea.
 */

import { h } from '../../core/dom.js';
import { icon } from '../icon.js';

/**
 * Create a chat input component.
 * @param {Function} onSend - Callback with message text.
 * @returns {{ element: HTMLElement, disable: Function, enable: Function, focus: Function, clear: Function }}
 */
export function createChatInput(onSend) {
  const textarea = h('textarea', {
    className: 'chat-input__textarea',
    placeholder: 'Type a message...',
    rows: '1',
  });

  const sendBtn = h('button', {
    className: 'chat-input__send',
    innerHTML: icon('send'),
    title: 'Send message',
    disabled: true,
  });

  const wrapper = h('div', { className: 'chat-input__wrapper' }, [
    textarea,
    sendBtn,
  ]);

  const element = h('div', { className: 'chat-input' }, [wrapper]);

  // Auto-expand textarea
  function autoResize() {
    textarea.style.height = 'auto';
    const MAX_INPUT_HEIGHT = 150;
    textarea.style.height = Math.min(textarea.scrollHeight, MAX_INPUT_HEIGHT) + 'px';
    sendBtn.disabled = !textarea.value.trim();
  }

  textarea.addEventListener('input', autoResize);

  // Handle send
  function doSend() {
    const text = textarea.value.trim();
    if (!text) return;
    onSend(text);
    textarea.value = '';
    autoResize();
  }

  // Enter to send, Shift+Enter for newline
  textarea.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      doSend();
    }
  });

  sendBtn.addEventListener('click', doSend);

  return {
    element,
    disable() {
      textarea.disabled = true;
      sendBtn.disabled = true;
    },
    enable() {
      textarea.disabled = false;
      sendBtn.disabled = !textarea.value.trim();
    },
    focus() {
      textarea.focus();
    },
    clear() {
      textarea.value = '';
      autoResize();
    },
  };
}
