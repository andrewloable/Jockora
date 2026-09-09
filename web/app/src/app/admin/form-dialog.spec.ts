import { describe, expect, it, beforeEach, afterEach, vi } from 'vitest';
import { Component, signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { FormDialog } from './form-dialog';

// Jockora-e9a.60. Every add and edit form was an in-page fieldset below the
// list it belonged to. Measured on a 390 by 844 phone: tapping Edit on the
// first station left scrollY at 0 while the form opened at viewport y 868 --
// entirely below the fold, on a page 2664px tall, with nothing scrolling to it.
// Tapping Edit looked like it did nothing at all.

@Component({
  standalone: true,
  imports: [FormDialog],
  template: `
    <button type="button" data-opener (click)="open.set(true)">Edit</button>
    <app-form-dialog [title]="title()" [open]="open()" (closed)="open.set(false)">
      <label>Name <input data-first /></label>
      <label>Genre <input data-second /></label>
      <label>
        Mood
        <select data-picker><option value="calm">calm</option><option value="raw">raw</option></select>
      </label>
    </app-form-dialog>
  `,
})
class Host {
  readonly open = signal(false);
  readonly title = signal('Edit NIGHT ROCK');
}

describe('FormDialog', () => {
  beforeEach(async () => {
    vi.stubGlobal('confirm', () => true);
    await TestBed.configureTestingModule({ imports: [Host] }).compileComponents();
  });
  afterEach(() => vi.unstubAllGlobals());

  function mounted() {
    const fixture = TestBed.createComponent(Host);
    fixture.detectChanges();
    return fixture;
  }

  const dialog = (f: { nativeElement: HTMLElement }) =>
    f.nativeElement.querySelector('dialog') as HTMLDialogElement;

  it('edit dialog opens on the screen rather than below the fold', () => {
    const fixture = mounted();
    expect(dialog(fixture).open).toBe(false);

    fixture.nativeElement.querySelector('[data-opener]').click();
    fixture.detectChanges();

    // THE TOP LAYER, which is what a fieldset three screenfuls down could not
    // give: showModal puts it above the page rather than in it.
    expect(dialog(fixture).open).toBe(true);
    expect(fixture.componentInstance.open()).toBe(true);
  });

  it('edit dialog names what is being edited', () => {
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-opener]').click();
    fixture.detectChanges();
    // The row scrolling out of view stops mattering once the dialog says which
    // one it is.
    expect(fixture.nativeElement.querySelector('[data-dialog-title]').textContent).toContain(
      'NIGHT ROCK',
    );
    expect(dialog(fixture).getAttribute('aria-label')).toContain('NIGHT ROCK');
  });

  it('edit dialog puts focus in the first field and gives it back on close', () => {
    const fixture = mounted();
    const opener = fixture.nativeElement.querySelector('[data-opener]') as HTMLButtonElement;
    document.body.appendChild(fixture.nativeElement);
    opener.focus();
    opener.click();
    fixture.detectChanges();

    expect(document.activeElement).toBe(fixture.nativeElement.querySelector('[data-first]'));

    fixture.nativeElement.querySelector('[data-dialog-close]').click();
    fixture.detectChanges();
    // BACK WHERE THEY WERE. A keyboard user who closes a dialog and lands at
    // the top of the document has lost their place in the list.
    expect(document.activeElement).toBe(opener);
  });

  it('edit dialog closes on Escape', () => {
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-opener]').click();
    fixture.detectChanges();

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    fixture.detectChanges();
    expect(fixture.componentInstance.open()).toBe(false);
  });

  it('edit dialog closes on the backdrop', () => {
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-opener]').click();
    fixture.detectChanges();

    // A click that lands on the dialog ELEMENT is the backdrop; one that lands
    // on a field inside it is not.
    dialog(fixture).click();
    fixture.detectChanges();
    expect(fixture.componentInstance.open()).toBe(false);
  });

  it('edit dialog keeps a field a click on the form from closing it', () => {
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-opener]').click();
    fixture.detectChanges();

    (fixture.nativeElement.querySelector('[data-first]') as HTMLElement).click();
    fixture.detectChanges();
    expect(fixture.componentInstance.open()).toBe(true);
  });

  it('edit dialog asks before discarding an edited form', () => {
    const asked: string[] = [];
    vi.stubGlobal('confirm', (q: string) => {
      asked.push(q);
      return false;
    });
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-opener]').click();
    fixture.detectChanges();

    const field = fixture.nativeElement.querySelector('[data-first]') as HTMLInputElement;
    field.value = 'changed';
    field.dispatchEvent(new Event('input', { bubbles: true }));
    fixture.detectChanges();

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    fixture.detectChanges();

    expect(asked).toHaveLength(1);
    // REFUSED means it stays open with the typing still in it.
    expect(fixture.componentInstance.open()).toBe(true);
    expect(dialog(fixture).open).toBe(true);
  });

  it('edit dialog does not ask when nothing was touched', () => {
    let asked = 0;
    vi.stubGlobal('confirm', () => {
      asked++;
      return true;
    });
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-opener]').click();
    fixture.detectChanges();
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    fixture.detectChanges();

    // Asking to discard a form nobody typed in trains people to click through
    // the question.
    expect(asked).toBe(0);
    expect(fixture.componentInstance.open()).toBe(false);
  });

  it('edit dialog forgets the previous edit when it opens again', () => {
    let asked = 0;
    vi.stubGlobal('confirm', () => {
      asked++;
      return true;
    });
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-opener]').click();
    fixture.detectChanges();
    const field = fixture.nativeElement.querySelector('[data-first]') as HTMLInputElement;
    field.dispatchEvent(new Event('input', { bubbles: true }));
    fixture.detectChanges();
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    fixture.detectChanges();
    expect(asked).toBe(1);

    // A SECOND OPEN STARTS CLEAN, or every dialog after the first one asks.
    fixture.nativeElement.querySelector('[data-opener]').click();
    fixture.detectChanges();
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    fixture.detectChanges();
    expect(asked).toBe(1);
  });

  it('edit dialog counts a picked option as an edit, not only typing', () => {
    // A form whose only change was a SELECT would otherwise be discarded
    // without a question -- and genre and mood are pickers on half these forms.
    let asked = 0;
    vi.stubGlobal('confirm', () => {
      asked++;
      return true;
    });
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-opener]').click();
    fixture.detectChanges();

    const picker = fixture.nativeElement.querySelector('[data-picker]') as HTMLSelectElement;
    picker.value = 'raw';
    picker.dispatchEvent(new Event('change', { bubbles: true }));
    fixture.detectChanges();

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    fixture.detectChanges();
    expect(asked).toBe(1);
  });

  it('edit dialog stays put when something else about it changes', () => {
    // The title is a signal on several of these screens, so the effect re-runs
    // while the dialog is open. Calling showModal again would throw, and
    // re-focusing would drag the caret out of whatever field they were in.
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-opener]').click();
    fixture.detectChanges();
    const second = fixture.nativeElement.querySelector('[data-second]') as HTMLInputElement;
    document.body.appendChild(fixture.nativeElement);
    second.focus();

    fixture.componentInstance.title.set('Edit AMBIENT');
    fixture.detectChanges();

    expect(dialog(fixture).open).toBe(true);
    expect(document.activeElement).toBe(second);
    expect(fixture.nativeElement.querySelector('[data-dialog-title]').textContent).toContain(
      'AMBIENT',
    );
  });


  it('edit dialog puts itself back when the browser closes it anyway', () => {
    // MEASURED IN CHROME 151: preventDefault on cancel is supposed to stop
    // Escape closing a dialog and does not. The discard prompt ran, the
    // operator answered no, and the dialog closed regardless -- leaving the
    // element shut while the component still believed it was open. So the
    // component resyncs from what actually happened.
    vi.stubGlobal('confirm', () => false);
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-opener]').click();
    fixture.detectChanges();
    const field = fixture.nativeElement.querySelector('[data-first]') as HTMLInputElement;
    field.dispatchEvent(new Event('input', { bubbles: true }));
    fixture.detectChanges();

    // The browser closing it behind the component's back.
    dialog(fixture).close();
    fixture.detectChanges();

    expect(fixture.componentInstance.open()).toBe(true);
    expect(dialog(fixture).open).toBe(true);
  });
});
