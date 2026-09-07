import { Component, inject, signal } from '@angular/core';
import { Router } from '@angular/router';
import { Api } from '../api/api';

/**
 * The way in. Accounts are admin-created, so there is no sign-up link and no
 * password reset: both would be doors the design does not have.
 *
 * Signals and plain input events rather than ngModel. This app is zoneless, and
 * ngModel's updates land a microtask later -- which makes the form's own tests
 * depend on scheduling rather than on the form.
 */
@Component({
  selector: 'app-login',
  standalone: true,
  template: `
    <form (submit)="submit($event)">
      <h1>Jockora</h1>
      <label>
        Name
        <input
          data-name
          autocomplete="username"
          [value]="name()"
          (input)="name.set($any($event.target).value)"
        />
      </label>
      <label>
        Password
        <input
          data-password
          type="password"
          autocomplete="current-password"
          [value]="password()"
          (input)="password.set($any($event.target).value)"
        />
      </label>
      <button type="submit" [disabled]="busy()">Sign in</button>
      @if (error()) {
        <p role="alert" data-error>{{ error() }}</p>
      }
    </form>
  `,
})
export class Login {
  private readonly api = inject(Api);
  private readonly router = inject(Router);

  readonly name = signal('');
  readonly password = signal('');
  readonly busy = signal(false);
  readonly error = signal('');

  submit(event?: Event): void {
    event?.preventDefault();
    this.busy.set(true);
    this.error.set('');
    this.api.login(this.name(), this.password()).subscribe({
      next: () => {
        this.busy.set(false);
        void this.router.navigate(['']);
      },
      error: () => {
        // The FORM STAYS. Clearing it on a mistyped password means typing the
        // name again, and the server cannot tell us which one was wrong -- by
        // design, so the form cannot be used to find out which accounts exist.
        this.busy.set(false);
        this.error.set('Wrong name or password.');
      },
    });
  }
}
