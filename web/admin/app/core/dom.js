/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

/**
 * Minimal DOM helper utilities.
 */

/**
 * Create an element with attributes and children.
 * @param {string} tag - Tag name
 * @param {Object} [attrs] - Attributes/properties object
 * @param {Array|string|Element} [children] - Children elements, strings, or arrays thereof
 * @returns {HTMLElement}
 */
export function h(tag, attrs, children) {
  const el = document.createElement(tag);

  if (attrs) {
    for (const [key, value] of Object.entries(attrs)) {
      if (key === 'className') {
        el.className = value;
      } else if (key === 'style' && typeof value === 'object') {
        Object.assign(el.style, value);
      } else if (key.startsWith('on') && typeof value === 'function') {
        const event = key.slice(2).toLowerCase();
        el.addEventListener(event, value);
      } else if (key === 'dataset') {
        for (const [dk, dv] of Object.entries(value)) {
          el.dataset[dk] = dv;
        }
      } else if (key === 'innerHTML') {
        // SAFETY: Only use innerHTML for trusted content like SVG icons.
        // Never pass user-provided strings through this path.
        el.innerHTML = value;
      } else if (key === 'value') {
        el.value = value;
      } else if (typeof value === 'boolean') {
        if (value) {
          el.setAttribute(key, '');
        }
        // false → don't set attribute (absence = not disabled/checked/selected)
      } else {
        el.setAttribute(key, value);
      }
    }
  }

  if (children !== undefined && children !== null) {
    appendChildren(el, children);
  }

  return el;
}

function appendChildren(parent, children) {
  if (Array.isArray(children)) {
    for (const child of children) {
      if (child !== null && child !== undefined && child !== false) {
        appendChildren(parent, child);
      }
    }
  } else if (typeof children === 'string' || typeof children === 'number') {
    parent.appendChild(document.createTextNode(String(children)));
  } else if (children instanceof Node) {
    parent.appendChild(children);
  }
}

/**
 * Clear all children from an element.
 */
export function clear(element) {
  while (element.firstChild) {
    element.removeChild(element.firstChild);
  }
}

/**
 * Replace all children of parent with the given element.
 */
export function render(parent, element) {
  clear(parent);
  if (element) {
    parent.appendChild(element);
  }
}


