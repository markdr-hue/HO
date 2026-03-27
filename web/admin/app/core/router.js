/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Hash-based SPA router with path parameter support.
 */

const routes = [];
let notFoundHandler = null;

/**
 * Register a route pattern with a handler function.
 * Supports :param style path parameters, e.g. /sites/:id
 */
export function register(path, handler) {
  const paramNames = [];
  const pattern = path.replace(/:([^/]+)/g, (_, name) => {
    paramNames.push(name);
    return '([^/]+)';
  });
  const regex = new RegExp(`^${pattern}$`);
  routes.push({ path, regex, paramNames, handler });
}

/**
 * Set a handler for unmatched routes.
 */
export function setNotFound(handler) {
  notFoundHandler = handler;
}

/**
 * Navigate to a hash path programmatically.
 */
export function navigate(path) {
  window.location.hash = path;
}

/**
 * Get the current hash path.
 */
export function currentPath() {
  return window.location.hash.slice(1) || '/';
}

/**
 * Resolve the current hash and invoke the matching handler.
 */
function resolve() {
  const path = currentPath();

  for (const route of routes) {
    const match = path.match(route.regex);
    if (match) {
      const params = {};
      let valid = true;
      route.paramNames.forEach((name, i) => {
        const val = decodeURIComponent(match[i + 1]);
        // Validate numeric params (id, siteId, etc.)
        if (/id$/i.test(name) && !/^\d+$/.test(val)) {
          valid = false;
        }
        params[name] = val;
      });
      if (!valid) {
        if (notFoundHandler) notFoundHandler();
        return;
      }
      route.handler(params);
      return;
    }
  }

  if (notFoundHandler) {
    notFoundHandler();
  }
}

/**
 * Start listening for hash changes.
 */
export function start() {
  window.addEventListener('hashchange', resolve);
  resolve();
}

/**
 * Extract the active site ID from the current hash path.
 * Returns null if not on a site route.
 */
export function getActiveSiteId() {
  const match = currentPath().match(/^\/sites\/(\d+)/);
  return match ? parseInt(match[1]) : null;
}
