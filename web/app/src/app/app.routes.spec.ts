import { describe, expect, it } from 'vitest';
import { routes } from './app.routes';

describe('routes', () => {
  it('loads the listener eagerly', () => {
    // Almost everyone loads this one, and a dial that arrives after a second
    // chunk download is a second of blank screen on a phone.
    const listener = routes.find((r) => r.path === '');
    expect(listener).toBeDefined();
    expect(listener?.component).toBeDefined();
    expect(listener?.loadChildren).toBeUndefined();
  });

  it('loads the console lazily', () => {
    // Most people never open it, and its forms, tables and vocabulary lists are
    // weight a listener should not pay for.
    const admin = routes.find((r) => r.path === 'admin');
    expect(admin).toBeDefined();
    expect(admin?.loadChildren).toBeTypeOf('function');
    expect(admin?.component).toBeUndefined();
  });

  it('loads the console\'s own routes', async () => {
    // The lazy loader is CALLED, not just checked for: a loadChildren that
    // points at a module which no longer exports adminRoutes type-checks and
    // fails at the moment somebody opens the console.
    const admin = routes.find((r) => r.path === 'admin');
    const loaded = await (admin!.loadChildren as () => Promise<unknown>)();
    expect(Array.isArray(loaded)).toBe(true);
  });

  it('has a login page of its own', () => {
    // Accounts are admin-created and a listener must sign in, so the form is a
    // route rather than a modal the router cannot send anybody to.
    const login = routes.find((r) => r.path === 'login');
    expect(login?.component).toBeDefined();
  });

  it('sends an unknown path to the dial', () => {
    // A stale bookmark should land somebody on the dial, not on an error.
    const rest = routes.find((r) => r.path === '**');
    expect(rest?.redirectTo).toBe('');
  });
});
