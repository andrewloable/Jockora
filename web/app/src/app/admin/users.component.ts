import { Component, inject, signal } from '@angular/core';
import { AdminApi, User, serverSaid } from '../api/api';
import { TableView } from './table-view';
import { FormDialog } from './form-dialog';

/**
 * The accounts. Admin-created, always: there is no self-registration route
 * anywhere in Jockora, and this console is the only way an account exists.
 */
@Component({
  selector: 'app-users',
  standalone: true,
  imports: [FormDialog],
  template: `
    <h2>People</h2>
    <!-- SEARCH AND SORT over the whole list, which is all in the browser.
         Jockora-9g7. -->
    <label data-table-search>
      Search
      <input
        data-search
        type="search"
        placeholder="name or role"
        [value]="view.query()"
        (input)="view.query.set($any($event.target).value)"
      />
    </label>
    <table data-users>
      <thead>
        <tr>
          <th [attr.aria-sort]="view.ariaSort('name')">
            <button type="button" data-sort="name" (click)="view.toggle('name')">
              Name <span data-sort-marker>{{ view.marker('name') }}</span>
            </button>
          </th>
          <th [attr.aria-sort]="view.ariaSort('role')">
            <button type="button" data-sort="role" (click)="view.toggle('role')">
              Role <span data-sort-marker>{{ view.marker('role') }}</span>
            </button>
          </th>
          <th [attr.aria-sort]="view.ariaSort('status')">
            <button type="button" data-sort="status" (click)="view.toggle('status')">
              Status <span data-sort-marker>{{ view.marker('status') }}</span>
            </button>
          </th>
          <!-- NOT SORTABLE: a column of buttons has no order. -->
          <th>Actions</th>
        </tr>
      </thead>
      <tbody>
        @for (user of view.shown(); track user.id) {
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

    <button type="button" data-add-open (click)="adding.set(true)">Create an account</button>
    <app-form-dialog [title]="'New account'" [open]="adding()" (closed)="closeAdd()">
      @if (adding()) {
        <fieldset data-new-account>
          <legend>New account</legend>
          <label>
            Name
            <input data-name [value]="name()" (input)="name.set(value($event))" />
          </label>
          <label>
            Password
            <input
              data-password
              type="password"
              [value]="password()"
              (input)="password.set(value($event))"
            />
          </label>
          <!-- THE PRIVILEGE DECISION ON THIS FORM, and it was an unlabelled combo
           box with no placeholder to fall back on either -- so a screen reader
           announced the choice between listener and admin as nothing at all. -->
          <label>
            Role
            <select data-role [value]="role()" (change)="role.set(value($event))">
              <option value="listener">listener</option>
              <option value="admin">admin</option>
            </select>
          </label>
          <div data-form-actions>
            <button type="button" data-add (click)="add()">Create</button>
          </div>
        </fieldset>
      }
    </app-form-dialog>

    <app-form-dialog
      [title]="resetting() ? 'New password for ' + resetting()!.name : ''"
      [open]="resetting() !== null"
      (closed)="cancelReset()"
    >
      @if (resetting(); as user) {
        <fieldset data-reset-form>
          <legend>New password for {{ user.name }}</legend>
          <label>
            New password
            <input
              data-new-password
              type="password"
              [value]="newPassword()"
              (input)="newPassword.set(value($event))"
            />
          </label>
          <div data-form-actions>
            <button type="button" data-save-password (click)="savePassword(user)">Set</button>
            <button type="button" data-cancel-reset (click)="cancelReset()">Cancel</button>
          </div>
        </fieldset>
      }
    </app-form-dialog>

    <p data-said>{{ said() }}</p>
  `,
})
export class Users {
  private readonly api = inject(AdminApi);

  readonly users = signal<User[]>([]);

  /** The search box and the sortable headers. Jockora-9g7. */
  readonly view = new TableView<User>(this.users, [
    { key: 'name', value: (u) => u.name },
    { key: 'role', value: (u) => u.role },
    { key: 'status', value: (u) => (u.disabled ? 'disabled' : 'active') },
  ]);
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
  /**
   * The NEW ACCOUNT form's password, and only that form's.
   *
   * ONE SIGNAL PER FORM. Both fieldsets used to share this one, and the create
   * fieldset is always rendered -- so typing a new password for an existing
   * user filled the create form with it, in a type=password box rendering dots
   * in a form nobody was looking at. Cancel did not clear it, so the next
   * account created got that password and the operator handed over a different
   * one. Jockora-e9a.62.
   *
   * Clearing on cancel would have patched the exit and left the two fields
   * mirroring each other while both were open, which is its own confusion.
   */
  readonly password = signal('');
  /** Whether the New account dialog is open. */
  readonly adding = signal(false);
  /** The RESET form's new password. Separate on purpose; see above. */
  readonly newPassword = signal('');
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
      error: (e: unknown) => this.said.set(serverSaid(e, 'Could not save that account.')),
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
          this.closeAdd();
          this.load();
        },
        error: (e: unknown) => this.said.set(serverSaid(e, 'Could not create that account.')),
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
    // NEVER BOTH AT ONCE -- and closed the same way Escape closes it, so a
    // password half-typed into the create form does not linger behind a dialog
    // the operator has moved away from.
    this.closeAdd();
    this.newPassword.set('');
    this.said.set('');
    this.resetting.set(user);
  }

  /**
   * Close the New account dialog, taking whatever was half-typed with it.
   *
   * The password especially: it is a secret meant for one account, and a form
   * that keeps it is how the next account gets created with it.
   */
  closeAdd(): void {
    this.adding.set(false);
    this.name.set('');
    this.password.set('');
    // BACK TO THE SAFE ONE. Role is the privilege decision on this form, and
    // carrying the last account's choice into the next means an operator who
    // just made an admin makes another one by not touching a box. Listener is
    // what a new account should be unless somebody says otherwise.
    this.role.set('listener');
  }

  /** Close the reset form, taking its password with it. */
  cancelReset(): void {
    this.newPassword.set('');
    this.resetting.set(null);
  }

  savePassword(user: User): void {
    this.api.resetPassword(user.id, this.newPassword()).subscribe({
      next: () => {
        this.resetting.set(null);
        this.newPassword.set('');
        this.said.set(`Password changed. ${user.name} stays signed in until they sign out.`);
      },
      // KEPT ON A FAILURE, deliberately. The reset form stays open, so the
      // password is still in its own field where the operator can correct it --
      // and the usual failure is the server saying it is too short, which is
      // the worst moment to make somebody retype a long one. It cannot reach
      // the create form any more; that is what the separate signal is for.
      error: (e: unknown) => this.said.set(serverSaid(e, 'Could not change that password.')),
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
      error: (e: unknown) => this.said.set(serverSaid(e, 'Could not delete that account.')),
    });
  }
}
