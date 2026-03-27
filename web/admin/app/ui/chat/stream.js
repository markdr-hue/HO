/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Streaming token renderer.
 * Connects to the SSE chat endpoint and handles token/tool events.
 */

import { createStreamingMessage, createToolCall } from './message.js';
import { TOKEN_KEY } from '../../core/http.js';

/**
 * Map common technical error messages to friendly, human-readable ones.
 */
export function humanizeError(msg) {
  if (!msg || typeof msg !== 'string') return msg;

  const mappings = [
    [/UNIQUE constraint failed/i, 'That item already exists. Try a different name or value.'],
    [/no such table/i, 'The data table doesn\'t exist yet. Create it first.'],
    [/no such column/i, 'One of the data fields doesn\'t exist in this table.'],
    [/FOREIGN KEY constraint failed/i, 'This item is referenced by other data and can\'t be removed yet.'],
    [/NOT NULL constraint failed/i, 'A required field is missing. Please provide all necessary values.'],
    [/CHECK constraint failed/i, 'One of the values doesn\'t meet the requirements.'],
    [/database is locked/i, 'The system is busy. Please try again in a moment.'],
    [/context deadline exceeded/i, 'The operation took too long. Please try again.'],
    [/connection refused/i, 'Unable to reach the server. Please check your connection.'],
    [/rate limit/i, 'Too many requests. Please wait a moment before trying again.'],
  ];

  for (const [pattern, friendly] of mappings) {
    if (pattern.test(msg)) return friendly;
  }
  return msg;
}

/**
 * Start streaming a response from the server.
 * @param {string} url - The SSE endpoint URL
 * @param {string} body - JSON body to POST
 * @param {Object} callbacks - { onToken, onToolStart, onToolResult, onDone, onError }
 * @returns {{ abort: Function }}
 */
export function startStream(url, body, callbacks = {}) {
  const token = localStorage.getItem(TOKEN_KEY);
  const controller = new AbortController();

  fetch(url, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${token}`,
    },
    body,
    signal: controller.signal,
  }).then(async (response) => {
    let finished = false;

    function fireDone() {
      if (!finished) {
        finished = true;
        if (callbacks.onDone) callbacks.onDone();
      }
    }

    if (!response.ok) {
      const text = await response.text();
      let msg;
      try {
        msg = JSON.parse(text).error;
      } catch {
        msg = text;
      }
      if (callbacks.onError) callbacks.onError(humanizeError(msg || `HTTP ${response.status}`));
      return;
    }

    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffer = '';

    while (true) {
      const { done, value } = await reader.read();
      if (done) break;

      buffer += decoder.decode(value, { stream: true });

      // Parse SSE events from buffer
      const lines = buffer.split('\n');
      buffer = '';

      let eventType = '';

      for (let i = 0; i < lines.length; i++) {
        const line = lines[i];

        if (line.startsWith('event: ')) {
          eventType = line.slice(7).trim();
        } else if (line.startsWith('data: ')) {
          const data = line.slice(6);

          if (eventType === 'token' || eventType === '') {
            // Backend sends tokens as JSON {"text":"..."}, extract the text
            try {
              const parsed = JSON.parse(data);
              if (callbacks.onToken) callbacks.onToken(parsed.text || data);
            } catch {
              if (callbacks.onToken) callbacks.onToken(data);
            }
          } else if (eventType === 'tool_start') {
            try {
              const parsed = JSON.parse(data);
              if (callbacks.onToolStart) callbacks.onToolStart(parsed);
            } catch (err) { console.warn('Failed to parse tool_start:', err); }
          } else if (eventType === 'tool_result') {
            try {
              const parsed = JSON.parse(data);
              if (callbacks.onToolResult) callbacks.onToolResult(parsed);
            } catch (err) { console.warn('Failed to parse tool_result:', err); }
          } else if (eventType === 'done') {
            fireDone();
          } else if (eventType === 'error') {
            if (callbacks.onError) callbacks.onError(humanizeError(data));
          }

          eventType = '';
        } else if (line === '') {
          // Empty line resets event type
          eventType = '';
        } else {
          // Incomplete line, put back in buffer
          buffer = lines.slice(i).join('\n');
          break;
        }
      }
    }

    // Stream complete — guard prevents double-fire if server sent done event
    fireDone();
  }).catch((err) => {
    if (err.name !== 'AbortError') {
      if (callbacks.onError) callbacks.onError(err.message);
    }
  });

  return {
    abort() {
      controller.abort();
    },
  };
}
