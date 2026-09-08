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
            @if (editing() === user.id) {
              <td>
                <input data-edit-name [value]="editName()" (input)="editName.set(value($event))" />
              </td>
              <td>
                <select data-edit-role (change)="editRole.set(value($event))">
                  <option value="listener" [selected]="editRole() === 'listener'">listener</option>
                  <option value="admin" [selected]="editRole() === 'admin'">admin</option>
                </select>
              </td>
            } @else {
              <td>{{ user.name }}</td>
              <td>{{ user.role }}</td>
            }
            <td>{{ user.disabled ? 'disabled' : 'active' }}</td>
            <td>
              @if (editing() === user.id) {
                <button type="button" data-save (click)="save(user)">Save</button>
                <button type="button" data-cancel (click)="cancel()">Cancel</button>
              } @else {
                <button type="button" data-edit (click)="startEdit(user)">Edit</button>
                <button type="button" data-toggle (click)="toggle(user)">
                  {{ user.disabled ? 'Enable' : 'Disable' }}
                </button>
                <button type="button" data-reset (click)="reset(user)">Reset password</button>
                <button type="button" data-remove data-danger (click)="remove(user)">Delete</button>
              }
            </td>
          </tr>
        } @empty {
          <!-- LOADING AND EMPTY ARE NOT THE SAME THING and both drew a table
               with no rows, so a slow first paint told a new operator their
               library was empty. Jockora-e9a.50. -->
          <tr>
            <td colspan="5">
              @if (!loaded()) {
                <span data-loading>Loading…</span>
              } @else if (!failed()) {
                <span data-empty
                  ><strong>Only your own account.</strong> Add an account below — there is no
                  self-registration, so listeners cannot make their own.</span
                >
              }
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
  /**
   * True once the first answer has arrived, success OR failure.
   *
   * Without it a table with no rows means two opposite things -- the request is
   * still in flight, or there is genuinely nothing -- and both drew the same
   * empty table. A new operator was told their library was empty and given
   * nothing to do about it. Jockora-e9a.50.
   */
  readonly loaded = signal(false);
  /**
   * True when the last read FAILED, as opposed to returning nothing.
   *
   * Three states, not two: a table with no rows can be in flight, genuinely
   * empty, or the wreckage of a request that did not come back. Without this
   * the third one wore the second one's words and told an operator whose
   * server was down that they had never scanned anything.
   */
  readonly failed = signal(false);
  readonly name = signal('');
  readonly password = signal('');
  readonly role = signal('listener');

  // The row being edited, by id, so opening a second closes the first.
  readonly editing = signal<number | null>(null);
  readonly editName = signal('');
  readonly editRole = signal('');
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
      next: (u) => {
        this.users.set(u);
        this.loaded.set(true);
        this.failed.set(false);
      },
      error: () => {
        this.loaded.set(true);
        this.failed.set(true);
        this.said.set('Could not read the accounts.');
      },
    });
  }

  startEdit(u: User): void {
    this.said.set('');
    this.editing.set(u.id);
    this.editName.set(u.name);
    this.editRole.set(u.role);
  }

  cancel(): void {
    this.editing.set(null);
  }

  /**
   * Save one account's name and role.
   *
   * A REFUSAL LEAVES THE ROW OPEN, and the server's own words are repeated:
   * the two that matter are a name already taken and the refusal to let an
   * operator take admin off their own account, and "could not save" would
   * hide both.
   */
  save(u: User): void {
    this.said.set('');
    this.api.updateUser(u.id, { name: this.editName(), role: this.editRole() }).subscribe({
      next: () => {
        this.said.set(`Saved ${this.editName()}.`);
        this.editing.set(null);
        this.load();
      },
      error: (e: { error?: string }) =>
        this.said.set(String(e.error ?? 'Could not save that account.')),
    });
  }

  add(): void {
    this.said.set('');
    this.api
      .addUser({ name: this.name(), password: this.password(), role: this.role() })
      .subscribe({
        next: () => {
          // The password is never shown again and cannot be read back, so the
          // operator has to hand it over now.
          this.said.set(
            `Created ${this.name()}. Give them that password; it is not stored anywhere readable.`,
          );
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
    // ASKED. There is no self-service way back into an account: the server
    // refuses to remove the last admin, but every other deletion is final.
    if (!confirm(`Delete the account ${user.name}? They will not be able to sign in.`)) {
      return;
    }
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
