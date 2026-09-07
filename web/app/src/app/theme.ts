import { Component, signal } from '@angular/core';

/** What the viewer has asked for. "system" means: follow the OS. */
export type Theme = 'system' | 'light' | 'dark';

const KEY = 'jockora-theme';
const ORDER: Theme[] = ['system', 'light', 'dark'];

/**
 * The chosen theme, shared by both halves of the app.
 *
 * A MODULE-LEVEL SIGNAL rather than an injectable service: it has no
 * dependencies, the listener and the console both need the same one, and a
 * service would mean providing it in two injectors or reasoning about which
 * root owns it.
 */
export const theme = signal<Theme>(readStoredTheme());

/**
 * What was stored, if it is a theme. Exported so both of its failure paths can
 * be tested: this runs once at module load, and a branch that only executes
 * before the first test starts is a branch no test can reach.
 */
export function readStoredTheme(): Theme {
  try {
    const saved = localStorage.getItem(KEY);
    return ORDER.includes(saved as Theme) ? (saved as Theme) : 'system';
  } catch {
    // Private windows and browsers set to block site data throw on ACCESS, not
    // just on write. Falling back to the system preference is the right answer
    // and costs the viewer nothing.
    return 'system';
  }
}

/**
 * setTheme records the choice and puts it on <html>, where the stylesheet reads
 * it as data-theme. "system" removes the attribute so prefers-color-scheme
 * takes over again.
 */
export function setTheme(next: Theme): void {
  theme.set(next);
  const root = document.documentElement;
  if (next === 'system') {
    root.removeAttribute('data-theme');
  } else {
    root.setAttribute('data-theme', next);
  }
  try {
    localStorage.setItem(KEY, next);
  } catch {
    // Not being able to REMEMBER the choice must not stop it applying now.
  }
}

/** Apply whatever was stored, at startup. */
export function applyStoredTheme(): void {
  setTheme(theme());
}

/**
 * A single control cycling system -> light -> dark.
 *
 * ONE BUTTON, NOT THREE. The three states are worth having -- "system" is the
 * right default and neither of the others can express it -- but a segmented
 * control for something a person sets once and forgets is three permanent
 * targets to buy one rare decision.
 */
@Component({
  selector: 'app-theme-toggle',
  standalone: true,
  template: `
    <button
      type="button"
      data-theme-toggle
      [attr.aria-label]="'Theme: ' + theme() + '. Change.'"
      (click)="cycle()"
    >
      {{ label() }}
    </button>
  `,
})
export class ThemeToggle {
  readonly theme = theme;

  label(): string {
    return { system: 'Auto', light: 'Light', dark: 'Dark' }[this.theme()];
  }

  cycle(): void {
    setTheme(ORDER[(ORDER.indexOf(this.theme()) + 1) % ORDER.length]);
  }
}
