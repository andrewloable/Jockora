import { Component, computed, inject, signal } from '@angular/core';
import { AdminApi, LLMCurrent, LLMForm, LLMProvider } from '../api/api';

/**
 * Which language model the station speaks through.
 *
 * THIS PAGE EXISTS BECAUSE ONE EVENING NEEDED IT THREE TIMES. A provider
 * withdrew a free tier without notice and the station went quiet; every
 * alternative failed differently -- one ignored the schema, one spent its whole
 * budget reasoning, one had no structured output at all. Each fix was an ssh, an
 * edit of a compose file and a restart.
 *
 * So: pick a provider, pick a model FROM WHAT IT ACTUALLY HOSTS, test that
 * exact model, and only then save. Listing is not enough on its own -- a model
 * can be listed, answer, and still be unable to do the job.
 */
@Component({
  selector: 'app-llm',
  standalone: true,
  template: `
    <h2>Language model</h2>
    <!-- WHAT THE MODEL LAST DID. This page tested a configuration once, at
         save time, and then never again -- so on 2026-09-08 the one screen
         dedicated to the language model was the last place that would tell you
         the language model had stopped working. -->
    @if (trouble(); as t) {
      <p role="alert" data-llm-health>{{ t }}</p>
    }

    <p data-llm-intro>
      The DJ writes with this, and so does enrichment. Changing it takes effect on the next break —
      nothing restarts.
    </p>

    <fieldset data-llm-provider>
      <legend>Where the model runs</legend>
      @for (p of providers(); track p.id) {
        <label>
          <input
            type="radio"
            name="provider"
            [value]="p.id"
            [checked]="provider() === p.id"
            (change)="choose(p.id)"
          />
          <strong>{{ p.name }}</strong>
          <small>{{ p.help }}</small>
        </label>
      }
    </fieldset>

    @if (chosen(); as p) {
      <fieldset data-llm-fields>
        <legend>{{ p.name }}</legend>

        @if (p.needs_url) {
          <label>
            Address
            <input data-llm-url [value]="url()" (input)="url.set($any($event.target).value)" />
            <small>{{ p.url_help }}</small>
          </label>
        }
        @if (p.needs_account) {
          <label>
            Account ID
            <input
              data-llm-account
              [value]="account()"
              (input)="account.set($any($event.target).value)"
            />
            <small>{{ p.account_help }}</small>
          </label>
        }
        @if (p.needs_key) {
          <label>
            API key
            <input
              data-llm-key
              type="password"
              [placeholder]="hasKey() ? 'a key is saved — leave blank to keep it' : ''"
              [value]="key()"
              (input)="key.set($any($event.target).value)"
            />
            <!-- The key is stored and NEVER sent back, the same rule a password
                 hash follows. An empty box means keep the one you have. -->
            <small>{{ p.key_help }}</small>
          </label>
        }

        @if (p.needs_model) {
          <label>
            Model
            <!-- PICKED, not typed. A slug typed by hand is a 404 you meet at
                 the next break. -->
            <select data-llm-model (change)="model.set($any($event.target).value)">
              <option value="" [selected]="!model()">choose a model…</option>
              @for (m of models(); track m) {
                <option [value]="m" [selected]="m === model()">{{ m }}</option>
              }
              @if (model() && !models().includes(model())) {
                <option [value]="model()" selected>{{ model() }}</option>
              }
            </select>
            <button type="button" data-llm-list [disabled]="busy()" (click)="list()">
              List models
            </button>
            @if (p.suggested) {
              <small>Known to work here: {{ p.suggested }}</small>
            }
          </label>
        }

        <button type="button" data-llm-test [disabled]="busy()" (click)="test()">Test</button>
        <button type="button" data-llm-save [disabled]="busy()" (click)="save()">
          Save and use it
        </button>
      </fieldset>
    }

    <p data-said>{{ said() }}</p>
  `,
})
export class LLM {
  private readonly api = inject(AdminApi);

  readonly providers = signal<LLMProvider[]>([]);
  readonly provider = signal('');
  readonly url = signal('');
  readonly account = signal('');
  readonly model = signal('');
  readonly key = signal('');
  readonly hasKey = signal(false);
  readonly models = signal<string[]>([]);
  readonly said = signal('');
  readonly busy = signal(false);

  /** What the model last did, as the server reports it. */
  readonly health = signal('ok');

  readonly chosen = computed(() => this.providers().find((p) => p.id === this.provider()));

  /**
   * The reason the model is refusing, or nothing when it is answering.
   *
   * "not configured" is not trouble: no model at all is a working shuffle, and
   * this page is where somebody goes to set the first one.
   */
  readonly trouble = computed(() => {
    const mark = 'degraded: ';
    const h = this.health();
    return h.startsWith(mark) ? `This model is refusing: ${h.slice(mark.length)}` : '';
  });

  constructor() {
    this.api.llm().subscribe({
      next: (r) => {
        this.providers.set(r.providers);
        this.setHealth(r.health);
        this.apply(r.current);
      },
      error: () => this.said.set('Could not read the model settings.'),
    });
  }

  /**
   * Set the health, treating a missing one as fine.
   *
   * NOT DEFENSIVE PADDING: an answer with no health field set the signal to
   * undefined and the computed below threw on undefined.startsWith, which took
   * the entire page down -- the one page an operator opens to fix a model that
   * is refusing. A server mid-upgrade is enough to produce it.
   */
  private setHealth(h: string | undefined): void {
    this.health.set(h ?? 'ok');
  }

  private apply(c: LLMCurrent): void {
    this.provider.set(c.provider ?? '');
    this.url.set(c.url ?? '');
    this.account.set(c.account ?? '');
    this.model.set(c.model ?? '');
    this.hasKey.set(c.has_key);
    this.key.set('');
  }

  /**
   * Switching provider clears the fields of the last one.
   *
   * Leaving a Cloudflare account id behind on an OpenRouter form is a value
   * that will be sent and ignored, and read back as though it meant something.
   */
  choose(id: string): void {
    this.provider.set(id);
    this.models.set([]);
    this.model.set('');
    this.said.set('');
    const p = this.providers().find((x) => x.id === id);
    if (p?.suggested) {
      this.model.set(p.suggested);
    }
  }

  private form(): LLMForm {
    return {
      provider: this.provider(),
      url: this.url(),
      account: this.account(),
      model: this.model(),
      key: this.key(),
    };
  }

  list(): void {
    this.busy.set(true);
    this.said.set('');
    this.api.llmModels(this.form()).subscribe({
      next: (r) => {
        this.models.set(r.models);
        this.busy.set(false);
        this.said.set(`${r.models.length} models available.`);
      },
      error: (e: { error?: { message?: string } }) => {
        this.busy.set(false);
        this.said.set(e.error?.message ?? 'Could not list the models.');
      },
    });
  }

  test(): void {
    this.busy.set(true);
    this.said.set('');
    this.api.llmTest(this.form()).subscribe({
      next: () => {
        this.busy.set(false);
        this.said.set('That model answered and honoured the schema.');
      },
      error: (e: { error?: { message?: string } }) => {
        this.busy.set(false);
        this.said.set(e.error?.message ?? 'That model could not be used.');
      },
    });
  }

  save(): void {
    this.busy.set(true);
    this.said.set('');
    this.api.llmSave(this.form()).subscribe({
      next: (r) => {
        this.apply(r.current);
        // The save answers with the CURRENT health, so a red line goes on its
        // own rather than sitting there until somebody reloads the page.
        this.setHealth(r.health);
        this.busy.set(false);
        this.said.set('Saved. The next break uses it.');
      },
      // The server tests before it saves, so a failure here is the model
      // refusing rather than the form being wrong.
      error: (e: { error?: { message?: string } }) => {
        this.busy.set(false);
        this.said.set(e.error?.message ?? 'That model could not be used, so nothing was saved.');
      },
    });
  }
}
