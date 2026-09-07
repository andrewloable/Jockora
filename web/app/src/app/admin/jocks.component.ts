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
      <thead>
        <tr>
          <th>Name</th>
          <th>Voice</th>
          <th>Good for</th>
          <th>Actions</th>
        </tr>
      </thead>
      <tbody>
      @for (jock of jocks(); track jock.id) {
        <tr>
          <td>{{ jock.name }}</td>
          <td>{{ jock.voice_id }}</td>
          <td>{{ jock.good_for_genres.join(', ') }}</td>
          <td>
            <button
              type="button"
              data-row-preview
              [disabled]="!jock.voice_id || previewing() !== null"
              (click)="preview(jock.voice_id)"
            >
              {{ previewing() === jock.voice_id ? 'Speaking…' : 'Hear it' }}
            </button>
            <button type="button" data-edit (click)="edit(jock)">Edit</button>
            <button type="button" data-remove (click)="remove(jock)">Delete</button>
          </td>
        </tr>
      }
      </tbody>
    </table>

    <fieldset>
      <legend>{{ editing() ? 'Edit ' + draft().id : 'New jock' }}</legend>
      <input data-id placeholder="id" [value]="draft().id" (input)="set('id', $event)" />
      <input data-name placeholder="name" [value]="draft().name" (input)="set('name', $event)" />
      <select data-voice (change)="set('voice_id', $event)">
        <option value="" [selected]="!draft().voice_id">— voice —</option>
        @for (voice of voices(); track voice) {
          <option [value]="voice" [selected]="voice === draft().voice_id">{{ voice }}</option>
        }
      </select>
      <button
        type="button"
        data-preview
        [disabled]="!draft().voice_id || previewing() !== null"
        (click)="preview(draft().voice_id)"
      >
        {{ previewing() === draft().voice_id ? 'Speaking…' : 'Hear it' }}
      </button>
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
  readonly previewing = signal<string | null>(null);

  // The element that plays the preview. Held so a second click stops the first
  // one rather than talking over it.
  private audio: HTMLAudioElement | null = null;

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

  /**
   * Speak the selected voice.
   *
   * A voice is a name like "am_fenrir", which tells an operator nothing. This
   * renders one line through the SAME sidecar the DJ uses, so what they hear is
   * what a listener will hear -- the alternative is picking by name and finding
   * out on air.
   */
  preview(voice: string): void {
    if (!voice || this.previewing() !== null) {
      return;
    }
    this.stop();
    this.previewing.set(voice);
    this.said.set('');
    this.api.previewVoice(voice).subscribe({
      next: (clip) => {
        this.previewing.set(null);
        this.audio = new Audio(URL.createObjectURL(clip));
        // Revoking on end, or every preview leaks a blob for as long as the
        // console stays open.
        this.audio.addEventListener('ended', () => this.stop());
        void this.audio.play();
      },
      error: () => {
        this.previewing.set(null);
        this.said.set('Could not speak that voice. The sidecar may be down.');
      },
    });
  }

  private stop(): void {
    if (this.audio) {
      this.audio.pause();
      URL.revokeObjectURL(this.audio.src);
      this.audio = null;
    }
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
