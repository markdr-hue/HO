/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * SSE client with auto-reconnect and exponential backoff.
 */

export class SSEClient {
  constructor() {
    this._source = null;
    this._url = null;
    this._handlers = {};
    this._reconnectAttempts = 0;
    this._maxReconnectDelay = 30000;
    this._baseDelay = 1000;
    this._reconnectTimer = null;
    this._closed = false;
  }

  /**
   * Connect to an SSE endpoint.
   */
  connect(url) {
    this._url = url;
    this._closed = false;
    this._doConnect();
  }

  _doConnect() {
    if (this._closed) return;

    // Auth via HttpOnly cookie — no token in URL.
    this._source = new EventSource(this._url, { withCredentials: true });

    this._source.onopen = () => {
      this._reconnectAttempts = 0;
    };

    this._source.onerror = () => {
      this._source.close();
      this._scheduleReconnect();
    };

    // Register named event listeners
    for (const [eventType, callbacks] of Object.entries(this._handlers)) {
      for (const cb of callbacks) {
        this._source.addEventListener(eventType, (e) => {
          try {
            const data = JSON.parse(e.data);
            cb(data);
          } catch {
            cb(e.data);
          }
        });
      }
    }

    // Also listen for generic message events
    this._source.onmessage = (e) => {
      const cbs = this._handlers['message'] || [];
      for (const cb of cbs) {
        try {
          const data = JSON.parse(e.data);
          cb(data);
        } catch {
          cb(e.data);
        }
      }
    };
  }

  _scheduleReconnect() {
    if (this._closed) return;
    const delay = Math.min(
      this._baseDelay * Math.pow(2, this._reconnectAttempts),
      this._maxReconnectDelay
    );
    this._reconnectAttempts++;
    this._reconnectTimer = setTimeout(() => this._doConnect(), delay);
  }

  /**
   * Register a callback for a specific event type.
   */
  on(eventType, callback) {
    if (!this._handlers[eventType]) {
      this._handlers[eventType] = [];
    }
    if (this._handlers[eventType].includes(callback)) return;
    this._handlers[eventType].push(callback);

  }

  /**
   * Disconnect and stop reconnecting.
   */
  disconnect() {
    this._closed = true;
    if (this._reconnectTimer) {
      clearTimeout(this._reconnectTimer);
      this._reconnectTimer = null;
    }
    if (this._source) {
      this._source.close();
      this._source = null;
    }
    this._handlers = {};
  }
}
