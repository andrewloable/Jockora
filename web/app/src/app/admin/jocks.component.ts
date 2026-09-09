import { Component, OnDestroy, inject, signal } from '@angular/core';
import { AdminApi, Jock, serverSaid } from '../api/api';
import { FormDialog } from './form-dialog';

/**
 * A name turned into the identifier the seeded jocks already use.
 *
 * "Sunny Marchetti" is sunny_marchetti, which is also the attribution string
 * the listener feedback writes -- so this is the existing convention rather
 * than a new one.
 */
export function slug(name: string): string {
  return name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '_')
    .replace(/^_+|_+$/g, '');
}

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
  imports: [FormDialog],
  template: `
    <h2>Jocks</h2>
    <table data-jocks>
      <thead>
        <tr>
          <th>Name</th>
          <th>Voice</th>
          <th>Good for</th>
          <!-- THE TWO FIELDS THAT DECIDE HOW A DJ SOUNDS, and the whole point
               of a persona in this product. They were invisible here, so
               telling two jocks apart meant opening Edit on each in turn. -->
          <th>Speech style</th>
          <th>Personality</th>
          <th>Actions</th>
        </tr>
      </thead>
      <tbody>
        @for (jock of jocks(); track jock.id) {
          <tr>
            <td data-label="Name">{{ jock.name }}</td>
            <td data-label="Voice">{{ jock.voice_id }}</td>
            <td data-label="Good for">{{ jock.good_for_genres.join(', ') }}</td>
            <!-- TRUNCATED BY THE COLUMN, NEVER BY THE DATA. The full text is on
                 the row, so the phone card shows all of it and a hover on the
                 desktop table reveals the rest. -->
            <td data-label="Speech style" data-style-cell [title]="jock.speech_style">
              {{ jock.speech_style }}
            </td>
            <td data-label="Personality" data-personality-cell [title]="jock.personality">
              {{ jock.personality }}
            </td>
            <!-- LABELLED like the rest: below 48rem every row becomes a card,
               and these three buttons were drawn past the right edge of a
               390px phone with nothing to scroll. -->
            <td data-label="Actions">
              <button
                type="button"
                data-row-preview
                [disabled]="!jock.voice_id || previewing() !== null"
                (click)="preview(jock.voice_id)"
              >
                {{ previewing() === jock.voice_id ? 'Speaking…' : 'Hear it' }}
              </button>
              <button type="button" data-edit (click)="edit(jock)">Edit</button>
              <button type="button" data-remove data-danger (click)="remove(jock)">Delete</button>
            </td>
          </tr>
        } @empty {
          <!-- LOADING AND EMPTY ARE NOT THE SAME THING and both drew a table
               with no rows, so a slow first paint told a new operator their
               library was empty. Jockora-e9a.50. -->
          <tr>
            <td colspan="6">
              @if (!loaded()) {
                <span data-loading>Loading…</span>
              } @else if (!failed()) {
                <span data-empty
                  ><strong>No jocks yet.</strong> Add a jock below, then give a station its
                  voice.</span
                >
              }
            </td>
          </tr>
        }
      </tbody>
    </table>

    <button type="button" data-add-open (click)="adding.set(true)">Add a jock</button>
    <app-form-dialog
      [title]="editing() ? 'Edit ' + draft().name : 'New jock'"
      [open]="adding() || editing()"
      (closed)="reset()"
    >
      @if (adding() || editing()) {
        <fieldset data-jock-form>
          <legend>{{ editing() ? 'Edit ' + draft().id : 'New jock' }}</legend>
      <!-- THE NAME COMES FIRST, because it is the only thing on this form an
           operator actually has in mind. The id follows FROM it. -->
      <label>
        Name
        <input
          data-name
          placeholder="Sunny Marchetti"
          [value]="draft().name"
          (input)="setName($event)"
        />
        <small>What the DJ is called on air, and what the identifier is made from.</small>
      </label>
      <!-- DERIVED, NOT DEMANDED. This used to be the first field, a free-text
           box whose placeholder was the word "id", asking a person to invent a
           stable machine key with no stated rules and no sign of what happens
           if it collides. It is still editable -- somebody who cares can set
           it -- but somebody who does not never has to look. -->
      <label>
        Identifier
        <input data-id [value]="draft().id" (input)="setId($event)" [readOnly]="editing()" />
        <small>{{
          editing()
            ? 'Fixed once a jock exists: stations and persona files refer to it.'
            : 'Made from the name. Change it if you want a different one.'
        }}</small>
      </label>
      <label>
        Voice
        <select data-voice (change)="set('voice_id', $event)">
          <option value="" [selected]="!draft().voice_id">— voice —</option>
          @for (voice of voices(); track voice) {
            <option [value]="voice" [selected]="voice === draft().voice_id">{{ voice }}</option>
          }
        </select>
        <small>Which speech voice reads this jock's breaks.</small>
      </label>
      <!-- THE SAME SHAPE AS A FIELD, so it lines up with the controls beside it
           rather than sitting below them. A bare button has no label header, and
           the fieldset aligns on the bottom of each box -- so without this the
           button hung 12px low. It cannot be a real label: a button is itself
           labelable, so a label wrapping one is invalid. -->
      <div data-field>
        Preview
        <button
          type="button"
          data-preview
          [disabled]="!draft().voice_id || previewing() !== null"
          (click)="preview(draft().voice_id)"
        >
          {{ previewing() === draft().voice_id ? 'Speaking…' : 'Hear it' }}
        </button>
        <small>Speaks one line, so you hear it before you save.</small>
      </div>
      <label>
        Speech style
        <input
          data-style
          placeholder="clipped, no filler, never repeats a phrase"
          [value]="draft().speech_style"
          (input)="set('speech_style', $event)"
        />
        <small>How they talk. The listener hears this more than anything else here.</small>
      </label>
      <label>
        Personality
        <input
          data-personality
          placeholder="dry, fond of the records, unimpressed by everything else"
          [value]="draft().personality"
          (input)="set('personality', $event)"
        />
        <small>Who they are. The persona card is ground truth and never drifts.</small>
      </label>
          <div data-form-actions>
            <button type="button" data-save (click)="save()">Save</button>
            <button type="button" data-cancel (click)="reset()">Cancel</button>
          </div>
        </fieldset>
      }
    </app-form-dialog>

    <p data-said>{{ said() }}</p>
  `,
})
export class Jocks implements OnDestroy {
  private readonly api = inject(AdminApi);

  readonly jocks = signal<Jock[]>([]);
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
  readonly voices = signal<string[]>([]);
  readonly draft = signal<Jock>(blank());
  /** Whether the New jock dialog is open. */
  readonly adding = signal(false);
  readonly editing = signal(false);
  /** True once the operator has set an id by hand, so it stops being derived. */
  readonly idTyped = signal(false);
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
      next: (j) => {
        this.jocks.set(j);
        this.loaded.set(true);
        this.failed.set(false);
      },
      error: () => {
        this.loaded.set(true);
        this.failed.set(true);
        this.said.set('Could not read the jocks.');
      },
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

  ngOnDestroy(): void {
    // THE SECTIONS ARE A SWITCH, so leaving Jocks destroys this component --
    // and a preview left playing goes on talking over a console that no longer
    // shows the jock it belongs to, with its blob held until the tab closes.
    // The same reason the dial and the rescan poller stop themselves.
    this.stop();
  }

  set(field: keyof Jock, event: Event): void {
    const value = (event.target as HTMLInputElement).value;
    this.draft.set({ ...this.draft(), [field]: value });
  }

  /**
   * The name, and the id that follows from it.
   *
   * Only while the id is still the derived one: the moment an operator types
   * their own, a later keystroke in the name box must not overwrite it. An
   * existing jock never re-derives at all, because the id is the stable key
   * that stations and persona files refer to and renaming must not move it.
   */
  setName(event: Event): void {
    const name = (event.target as HTMLInputElement).value;
    const derive = !this.editing() && !this.idTyped();
    this.draft.set({ ...this.draft(), name, id: derive ? slug(name) : this.draft().id });
  }

  setId(event: Event): void {
    this.idTyped.set(true);
    this.set('id', event);
  }

  edit(jock: Jock): void {
    // NEVER BOTH AT ONCE.
    this.adding.set(false);
    this.draft.set({ ...jock });
    this.editing.set(true);
    this.idTyped.set(true);
    this.said.set('');
  }

  reset(): void {
    this.draft.set(blank());
    this.editing.set(false);
    this.adding.set(false);
    this.idTyped.set(false);
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
      error: (e: unknown) =>
        this.said.set(serverSaid(e, 'Could not save that jock.')),
    });
  }

  remove(jock: Jock): void {
    // ASKED, because a persona card is hand-written content and deleting one
    // unassigns it from every station that used it. The button beside it is
    // Edit and looked identical.
    if (!confirm(`Delete ${jock.name}? Any station using this jock loses it.`)) {
      return;
    }
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
