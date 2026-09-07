import { Component, inject, signal } from '@angular/core';
import { AdminApi, User } from '../api/api';

/**
 * The accounts. Admin-created, always: there is no self-registration route
 * anywhere in Jockora, and this console is the only way an account exists.
 */
@Component({
  selector: 'app-users',
  standalone: true,
  template: `
    <h2>People</h2>
    <table data-users>
      <thead>
        <tr>
          <th>Name</th>
          <th>Role</th>
          <th>Status</th>
          <th>Actions</th>
        </tr>
      </thead>
      <tbody>
      @for (user of users(); track user.id) {
        <tr [attr.data-row]="user.id">
          <td>{{ user.name }}</td>
          <td>{{ user.role }}</td>
          <td>{{ user.disabled ? 'disabled' : 'active' }}</td>
          <td>
            <button type="button" data-toggle (click)="toggle(user)">
              {{ user.disabled ? 'Enable' : 'Disable' }}
            </button>
            <button type="button" data-reset (click)="reset(user)">Reset password</button>
            <button type="button" data-remove (click)="remove(user)">Delete</button>
          </td>
        </tr>
      }
      </tbody>
    </table>

    <fieldset>
      <legend>New account</legend>
      <input data-name placeholder="name" [value]="name()" (input)="name.set(value($event))" />
      <input
        data-password
        type="password"
        placeholder="password"
        [value]="password()"
        (input)="password.set(value($event))"
      />
      <select data-role [value]="role()" (change)="role.set(value($event))">
        <option value="listener">listener</option>
        <option value="admin">admin</option>
      </select>
      <button type="button" data-add (click)="add()">Create</button>
    </fieldset>

    @if (resetting(); as user) {
      <fieldset data-reset-form>
        <legend>New password for {{ user.name }}</legend>
        <input
          data-new-password
          type="password"
          [value]="password()"
          (input)="password.set(value($event))"
        />
        <button type="button" data-save-password (click)="savePassword(user)">Set</button>
        <button type="button" data-cancel-reset (click)="resetting.set(null)">Cancel</button>
      </fieldset>
    }

    <p data-said>{{ said() }}</p>
  `,
})
export class Users {
  private readonly api = inject(AdminApi);

  readonly users = signal<User[]>([]);
  readonly name = signal('');
  readonly password = signal('');
  readonly role = signal('listener');
  readonly resetting = signal<User | null>(null);
  readonly said = signal('');

  constructor() {
    this.load();
  }

  value(event: Event): string {
    return (event.target as HTMLInputElement).value;
  }

  load(): void {
    this.api.users().subscribe({
      next: (u) => this.users.set(u),
      error: () => this.said.set('Could not read the accounts.'),
    });
  }

  add(): void {
    this.said.set('');
    this.api.addUser({ name: this.name(), password: this.password(), role: this.role() }).subscribe({
      next: () => {
        // The password is never shown again and cannot be read back, so the
        // operator has to hand it over now.
        this.said.set(`Created ${this.name()}. Give them that password; it is not stored anywhere readable.`);
        this.name.set('');
        this.password.set('');
        this.load();
      },
      error: (e: { error?: { error?: string } }) =>
        this.said.set(String(e.error?.error ?? 'Could not create that account.')),
    });
  }

  toggle(user: User): void {
    this.said.set('');
    this.api.setUserEnabled(user.id, user.disabled).subscribe({
      next: () => this.load(),
      error: () => this.said.set('Could not change that account.'),
    });
  }

  reset(user: User): void {
    this.password.set('');
    this.said.set('');
    this.resetting.set(user);
  }

  savePassword(user: User): void {
    this.api.resetPassword(user.id, this.password()).subscribe({
      next: () => {
        this.resetting.set(null);
        this.password.set('');
        this.said.set(`Password changed. ${user.name} stays signed in until they sign out.`);
      },
      error: (e: { error?: { error?: string } }) =>
        this.said.set(String(e.error?.error ?? 'Could not change that password.')),
    });
  }

  remove(user: User): void {
    this.said.set('');
    this.api.removeUser(user.id).subscribe({
      next: () => this.load(),
      // 409 is the server refusing to delete the last admin. Shown as it
      // came: a console that swallows it looks broken.
      error: (e: { error?: { error?: string } }) =>
        this.said.set(String(e.error?.error ?? 'Could not delete that account.')),
    });
  }
}
