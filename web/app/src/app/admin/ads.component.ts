import { Component, computed, inject, signal } from '@angular/core';
import { Observable } from 'rxjs';
import { Ad, AdminApi, serverSaid } from '../api/api';
import { FormDialog } from './form-dialog';

/** The slot. dj.MaxAdChars, and the reason the counter exists at all. */
const maxScript = 360;
/** The server's own field caps, so the operator meets a message, not a wall. */
const maxAbout = 1500;
const maxDelivery = 200;

/**
 * The operator sells airtime.
 *
 * They describe a product, read the blurb the model wrote, edit it, and save
 * it. WRITING AND SAVING ARE TWO ACTS: the blurb lands in an editable textarea,
 * typing over it is the same gesture as accepting it, and Save posts what is on
 * screen. An advert re-written on save would air words nobody read.
 *
 * Writing is OPTIONAL. An operator with no model configured types their own
 * script and saves; that is what the 503 falls back to, and it is why Save is
 * never disabled by anything the model says.
 */
@Component({
  selector: 'app-ads',
  standalone: true,
  imports: [FormDialog],
  template: `
    <h2>Ads</h2>

    <table data-ads>
      <thead>
        <tr>
          <th scope="col">Brand</th>
          <th scope="col">Advert</th>
          <th scope="col">Last aired</th>
          <th scope="col">On air</th>
          <th scope="col">Actions</th>
        </tr>
      </thead>
      <tbody>
        @for (ad of ads(); track ad.id) {
          <tr>
            <td data-label="Brand">{{ ad.brand }}</td>
            <td data-label="Advert">{{ ad.script }}</td>
            <!-- NEVER, not the epoch. An advert written this morning and one
                 aired in 1970 must not read alike. -->
            <td data-label="Last aired">{{ aired(ad.last_aired_at) }}</td>
            <td data-label="On air" data-ad-state>{{ ad.enabled ? 'On' : 'Off' }}</td>
            <td data-label="Actions">
              <button type="button" data-edit (click)="edit(ad)">Edit</button>
              <!-- REVERSIBLE, so no confirmation. Jockora-e9a.48 is explicit
                   about that, and the same word the other sections use. -->
              <button type="button" data-toggle (click)="toggle(ad)">
                {{ ad.enabled ? 'Disable' : 'Enable' }}
              </button>
              <button type="button" data-remove data-danger (click)="remove(ad)">Delete</button>
            </td>
          </tr>
        } @empty {
          <!-- LOADING, EMPTY AND FAILED ARE THREE STATES. Jockora-e9a.50: a
               failed read wearing the empty state's words told an operator
               whose server was down that they had nothing. -->
          <tr>
            <td colspan="5">
              @if (!loaded()) {
                <span data-loading>Loading…</span>
              } @else if (!failed()) {
                <span data-empty
                  ><strong>No adverts yet.</strong> Describe a product below and let the DJ read
                  it.</span
                >
              }
            </td>
          </tr>
        }
      </tbody>
    </table>

    <!-- ONE FIELD PER ROW, each label above its control and each explanation
         below it. The fieldset is a wrapping flex row, which suited a row of
         short inputs and put all four of these on one line with their helper
         text flowing between them. -->
    <button type="button" data-add-open (click)="adding.set(true)">Write an advert</button>
    <app-form-dialog
      [title]="editing() ? 'Edit ' + editing()!.brand : 'New advert'"
      [open]="adding() || editing() !== null"
      (closed)="reset()"
    >
      @if (adding() || editing()) {
        <fieldset data-ad-form>
          <legend>{{ editing() ? 'Edit ' + editing()!.brand : 'New advert' }}</legend>

      <label>
        Product or brand name
        <input
          data-brand
          placeholder="Stillwater Ceramics"
          [value]="brand()"
          (input)="brand.set($any($event.target).value)"
        />
        <small>The advert has to say this name, or it is not an advert.</small>
      </label>

      <label>
        What it is about
        <textarea
          data-about
          rows="3"
          [attr.maxlength]="maxAbout"
          placeholder="Hand-thrown mugs, made in a shed in Bacolod. Open Saturdays."
          [value]="about()"
          (input)="about.set($any($event.target).value)"
        ></textarea>
        <small>Your own words. The model writes only from what is here.</small>
      </label>

      <label>
        Delivery
        <input
          data-delivery
          [attr.maxlength]="maxDelivery"
          placeholder="hard sell, deadpan, warm, urgent, late-night"
          [value]="delivery()"
          (input)="delivery.set($any($event.target).value)"
        />
        <small>How the DJ should read it. The advert never says this word itself.</small>
      </label>

      <!-- WITH THE BOX IT FILLS. It used to sit between the Delivery helper
           text and The advert label, belonging to neither by position -- the
           same defect as the station form's Describe it. Jockora-e9a.61.
           DISABLED WHILE BLANK AND WHILE IN FLIGHT, and it says which: a live
           model call takes seconds, and an unlabelled button that does nothing
           reads as broken. -->
      <div data-field data-write-action>
        Write the advert
        <button
          type="button"
          data-write
          [disabled]="writing() || !brand().trim() || !about().trim()"
          (click)="write()"
        >
          {{ writing() ? 'Writing…' : 'Write it' }}
        </button>
        @if (writing()) {
          <!-- WHY IT IS SLOW, in the words the station brief uses. Enrichment is
               serial and shares the model, so a blurb written mid-enrichment
               queues behind a dossier pass. -->
          <small data-writing-note
            >Asking the model. If this is slow, enrichment may be running — you can pause it on
            the Overview.</small
          >
        } @else {
          <small>Reads the three boxes above and fills the one below.</small>
        }
      </div>

      <label>
        The advert
        <!-- ONE TEXTAREA. Not a read-only preview with an edit button: typing
             over the blurb is the same gesture as accepting it. -->
        <textarea
          data-script
          rows="4"
          [value]="script()"
          (input)="script.set($any($event.target).value)"
        ></textarea>
        <!-- INSIDE THE LABEL, because it describes THIS box. It used to sit
             loose in the fieldset between the textarea and Save, at the same
             baseline as both. CODE POINTS, NOT .length: JavaScript counts
             UTF-16 units, so an emoji counts two and the console would say 340
             of 360 while the server refused. Jockora-9jo, one field over. -->
        <small data-count [attr.data-over]="over() ? 'true' : null">
          {{ length() }} of {{ maxScript }} characters{{ over() ? ' — too long for one slot' : '' }}
        </small>
      </label>

      @if (warning(); as w) {
        <!-- AN ADVISORY, NOT AN ERROR, and deliberately not role="alert". An
             operator advertising a real company sees this every time, and a red
             box that means nothing teaches them to ignore red boxes. -->
        <small data-brand-warning>{{ w }}</small>
      }

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
export class Ads {
  private readonly api = inject(AdminApi);

  readonly maxScript = maxScript;
  readonly maxAbout = maxAbout;
  readonly maxDelivery = maxDelivery;

  readonly ads = signal<Ad[]>([]);
  readonly loaded = signal(false);
  readonly failed = signal(false);

  readonly brand = signal('');
  readonly about = signal('');
  readonly delivery = signal('');
  readonly script = signal('');
  readonly warning = signal('');
  readonly writing = signal(false);
  /** Whether the New advert dialog is open. */
  readonly adding = signal(false);
  readonly editing = signal<Ad | null>(null);
  /** Which write the form is waiting for. See write(). */
  private writeSeq = 0;
  readonly said = signal('');

  /** Code points, which is what the server counts. */
  readonly length = computed(() => [...this.script()].length);
  readonly over = computed(() => this.length() > maxScript);

  constructor() {
    this.load();
  }

  load(): void {
    this.api.ads().subscribe({
      next: (a) => {
        this.ads.set(a);
        this.loaded.set(true);
        this.failed.set(false);
      },
      error: () => {
        this.loaded.set(true);
        this.failed.set(true);
        this.said.set('Could not read the adverts.');
      },
    });
  }

  /**
   * When it last aired, as a person reads it.
   *
   * ONE PLACE DECIDES "never", for both the advert that has no timestamp and
   * the one whose timestamp cannot be read. A ternary in the template guarding
   * the first case is redundant with the NaN check that has to be here anyway
   * for the second, and two ways of producing the same word is one of them
   * nothing tests.
   */
  aired(at: string | undefined): string {
    const d = new Date(at ?? '');
    return isNaN(d.getTime()) ? 'never' : d.toLocaleString();
  }

  /**
   * Draft a blurb. SAVES NOTHING, and never clears what the operator typed.
   *
   * THE ANSWER HAS TO PROVE IT STILL APPLIES. A slow response used to land in
   * whichever advert was open when it returned: start a blurb for brand A,
   * press Edit on advert B while it works, and A's copy arrived in B's form --
   * then Save overwrote B with copy about A. The window is wide by this form's
   * own account, since it warns that writing queues behind enrichment.
   *
   * A sequence number rather than an unsubscribe, because the same counter also
   * answers "is a write for THIS form still running", which is what the
   * Writing… label and the disabled button were getting wrong. Jockora-e9a.63.
   */
  write(): void {
    const seq = ++this.writeSeq;
    this.said.set('');
    this.warning.set('');
    this.writing.set(true);
    this.api.writeAd(this.brand(), this.about(), this.delivery()).subscribe({
      next: (w) => {
        if (seq !== this.writeSeq) {
          return;
        }
        this.writing.set(false);
        this.script.set(w.script);
        this.warning.set(w.warning ?? '');
      },
      error: (e: unknown) => {
        // A FAILURE IS STALE THE SAME WAY. A message about another advert's
        // request, on this advert's form, is the same defect wearing different
        // clothes.
        if (seq !== this.writeSeq) {
          return;
        }
        this.writing.set(false);
        // THE SERVER'S OWN SENTENCE. Every one of them -- 503, 502, 504 -- was
        // written to be read, and they name what to do next. The form is left
        // exactly as it was: the operator was not wrong.
        this.said.set(serverSaid(e, 'Could not write that advert.'));
      },
    });
  }

  /**
   * Abandon any write in flight, because the form no longer holds what it was
   * asked for. Bumping the counter is what makes the answer stale.
   */
  private abandonWrite(): void {
    this.writeSeq++;
    this.writing.set(false);
  }

  edit(ad: Ad): void {
    this.abandonWrite();
    // NEVER BOTH AT ONCE.
    this.adding.set(false);
    this.editing.set(ad);
    this.brand.set(ad.brand);
    this.about.set(ad.brief);
    this.delivery.set(ad.delivery);
    this.script.set(ad.script);
    this.warning.set('');
    this.said.set('');
  }

  reset(): void {
    this.abandonWrite();
    this.adding.set(false);
    this.editing.set(null);
    this.brand.set('');
    this.about.set('');
    this.delivery.set('');
    this.script.set('');
    this.warning.set('');
  }

  /**
   * Save what is on screen.
   *
   * NEVER RE-WRITES. The operator either accepted a blurb they read or typed
   * their own, and a second model call here would air words they never saw.
   */
  save(): void {
    this.said.set('');
    const body = {
      brand: this.brand(),
      brief: this.about(),
      delivery: this.delivery(),
      script: this.script(),
    };
    const current = this.editing();
    // Typed as unknown: PUT answers 204 and POST answers an id, and the
    // only thing this cares about is that it worked.
    const call: Observable<unknown> = current
      ? this.api.updateAd(current.id, body)
      : this.api.addAd(body);
    call.subscribe({
      next: () => {
        this.said.set('Saved.');
        this.reset();
        this.load();
      },
      error: (e: unknown) =>
        this.said.set(serverSaid(e, 'Could not save that advert.')),
    });
  }

  /** Pause an advert or put it back on air. NO CONFIRMATION: it is reversible. */
  toggle(ad: Ad): void {
    this.api.setAdEnabled(ad.id, !ad.enabled).subscribe({
      next: () => {
        this.said.set(ad.enabled ? `${ad.brand} is off air.` : `${ad.brand} is back on air.`);
        this.load();
      },
      error: () => this.said.set('Could not change that advert.'),
    });
  }

  remove(ad: Ad): void {
    // ASKED, unlike the other sections. Jockora-e9a.48 is open against Jocks,
    // Accounts and Sources for deleting on one click from a button identical
    // to Edit; this is the one destructive button on this screen.
    if (!confirm(`Delete the ${ad.brand} advert? It stops airing immediately.`)) {
      return;
    }
    this.api.removeAd(ad.id).subscribe({
      next: () => {
        this.said.set('Deleted.');
        this.load();
      },
      error: () => this.said.set('Could not delete that advert.'),
    });
  }
}
