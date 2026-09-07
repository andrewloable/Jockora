import { Component, effect, inject, input, signal } from '@angular/core';
import { Api } from '../api/api';

/**
 * The thumbs-down on the break that just aired.
 *
 * ONE BUTTON, and no thumbs-up. A listener volunteers that something was bad;
 * asking them to rate every break turns listening into work, and the DJ's job
 * is to be good by default rather than to be graded.
 */
@Component({
  selector: 'app-feedback',
  standalone: true,
  template: `
    @if (transcript()) {
      <p data-feedback>
        <button type="button" data-thumbsdown [disabled]="sent()" (click)="send()">
          That one was bad
        </button>
        <span data-said>{{ said() }}</span>
      </p>
    }
  `,
})
export class Feedback {
  private readonly api = inject(Api);

  readonly station = input<number | null>(null);
  /** The break being rated. Nothing to rate means nothing to press. */
  readonly transcript = input('');

  readonly said = signal('');
  readonly sent = signal(false);

  constructor() {
    // A NEW BREAK IS A NEW VERDICT. sent() stops a double press on the SAME
    // break; carrying it into the next one let a listener rate exactly one
    // thing per page load, and the panel that reads these would see a single
    // row where there should be a shift's worth.
    effect(() => {
      this.transcript();
      this.sent.set(false);
      this.said.set('');
    });
  }

  send(): void {
    const station = this.station();
    if (station === null) {
      return;
    }
    this.api.feedback(station, 'down').subscribe({
      next: () => {
        this.sent.set(true);
        this.said.set(' Noted.');
      },
      error: () => this.said.set(' Could not record that.'),
    });
  }
}
