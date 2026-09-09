import { Component, ElementRef, effect, inject, input, output, signal } from '@angular/core';

/**
 * The shell every add and edit form in the console opens inside.
 *
 * WHY IT EXISTS. Each form used to be an in-page fieldset below the list it
 * belonged to. Measured on a 390 by 844 phone: tapping Edit on the first
 * station left scrollY at 0 while the form opened at viewport y 868 -- entirely
 * below the fold, on a page 2664px tall, with nothing scrolling to it. Tapping
 * Edit looked like it did nothing at all. On the desktop the add form stayed on
 * screen underneath an open edit, so two name fields faced the operator at
 * once. Jockora-e9a.60.
 *
 * THE PLATFORM DIALOG, not an overlay and not a dependency. showModal gives the
 * top layer, the backdrop, focus containment, inert background and Escape for
 * free -- and every browser this targets has had it for years. A hand-rolled
 * div would have to reimplement all of that and would get the focus trap wrong.
 *
 * ONE SHELL, six screens. The forms themselves are unchanged: their layout was
 * settled in Jockora-e9a.53 and Jockora-e9a.55 and this only moves where they
 * live.
 */
@Component({
  selector: 'app-form-dialog',
  standalone: true,
  template: `
    <dialog
      data-form-dialog
      [attr.aria-label]="title()"
      (cancel)="onCancel($event)"
      (close)="onClose()"
      (click)="onClick($event)"
    >
      <header data-dialog-head>
        <h2 data-dialog-title>{{ title() }}</h2>
        <button type="button" data-dialog-close aria-label="Close" (click)="request()">
          Close
        </button>
      </header>
      <!-- The form itself, unchanged. -->
      <div data-dialog-body (input)="touched.set(true)" (change)="touched.set(true)">
        <ng-content />
      </div>
    </dialog>
  `,
})
export class FormDialog {
  private readonly host = inject(ElementRef<HTMLElement>);

  /** What is being edited, said out loud so a scrolled-away row stops mattering. */
  readonly title = input('');
  /** The parent owns this. The dialog never closes itself behind the caller's back. */
  readonly open = input(false);
  /** Asked to close: by Escape, by the backdrop, or by the Close button. */
  readonly closed = output<void>();

  /**
   * Whether anything in the form has been touched since it opened.
   *
   * MEASURED HERE rather than asked of each screen: one input or change event
   * from anywhere in the content is the whole signal, and six forms each
   * computing their own dirtiness is six chances to get it wrong.
   */
  protected readonly touched = signal(false);

  /** Where focus was before this opened, so it can be given back. */
  private opener: HTMLElement | null = null;

  constructor() {
    // OPEN() IS THE ONLY THING THIS READS, deliberately. That is what makes it
    // safe to call showModal unguarded: the effect cannot re-run while the
    // dialog is already open, and showModal on an open dialog throws. Reading
    // another signal here would reintroduce that, so do not.
    //
    // The dialog element is static in this component's own template, so it
    // exists by the time this component's effects flush.
    effect(() => {
      const el = this.dialog();
      if (this.open()) {
        this.opener = document.activeElement as HTMLElement | null;
        this.touched.set(false);
        el.showModal();
        this.focusFirstField(el);
      } else if (el.open) {
        el.close();
        // BACK WHERE THEY WERE. A keyboard user who closes a dialog and lands
        // at the top of the document has lost their place in the list.
        this.opener?.focus();
        this.opener = null;
      }
    });
  }

  private dialog(): HTMLDialogElement {
    return (this.host.nativeElement as HTMLElement).querySelector('dialog') as HTMLDialogElement;
  }

  /**
   * Focus the first field, not the Close button.
   *
   * showModal focuses the first focusable descendant, which is the Close button
   * in the header -- so a keyboard user would land on the way out rather than
   * on the way in.
   */
  private focusFirstField(el: HTMLDialogElement): void {
    const first = el.querySelector<HTMLElement>(
      '[data-dialog-body] input:not([type=hidden]), [data-dialog-body] select, ' +
        '[data-dialog-body] textarea',
    );
    first?.focus();
  }

  /** Escape. The browser fires cancel first, which is where a refusal belongs. */
  onCancel(event: Event): void {
    event.preventDefault();
    this.request();
  }

  /**
   * The element closed. If the caller still says open, put it back.
   *
   * MEASURED, not assumed: preventDefault on cancel is supposed to stop Escape
   * closing a dialog, and in Chrome 151 it does not -- the discard prompt ran,
   * the operator answered no, and the dialog closed anyway, leaving the element
   * shut while the component still believed it was open. Rather than trusting
   * one browser's reading of cancel, this resyncs from whatever actually
   * happened, which also covers any other native close path.
   */
  onClose(): void {
    if (!this.open()) {
      return;
    }
    const el = this.dialog();
    el.showModal();
    this.focusFirstField(el);
  }

  /**
   * A click on the dialog ELEMENT is the backdrop; one that lands on the form
   * inside it is not. The body wrapper is what makes the two distinguishable.
   */
  onClick(event: MouseEvent): void {
    if (event.target === this.dialog()) {
      this.request();
    }
  }

  /** Ask to close, and let an edited form refuse. */
  request(): void {
    if (this.touched() && !confirm('Discard your changes to this form?')) {
      return;
    }
    this.closed.emit();
  }
}
