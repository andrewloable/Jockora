import { describe, expect, it } from 'vitest';
import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';

/** Every .ts under src/app, spec files included. */
function sources(dir: string): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) {
      out.push(...sources(path));
    } else if (entry.name.endsWith('.ts')) {
      out.push(path);
    }
  }
  return out;
}

describe('accounts live in one place', () => {
  // Accounts are ADMIN-CREATED. There is no self-registration route anywhere in
  // Jockora, and the way that stays true is that the account endpoints have
  // exactly one caller: if a second component ever learns to POST /admin/users,
  // the rule has quietly become a convention, and conventions rot.
  //
  // This reads the tree off disk rather than trusting imports, so a call built
  // out of a template string or reached through a re-export is still caught.
  // import.meta.dirname is the workspace root under the test builder, not the
  // directory this file sits in. The first test below fails loudly if that ever
  // stops being true, rather than letting a mis-anchored sweep pass on nothing.
  const files = sources(join(import.meta.dirname, 'src', 'app'));

  // Specs are excluded from the searches below: they NAME the strings they are
  // guarding against, this file included, so a sweep that read them would only
  // ever find itself.
  const shipped = files.filter((f) => !f.endsWith('.spec.ts'));

  it('finds a tree to read', () => {
    // A recursion bug that returned nothing would make every assertion below
    // pass while proving nothing at all.
    expect(files.length).toBeGreaterThan(20);
    expect(shipped.length).toBeGreaterThan(10);
    expect(shipped.some((f) => f.endsWith('users.component.ts'))).toBe(true);
    expect(shipped.some((f) => f.endsWith(join('listener', 'dial.component.ts')))).toBe(true);
  });

  it('mentions /admin/users only in the api service', () => {
    const guilty = shipped
      .filter((f) => readFileSync(f, 'utf8').includes('/admin/users'))
      .map((f) => f.slice(f.indexOf(join('src', 'app'))));

    expect(guilty).toEqual([join('src', 'app', 'api', 'api.ts')]);
  });

  it('calls the account methods only from the users component', () => {
    // The URL guard above only proves the components go through the service.
    // THIS is the rule that matters: one caller, so a second component cannot
    // grow an account form without this failing.
    const callers = new Map<string, string[]>();
    for (const file of shipped) {
      const text = readFileSync(file, 'utf8');
      for (const method of ['addUser', 'removeUser', 'setUserEnabled', 'resetPassword']) {
        if (text.includes(`${method}(`)) {
          callers.set(method, [...(callers.get(method) ?? []), file]);
        }
      }
    }

    // Every method is found somewhere, or a rename would empty this silently.
    expect([...callers.keys()].sort()).toEqual([
      'addUser',
      'removeUser',
      'resetPassword',
      'setUserEnabled',
    ]);
    for (const [method, files] of callers) {
      const outside = files.filter(
        (f) => !f.endsWith('users.component.ts') && !f.endsWith(join('api', 'api.ts')),
      );
      expect(outside, method).toEqual([]);
    }
  });

  it('has no route or component that signs anybody up', () => {
    // The listener half must not carry one either: a register form that 404s is
    // still a promise the product does not keep.
    for (const file of shipped) {
      const text = readFileSync(file, 'utf8').toLowerCase();
      expect(text, file).not.toContain('/register');
      expect(text, file).not.toContain('/signup');
      expect(text, file).not.toContain('sign up');
    }
  });
});
