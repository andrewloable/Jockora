import { Component, inject, signal } from '@angular/core';
import { AdminApi, Jock } from '../api/api';

const blank = (): Jock => ({
  id: '',
  name: '',
  voice_id: '',
  good_for_genres: [],
  good_for_moods: [],
  speech_style: '',
  personality: '',
  forbidden: [],
});

/**
 * The jocks. A persona card is written WHOLE: the form is the card, and
 * submitting it replaces the card.
 */
@Component({
  selector: 'app-jocks',
  standalone: true,
  template: `
    <h2>Jocks</h2>
    <table data-jocks>
      @for (jock of jocks(); track jock.id) {
        <tr>
          <td>{{ jock.name }}</td>
          <td>{{ jock.voice_id }}</td>
          <td>{{ jock.good_for_genres.join(', ') }}</td>
          <td>
            <button type="button" data-edit (click)="edit(jock)">Edit</button>
            <button type="button" data-remove (click)="remove(jock)">Delete</button>
          </td>
        </tr>
      }
    </table>

    <fieldset>
      <legend>{{ editing() ? 'Edit ' + draft().id : 'New jock' }}</legend>
      <input data-id placeholder="id" [value]="draft().id" (input)="set('id', $event)" />
      <input data-name placeholder="name" [value]="draft().name" (input)="set('name', $event)" />
      <select data-voice [value]="draft().voice_id" (change)="set('voice_id', $event)">
        <option value="">— voice —</option>
        @for (voice of voices(); track voice) {
          <option [value]="voice">{{ voice }}</option>
        }
      </select>
      <input
        data-style
        placeholder="speech style"
        [value]="draft().speech_style"
        (input)="set('speech_style', $event)"
      />
      <input
        data-personality
        placeholder="personality"
        [value]="draft().personality"
        (input)="set('personality', $event)"
      />
      <button type="button" data-save (click)="save()">Save</button>
      @if (editing()) {
        <button type="button" data-cancel (click)="reset()">Cancel</button>
      }
    </fieldset>

    <p data-said>{{ said() }}</p>
  `,
})
export class Jocks {
  private readonly api = inject(AdminApi);

  readonly jocks = signal<Jock[]>([]);
  readonly voices = signal<string[]>([]);
  readonly draft = signal<Jock>(blank());
  readonly editing = signal(false);
  readonly said = signal('');

  constructor() {
    this.load();
    // The VOICES the sidecar can actually produce. A jock nobody can voice
    // fails as a silent break, minutes later, on air.
    this.api.voices().subscribe({
      next: (v) => this.voices.set(v.voices),
      error: () => this.said.set('Could not read the voices; the sidecar may be down.'),
    });
  }

  load(): void {
    this.api.jocks().subscribe({
      next: (j) => this.jocks.set(j),
      error: () => this.said.set('Could not read the jocks.'),
    });
  }

  set(field: keyof Jock, event: Event): void {
    const value = (event.target as HTMLInputElement).value;
    this.draft.set({ ...this.draft(), [field]: value });
  }

  edit(jock: Jock): void {
    this.draft.set({ ...jock });
    this.editing.set(true);
    this.said.set('');
  }

  reset(): void {
    this.draft.set(blank());
    this.editing.set(false);
  }

  save(): void {
    this.said.set('');
    this.api.saveJock(this.draft(), this.editing()).subscribe({
      next: () => {
        // Said out loud: the break already being generated airs in the old
        // voice, and not saying so makes the change read as a bug.
        this.said.set('Saved. A station already on air changes at its next break.');
        this.reset();
        this.load();
      },
      error: (e: { error?: { error?: string } }) =>
        this.said.set(String(e.error?.error ?? 'Could not save that jock.')),
    });
  }

  remove(jock: Jock): void {
    this.api.removeJock(jock.id).subscribe({
      next: (answer) => {
        // WHICH STATIONS lost their jock. One that went quiet without anyone
        // saying so is the hardest kind of change to trace back.
        const n = answer?.unassigned?.length ?? 0;
        this.said.set(
          n === 0
            ? 'Deleted.'
            : `Deleted. ${n} station${n === 1 ? '' : 's'} now have no jock and play music only.`,
        );
        this.load();
      },
      error: () => this.said.set('Could not delete that jock.'),
    });
  }
}
