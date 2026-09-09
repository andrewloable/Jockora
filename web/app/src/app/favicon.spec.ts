import { describe, expect, it, afterEach } from 'vitest';
import { ADMIN_ICON, LISTENER_ICON, setFavicon } from './favicon';

// Jockora-9xk. An operator runs the console and the station side by side, and
// two tabs carrying the same icon means clicking the wrong one.

describe('setFavicon', () => {
  afterEach(() => document.querySelectorAll('link[rel="icon"]').forEach((l) => l.remove()));

  function link(): HTMLLinkElement {
    const el = document.createElement('link');
    el.rel = 'icon';
    el.href = 'icon.svg';
    document.head.appendChild(el);
    return el;
  }

  it('points the tab at the mark it is given', () => {
    const el = link();
    setFavicon(ADMIN_ICON);
    expect(el.getAttribute('href')).toBe(ADMIN_ICON);
    setFavicon(LISTENER_ICON);
    expect(el.getAttribute('href')).toBe(LISTENER_ICON);
  });

  it('the two marks are different files, or the tabs are indistinguishable', () => {
    // The whole point. A constant that drifted to the same value would leave
    // this feature looking wired and doing nothing.
    expect(ADMIN_ICON).not.toBe(LISTENER_ICON);
  });

  it('leaves the tab alone rather than crashing when there is no link', () => {
    // index.html always ships one, so this is not reachable in the app -- but a
    // component test that renders no document head is, and a route change is
    // the wrong moment to throw.
    expect(() => setFavicon(ADMIN_ICON)).not.toThrow();
  });
});
