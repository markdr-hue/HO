/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Fetch wrapper with JWT injection and error handling.
 */

// Admin panel HTTP wrapper — uses 'ho_token' for JWT storage.
export const TOKEN_KEY = 'ho_token';

export function getToken() {
  return localStorage.getItem(TOKEN_KEY);
}

async function request(method, url, body) {
  const headers = { 'Content-Type': 'application/json' };
  const token = getToken();
  if (token) {
    headers['Authorization'] = `Bearer ${token}`;
  }

  const opts = { method, headers };
  if (body !== undefined && method !== 'GET') {
    opts.body = JSON.stringify(body);
  }

  const res = await fetch(url, opts);

  // Handle 401 - redirect to login
  if (res.status === 401) {
    localStorage.removeItem(TOKEN_KEY);
    window.location.hash = '#/login';
    throw new Error('Unauthorized');
  }

  // Try to parse JSON
  const text = await res.text();
  let data;
  try {
    data = JSON.parse(text);
  } catch {
    data = text;
  }

  if (!res.ok) {
    const errMsg = data?.error || data?.message || `Request failed (${res.status})`;
    throw new Error(errMsg);
  }

  return data;
}

export function get(url) {
  return request('GET', url);
}

export function post(url, body) {
  return request('POST', url, body);
}

export function put(url, body) {
  return request('PUT', url, body);
}

export function del(url) {
  return request('DELETE', url);
}
