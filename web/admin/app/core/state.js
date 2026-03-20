/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Lightweight reactive store with watchers.
 */

const store = {};
const watchers = {};

/**
 * Get a value from the store.
 */
export function get(key) {
  return store[key];
}

/**
 * Set a value in the store and notify watchers.
 */
export function set(key, value) {
  const old = store[key];
  store[key] = value;

  if (watchers[key]) {
    for (const cb of watchers[key]) {
      cb(value, old);
    }
  }
}

/**
 * Watch for changes to a key. Returns an unwatch function.
 */
export function watch(key, callback) {
  if (!watchers[key]) {
    watchers[key] = [];
  }
  watchers[key].push(callback);

  return () => {
    watchers[key] = watchers[key].filter(cb => cb !== callback);
  };
}

/**
 * Returns true if the current user has the admin role.
 */
export function isAdmin() {
  try {
    const user = JSON.parse(localStorage.getItem('ho_user') || '{}');
    return user.role === 'admin';
  } catch {
    return false;
  }
}
