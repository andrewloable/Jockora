import { describe, expect, it, beforeEach, afterEach, vi } from 'vitest';
import { TestBed } from '@angular/core/testing';
import { ThemeToggle, applyStoredTheme, readStoredTheme, setTheme, theme } from './theme';

// The theme has to survive three hostile-ish environments: a browser that
// throws on localStorage access (private windows, "block site data"), a stored
// value that is nonsense, and a viewer who has expressed no preference at all.

describe('theme', () => {
  beforeEach(() => {
    localStorage.clear();
    document.documentElement.removeAttribute('data-theme');
    setTheme('system');
  });

  afterEach(() => {
    vi.restoreAllMocks();
    document.documentElement.removeAttribute('data-theme');
  });

  it('follows the system by default, setting no attribute', () => {
    // No data-theme means prefers-color-scheme decides, which is the right
    // default: most people never touch a theme switch.
    setTheme('system');
    expect(theme()).toBe('system');
    expect(document.documentElement.hasAttribute('data-theme')).toBe(false);
  });

  it('puts an explicit choice on the root element', () => {
    setTheme('dark');
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark');
    setTheme('light');
    expect(document.documentElement.getAttribute('data-theme')).toBe('light');
  });

  it('remembers the choice', () => {
    setTheme('dark');
    expect(localStorage.getItem('jockora-theme')).toBe('dark');
  });

  it('applies what was stored, before anything renders', () => {
    localStorage.setItem('jockora-theme', 'dark');
    // applyStoredTheme reads the signal, which was seeded at module load, so
    // set it the way a fresh page would have.
    setTheme('dark');
    applyStoredTheme();
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark');
  });

  it('reads back a stored theme', () => {
    localStorage.setItem('jockora-theme', 'dark');
    expect(readStoredTheme()).toBe('dark');
  });

  it('ignores a stored value that is not a theme', () => {
    // Anything could be in there: a hand-edited value, or a key another app
    // happened to use. An unknown value means no preference, not a crash.
    localStorage.setItem('jockora-theme', 'chartreuse');
    expect(readStoredTheme()).toBe('system');
  });

  it('falls back to the system when storage cannot even be read', () => {
    // Private windows and browsers set to block site data throw on ACCESS, not
    // only on write.
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('SecurityError');
    });
    expect(readStoredTheme()).toBe('system');
  });

  it('still applies the theme when storage refuses to be written', () => {
    // A private window throws on setItem. The choice must still take effect for
    // this session; only the memory of it is lost.
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('QuotaExceededError');
    });
    expect(() => setTheme('dark')).not.toThrow();
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark');
  });
});

describe('ThemeToggle', () => {
  beforeEach(async () => {
    localStorage.clear();
    setTheme('system');
    await TestBed.configureTestingModule({ imports: [ThemeToggle] }).compileComponents();
  });

  afterEach(() => document.documentElement.removeAttribute('data-theme'));

  it('cycles auto, light, dark and back', () => {
    // One button rather than a segmented control: three permanent targets to
    // buy one decision a person makes once is a bad trade.
    const fixture = TestBed.createComponent(ThemeToggle);
    fixture.detectChanges();
    const button = fixture.nativeElement.querySelector('[data-theme-toggle]');

    expect(button.textContent.trim()).toBe('Auto');
    button.click();
    fixture.detectChanges();
    expect(button.textContent.trim()).toBe('Light');
    button.click();
    fixture.detectChanges();
    expect(button.textContent.trim()).toBe('Dark');
    button.click();
    fixture.detectChanges();
    expect(button.textContent.trim()).toBe('Auto');
  });

  it('says what it does, for a screen reader', () => {
    const fixture = TestBed.createComponent(ThemeToggle);
    fixture.detectChanges();
    const button = fixture.nativeElement.querySelector('[data-theme-toggle]');
    expect(button.getAttribute('aria-label')).toContain('Theme: system');
    button.click();
    fixture.detectChanges();
    expect(button.getAttribute('aria-label')).toContain('Theme: light');
  });
});
