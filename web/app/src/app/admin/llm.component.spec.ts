import { describe, expect, it, beforeEach } from 'vitest';
import { TestBed } from '@angular/core/testing';
import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { LLM } from './llm.component';

// CHOOSING THE MODEL FROM THE CONSOLE. One evening needed this three times: a
// provider withdrew a free tier and the station went quiet, and every
// alternative failed differently. Each fix was an ssh and a restart.

const providers = [
  {
    id: 'llamacpp',
    name: 'Local llama.cpp',
    help: 'A model running on your own hardware.',
    needs_url: true,
    needs_key: false,
    needs_account: false,
    needs_model: false,
    url_help: 'Usually http://127.0.0.1:8081.',
  },
  {
    id: 'openrouter',
    name: 'OpenRouter',
    help: 'One key, many providers.',
    needs_url: false,
    needs_key: true,
    needs_account: false,
    needs_model: true,
    key_help: 'Create one at https://openrouter.ai/keys',
    suggested: 'meta-llama/llama-3.3-70b-instruct',
  },
  {
    id: 'cloudflare',
    name: 'Cloudflare Workers AI',
    help: 'Cheap, fast, priced per token.',
    needs_url: false,
    needs_key: true,
    needs_account: true,
    needs_model: true,
    key_help:
      'Dashboard, AI, Workers AI, Use REST API: https://dash.cloudflare.com/profile/api-tokens',
    account_help: 'The account ID on any dashboard page, or run wrangler whoami',
    suggested: '@cf/meta/llama-3.3-70b-instruct-fp8-fast',
  },
];

describe('LLM', () => {
  let ctrl: HttpTestingController;

  beforeEach(async () => {
    await TestBed.configureTestingModule({
      imports: [LLM],
      providers: [provideHttpClient(), provideHttpClientTesting()],
    }).compileComponents();
    ctrl = TestBed.inject(HttpTestingController);
  });

  function mounted(current: object = { has_key: false }, health = 'ok') {
    const fixture = TestBed.createComponent(LLM);
    fixture.detectChanges();
    ctrl.expectOne('/admin/llm').flush({ providers, current, health });
    fixture.detectChanges();
    return fixture;
  }

  function pick(fixture: { nativeElement: HTMLElement }, id: string) {
    const radio = fixture.nativeElement.querySelector(
      `[data-llm-provider] input[value="${id}"]`,
    ) as HTMLInputElement;
    radio.checked = true;
    radio.dispatchEvent(new Event('change'));
    (fixture as unknown as { detectChanges(): void }).detectChanges();
  }

  // ------------------------------------------------------- model outage --
  //
  // 2026-09-08: the token saved on this very page stopped being accepted at
  // 02:58 and the page went on showing the configuration as though it were
  // fine. It tested the model once, at save time, and then never again -- so
  // the one screen dedicated to the language model was the last place that
  // would tell you the language model had stopped working.

  it('model outage names the reason on the page that fixes it', () => {
    const el = mounted(
      { provider: 'cloudflare', has_key: true },
      'degraded: rejected the API key',
    ).nativeElement;
    const alert = el.querySelector('[data-llm-health]');
    expect(alert).not.toBeNull();
    expect(alert.textContent).toContain('rejected the API key');
    expect(alert.getAttribute('role')).toBe('alert');
  });

  it('model outage says nothing while the model is answering', () => {
    const el = mounted({ provider: 'cloudflare', has_key: true }, 'ok').nativeElement;
    expect(el.querySelector('[data-llm-health]')).toBeNull();
  });

  it('model outage clears the warning once a new configuration is saved', () => {
    const fixture = mounted(
      { provider: 'cloudflare', has_key: true },
      'degraded: rejected the API key',
    );
    expect(fixture.nativeElement.querySelector('[data-llm-health]')).not.toBeNull();

    fixture.nativeElement.querySelector('[data-llm-key]').value = 'a-fresh-token';
    fixture.nativeElement.querySelector('[data-llm-key]').dispatchEvent(new Event('input'));
    fixture.nativeElement.querySelector('[data-llm-save]').click();
    ctrl.expectOne('/admin/llm').flush({
      providers,
      current: { provider: 'cloudflare', has_key: true },
      health: 'ok',
    });
    fixture.detectChanges();
    // The save answers with the CURRENT health, so the red line goes on its
    // own rather than sitting there until somebody reloads the page.
    expect(fixture.nativeElement.querySelector('[data-llm-health]')).toBeNull();
  });

  it('model outage survives a server that sends no health at all', () => {
    // An answer with no health field set the signal to undefined and the
    // computed threw on undefined.startsWith, which took down the one page an
    // operator opens to fix a model that is refusing. A server mid-upgrade is
    // enough to produce it.
    const fixture = TestBed.createComponent(LLM);
    fixture.detectChanges();
    ctrl.expectOne('/admin/llm').flush({ providers, current: { has_key: false } });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-llm-health]')).toBeNull();
    expect(fixture.nativeElement.querySelector('[data-llm-provider]')).not.toBeNull();
  });

  it('explains every provider and what it needs', () => {
    const el = mounted().nativeElement;
    const text = el.querySelector('[data-llm-provider]').textContent;
    for (const p of providers) {
      expect(text).toContain(p.name);
      expect(text).toContain(p.help);
    }
  });

  it('shows only the fields the chosen provider wants', () => {
    const fixture = mounted();
    pick(fixture, 'llamacpp');
    const el = fixture.nativeElement;
    expect(el.querySelector('[data-llm-url]')).toBeTruthy();
    // A local server needs no key and no model: it was started with one.
    expect(el.querySelector('[data-llm-key]')).toBeNull();
    expect(el.querySelector('[data-llm-model]')).toBeNull();

    // Typing the address of a local server.
    const url = el.querySelector('[data-llm-url]') as HTMLInputElement;
    url.value = 'http://192.168.0.254:8081';
    url.dispatchEvent(new Event('input'));
    expect(fixture.componentInstance.url()).toBe('http://192.168.0.254:8081');

    pick(fixture, 'cloudflare');
    expect(el.querySelector('[data-llm-account]')).toBeTruthy();
    expect(el.querySelector('[data-llm-key]')).toBeTruthy();
    expect(el.querySelector('[data-llm-url]')).toBeNull();
    // And it says where the account id and the token come from.
    expect(el.querySelector('[data-llm-fields]').textContent).toContain('wrangler whoami');
    expect(el.querySelector('[data-llm-fields]').textContent).toContain('dash.cloudflare.com');
  });

  it('opens on a model known to work rather than an empty box', () => {
    const fixture = mounted();
    pick(fixture, 'cloudflare');
    expect(fixture.componentInstance.model()).toBe('@cf/meta/llama-3.3-70b-instruct-fp8-fast');
  });

  it('lists the models the platform actually hosts', () => {
    const fixture = mounted();
    pick(fixture, 'cloudflare');
    fixture.nativeElement.querySelector('[data-llm-account]').value = 'acc';
    fixture.nativeElement.querySelector('[data-llm-account]').dispatchEvent(new Event('input'));
    fixture.nativeElement.querySelector('[data-llm-list]').click();

    const req = ctrl.expectOne('/admin/llm/models');
    expect(req.request.body.provider).toBe('cloudflare');
    req.flush({ models: ['@cf/a', '@cf/b'] });
    fixture.detectChanges();

    const options = [...fixture.nativeElement.querySelectorAll('[data-llm-model] option')].map(
      (o) => (o as HTMLOptionElement).value,
    );
    expect(options).toContain('@cf/a');
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('2 models');
  });

  it('tests a model without committing to it', () => {
    const fixture = mounted();
    pick(fixture, 'openrouter');
    fixture.nativeElement.querySelector('[data-llm-test]').click();
    ctrl.expectOne('/admin/llm/test').flush({ ok: true });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('honoured');
    ctrl.expectNone('/admin/llm');
  });

  it('says why a model was refused, in the model own words', () => {
    // The two failures met live were "ignored the JSON schema" and "spent its
    // budget reasoning". Neither is guessable from "something went wrong".
    const fixture = mounted();
    pick(fixture, 'openrouter');
    fixture.nativeElement.querySelector('[data-llm-test]').click();
    ctrl.expectOne('/admin/llm/test').flush(
      { field: 'model', message: 'that model ignored the JSON schema' },
      {
        status: 400,
        statusText: 'Bad Request',
      },
    );
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain(
      'ignored the JSON schema',
    );
  });

  it('saves and reports what is now in use', () => {
    const fixture = mounted();
    pick(fixture, 'openrouter');
    fixture.nativeElement.querySelector('[data-llm-key]').value = 'sk-test';
    fixture.nativeElement.querySelector('[data-llm-key]').dispatchEvent(new Event('input'));
    fixture.nativeElement.querySelector('[data-llm-save]').click();

    const req = ctrl.expectOne('/admin/llm');
    expect(req.request.method).toBe('PUT');
    expect(req.request.body.key).toBe('sk-test');
    req.flush({ current: { provider: 'openrouter', model: 'm', has_key: true } });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('next break');
    // The key box is cleared and the page says one is saved, because the key
    // never comes back and an empty box means keep it.
    expect(fixture.componentInstance.key()).toBe('');
    expect(fixture.componentInstance.hasKey()).toBe(true);
  });

  it('says nothing was saved when the model is refused', () => {
    const fixture = mounted();
    pick(fixture, 'openrouter');
    fixture.nativeElement.querySelector('[data-llm-save]').click();
    ctrl.expectOne('/admin/llm').flush(null, { status: 400, statusText: 'Bad Request' });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain(
      'nothing was saved',
    );
  });

  it('starts from what is already configured', () => {
    const fixture = mounted({
      provider: 'cloudflare',
      account: 'acc',
      model: '@cf/x',
      has_key: true,
    });
    expect(fixture.componentInstance.provider()).toBe('cloudflare');
    expect(fixture.componentInstance.account()).toBe('acc');
    // A saved key is announced, never shown.
    const key = fixture.nativeElement.querySelector('[data-llm-key]') as HTMLInputElement;
    expect(key.value).toBe('');
    expect(key.placeholder).toContain('leave blank to keep it');
  });

  it('forgets the last provider fields when another is chosen', () => {
    // A Cloudflare account id left on an OpenRouter form is a value that gets
    // sent, ignored, and read back as though it meant something.
    const fixture = mounted({
      provider: 'cloudflare',
      account: 'acc',
      model: '@cf/x',
      has_key: true,
    });
    pick(fixture, 'openrouter');
    expect(fixture.componentInstance.model()).toBe('meta-llama/llama-3.3-70b-instruct');
    expect(fixture.componentInstance.models()).toEqual([]);
  });

  it('says so when the settings or a listing cannot be read', () => {
    const fixture = TestBed.createComponent(LLM);
    fixture.detectChanges();
    ctrl.expectOne('/admin/llm').flush(null, { status: 500, statusText: 'Error' });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain(
      'Could not read',
    );
  });

  it('says so when a listing fails', () => {
    const fixture = mounted();
    pick(fixture, 'cloudflare');
    fixture.nativeElement.querySelector('[data-llm-list]').click();
    ctrl.expectOne('/admin/llm/models').flush(null, { status: 400, statusText: 'Bad Request' });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain(
      'Could not list',
    );
  });

  it('works for a provider that suggests no model', () => {
    // Not every provider has a model this project has measured.
    const fixture = TestBed.createComponent(LLM);
    fixture.detectChanges();
    ctrl.expectOne('/admin/llm').flush({
      providers: [{ ...providers[1], suggested: undefined }],
      current: { has_key: false },
    });
    fixture.detectChanges();
    pick(fixture, 'openrouter');
    expect(fixture.componentInstance.model()).toBe('');
    expect(fixture.nativeElement.querySelector('[data-llm-fields]').textContent).not.toContain(
      'Known to work',
    );
  });

  it('falls back to its own words when a refusal carries none', () => {
    const fixture = mounted();
    pick(fixture, 'openrouter');
    fixture.nativeElement.querySelector('[data-llm-test]').click();
    ctrl.expectOne('/admin/llm/test').flush(null, { status: 502, statusText: 'Bad Gateway' });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain(
      'could not be used',
    );
  });

  it('keeps a model that is not in the list, so a saved one is not lost', () => {
    const fixture = mounted({ provider: 'openrouter', model: 'something/custom', has_key: true });
    const options = [...fixture.nativeElement.querySelectorAll('[data-llm-model] option')].map(
      (o) => (o as HTMLOptionElement).value,
    );
    expect(options).toContain('something/custom');
    // And it can be changed by hand through the select.
    const select = fixture.nativeElement.querySelector('[data-llm-model]') as HTMLSelectElement;
    select.value = 'something/custom';
    select.dispatchEvent(new Event('change'));
    fixture.detectChanges();
    expect(fixture.componentInstance.model()).toBe('something/custom');
  });
});
