import { describe, expect, it } from 'vitest';
import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';

/** Every .ts under src/app, spec files excluded. */
function shipped(dir: string): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) {
      out.push(...shipped(path));
    } else if (entry.name.endsWith('.ts') && !entry.name.endsWith('.spec.ts')) {
      out.push(path);
    }
  }
  return out;
}

// A <select> whose OPTIONS ARE RENDERED BY @for cannot carry [value].
//
// The value binding is applied before the options exist, so the browser falls
// back to the first one -- and it never re-runs, because the bound value never
// changed. The station's jock simply vanished from the picker on every reload
// while the server had it all along, and the same shape was sitting in four
// other places waiting to do the same thing.
//
// [selected] on each option is evaluated as the options render, so it does not
// care what order anything arrives in.
describe('selects with async options', () => {
  const files = shipped(join(import.meta.dirname, 'src', 'app'));

  it('finds the templates to check', () => {
    expect(files.length).toBeGreaterThan(10);
    expect(files.some((f) => f.endsWith('stations.component.ts'))).toBe(true);
  });

  it('never binds [value] on a select whose options come from @for', () => {
    const guilty: string[] = [];

    for (const file of files) {
      const text = readFileSync(file, 'utf8');
      // Each <select ...> and everything up to its closing tag.
      for (const block of text.match(/<select[\s\S]*?<\/select>/g) ?? []) {
        if (block.includes('@for') && /\[value\]=/.test(block.split('>')[0])) {
          guilty.push(`${file.slice(file.indexOf(join('src', 'app')))}: ${block.slice(0, 60)}`);
        }
      }
    }

    expect(guilty).toEqual([]);
  });
});
