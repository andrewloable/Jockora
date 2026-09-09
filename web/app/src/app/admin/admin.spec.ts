import { describe, expect, it, beforeEach, vi, afterEach } from 'vitest';
import { TestBed } from '@angular/core/testing';
import { Router, provideRouter } from '@angular/router';
import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { Admin } from './admin';
import { adminRoutes } from './admin.routes';
import { adminGuard } from './admin.guard';

describe('Admin', () => {
  let ctrl: HttpTestingController;

  // The Logs section opens a live connection on mount, and the real
  // EventSource would reach for a socket.
  beforeEach(() => {
    vi.stubGlobal(
      'EventSource',
      class {
        close() {}
        readyState = 0;
      },
    );
  });
  afterEach(() => vi.unstubAllGlobals());

  beforeEach(async () => {
    await TestBed.configureTestingModule({
      imports: [Admin],
      providers: [provideHttpClient(), provideHttpClientTesting(), provideRouter([])],
    }).compileComponents();
    ctrl = TestBed.inject(HttpTestingController);
  });

  // Whatever the section that just mounted asked for. The shell's job is to
  // mount one section; what each of them fetches is its own spec's problem.
  function settle(fixture: { detectChanges(): void }) {
    for (const req of ctrl.match(() => true)) {
      const url = req.request.url;
      req.flush(
        url === '/me'
          ? { name: 'mandark', role: 'admin' }
          : url === '/admin/vocab'
            ? { genres: ['rock'], moods: [] }
            : url === '/admin/voices'
              ? { voices: [] }
              : url.includes('/tracks')
                ? { tracks: [], total: 0 }
                : url === '/admin/overview.json'
                  ? {}
                  : url === '/admin/stations'
                    ? [{ id: 1, name: 'Rock', genre: 'rock', enabled: true, tracks: 90 }]
                    : url === '/admin/logs'
                      ? { records: [], dropped: 0, level: 'INFO' }
                      : url === '/admin/llm'
                        ? { providers: [], current: { has_key: false }, health: 'ok' }
                        : [],
      );
    }
    fixture.detectChanges();
  }

  function mounted() {
    const fixture = TestBed.createComponent(Admin);
    fixture.detectChanges();
    settle(fixture);
    return fixture;
  }

  // ------------------------------------------------------- Jockora-e9a.58 --
  //
  // Eleven form controls across five tabs had NO accessible name at all, and
  // three of them were selects with not even a placeholder to fall back on --
  // one being the accounts role picker, which chooses between listener and
  // admin. A privilege decision announced as an unlabelled combo box.
  //
  // A PLACEHOLDER IS NOT A LABEL. It disappears the moment there is a value, so
  // an operator filling a form cannot check what they typed against what was
  // asked, and returning to a half-filled form gives no field names at all. It
  // also renders in --ink-faint, the lowest-contrast ink in the palette, doing
  // the most important job on the screen.
  //
  // AN AUDIT, not one assertion per field, so a control added tomorrow with no
  // label fails this automatically rather than waiting for the next review.

  /** Every control on screen that a screen reader would announce as unnamed. */
  function unnamed(root: HTMLElement) {
    return [...root.querySelectorAll('input, select, textarea')].filter((c) => {
      const wrapping = c.closest('label')?.textContent?.trim();
      const forId = c.id && root.querySelector(`label[for="${c.id}"]`)?.textContent?.trim();
      const aria = c.getAttribute('aria-label');
      const by = c.getAttribute('aria-labelledby');
      const referenced = by && root.querySelector(`#${by}`)?.textContent?.trim();
      return !(wrapping || forId || aria || referenced);
    });
  }

  /**
   * Open whatever add form this section has, so the audit can see inside it.
   *
   * The add forms moved into dialogs in Jockora-e9a.60, which means an audit
   * that only walks the tabs stops looking at half the console's controls
   * without saying so.
   */
  function openEveryForm(fixture: { nativeElement: HTMLElement; detectChanges(): void }) {
    for (const button of [...fixture.nativeElement.querySelectorAll('[data-add-open]')]) {
      (button as HTMLButtonElement).click();
      fixture.detectChanges();
      settle(fixture);
    }
  }

  /** How a failure names the offender, since an unlabelled control has no name. */
  const describeControl = (c: Element) =>
    `<${c.tagName.toLowerCase()} ${[...c.attributes]
      .map((a) => a.name)
      .filter((n) => n.startsWith('data-'))
      .join(' ')}>`;

  it('labelled control gives every input in the console an accessible name', () => {
    const fixture = mounted();
    const sections = [
      'overview',
      'sources',
      'stations',
      'playlist',
      'jocks',
      'ads',
      'accounts',
      'model',
      'logs',
    ];
    const offenders: string[] = [];
    let counted = 0;

    for (const section of sections) {
      fixture.nativeElement.querySelector(`[data-section="${section}"]`).click();
      fixture.detectChanges();
      settle(fixture);
      // The playlist editor needs a station picked before it mounts at all.
      settle(fixture);
      // AND THE ADD FORMS ARE BEHIND A BUTTON since Jockora-e9a.60 moved them
      // into dialogs. Without this the audit silently stops seeing them, which
      // would turn a passing sweep into proof of nothing.
      openEveryForm(fixture);
      const controls = [...fixture.nativeElement.querySelectorAll('input, select, textarea')];
      counted += controls.length;
      offenders.push(
        ...unnamed(fixture.nativeElement).map((c) => `${section}: ${describeControl(c)}`),
      );
    }

    // COUNTED, so an audit that walked nine empty screens cannot pass. The
    // console has dozens of controls; a number this low means the sections did
    // not render and the loop above proved nothing.
    expect(counted).toBeGreaterThan(20);
    expect(offenders).toEqual([]);
  });

  it('labelled control gives every select an accessible name', () => {
    // The three worst had no placeholder either, so there was nothing at all to
    // announce or to read. Asserted separately because a select cannot fall
    // back on placeholder text the way an input can.
    const fixture = mounted();
    let selects = 0;
    const offenders: string[] = [];
    for (const section of [
      'sources',
      'stations',
      'playlist',
      'jocks',
      'accounts',
      'model',
      'logs',
    ]) {
      fixture.nativeElement.querySelector(`[data-section="${section}"]`).click();
      fixture.detectChanges();
      settle(fixture);
      settle(fixture);
      openEveryForm(fixture);
      selects += fixture.nativeElement.querySelectorAll('select').length;
      offenders.push(
        ...unnamed(fixture.nativeElement)
          .filter((c) => c.tagName === 'SELECT')
          .map((c) => `${section}: ${describeControl(c)}`),
      );
    }
    expect(selects).toBeGreaterThan(4);
    expect(offenders).toEqual([]);
  });

  // ------------------------------------------------------- Jockora-e9a.66 --
  //
  // Nothing in either app signed a person out. A search for logout, signOut and
  // sign out across the listener and the console returned exactly one hit, and
  // it was a MESSAGE rather than a control: users.component telling the
  // operator a user "stays signed in until they sign out" -- an instruction to
  // do something the interface did not offer.
  //
  // Accounts are admin-created with no self-registration, the intended device
  // is a shared one, and a session lasts thirty days.

  it('sign out ends the session and returns to the login page', async () => {
    const fixture = mounted();
    const nav: unknown[][] = [];
    const router = TestBed.inject(Router);
    vi.spyOn(router, 'navigate').mockImplementation((c: readonly unknown[]) => {
      nav.push([...c]);
      return Promise.resolve(true);
    });

    const out = fixture.nativeElement.querySelector('[data-sign-out]') as HTMLButtonElement;
    expect(out).not.toBeNull();
    // BESIDE THE NAME IT ENDS. The masthead already says whose session it is.
    expect(out.closest('[data-masthead]')).not.toBeNull();

    out.click();
    // SERVER-SIDE, so a copy of the cookie stops working too -- not merely
    // forgotten in this browser.
    const req = ctrl.expectOne('/logout');
    expect(req.request.method).toBe('POST');
    req.flush(null);
    await Promise.resolve();
    fixture.detectChanges();

    expect(nav).toEqual([['/login']]);
  });

  it('sign out lands on the login page even when the server refuses', async () => {
    // A session that cannot be ended server-side must still be dropped here,
    // or the operator is stuck signed in on a device they are handing over.
    const fixture = mounted();
    const nav: unknown[][] = [];
    vi.spyOn(TestBed.inject(Router), 'navigate').mockImplementation((c: readonly unknown[]) => {
      nav.push([...c]);
      return Promise.resolve(true);
    });

    (fixture.nativeElement.querySelector('[data-sign-out]') as HTMLButtonElement).click();
    ctrl.expectOne('/logout').error(new ProgressEvent('failed'));
    await Promise.resolve();
    fixture.detectChanges();
    expect(nav).toEqual([['/login']]);
  });

  it('opens on the overview', () => {
    const fixture = mounted();
    expect(fixture.componentInstance.showing()).toBe('overview');
    expect(fixture.nativeElement.querySelector('app-overview')).not.toBeNull();
  });

  it('offers every section', () => {
    const nav = mounted().nativeElement.querySelectorAll('nav button');
    expect(Array.from(nav).map((b) => (b as HTMLElement).textContent?.trim())).toEqual([
      'overview',
      'sources',
      'stations',
      'playlist',
      'jocks',
      'ads',
      'accounts',
      'model',
      'logs',
    ]);
  });

  it('mounts one section at a time', () => {
    // NINE sections polling the server at once for pages nobody is looking at
    // is a load the operator did not ask for -- and Logs holds an open
    // EventSource, so one per section would be nine live connections against
    // a server that caps them at eight.
    const fixture = mounted();
    for (const [section, tag] of [
      ['sources', 'app-sources'],
      ['stations', 'app-stations'],
      ['playlist', 'app-playlist'],
      ['jocks', 'app-jocks'],
      ['ads', 'app-ads'],
      ['accounts', 'app-users'],
      ['model', 'app-llm'],
      ['logs', 'app-logs'],
    ] as const) {
      fixture.nativeElement.querySelector(`[data-section="${section}"]`).click();
      fixture.detectChanges();
      settle(fixture);
      // The playlist editor needs a station picked before it mounts at all.
      settle(fixture);
      expect(fixture.nativeElement.querySelector(tag)).not.toBeNull();
      expect(fixture.nativeElement.querySelectorAll('app-overview').length).toBe(0);
    }
  });

  it('picks a station for the playlist editor, and lets it be changed', () => {
    // The editor edits ONE station, so the console has to say which.
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-section="playlist"]').click();
    fixture.detectChanges();
    ctrl.expectOne('/admin/stations').flush([
      { id: 1, name: 'Rock', genre: 'rock', enabled: true, tracks: 90 },
      { id: 7, name: 'Calm', genre: 'ambient', enabled: true, tracks: 60 },
    ]);
    fixture.detectChanges();
    expect(fixture.componentInstance.picked()).toBe(1);
    settle(fixture);

    const select = fixture.nativeElement.querySelector('[data-pick-station]');
    expect(Array.from(select.options).map((o) => (o as HTMLOptionElement).value)).toEqual([
      '1',
      '7',
    ]);
    select.value = '7';
    select.dispatchEvent(new Event('change'));
    fixture.detectChanges();
    expect(fixture.componentInstance.picked()).toBe(7);
    settle(fixture);
  });

  it('says so rather than showing an empty editor', () => {
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-section="playlist"]').click();
    fixture.detectChanges();
    ctrl.expectOne('/admin/stations').flush([]);
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('app-playlist')).toBeNull();
    expect(fixture.nativeElement.querySelector('[data-no-stations]').textContent).toContain(
      'No stations yet',
    );
  });

  it('shows nothing to edit when the stations cannot be read', () => {
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-section="playlist"]').click();
    fixture.detectChanges();
    ctrl.expectOne('/admin/stations').flush(null, { status: 500, statusText: 'Error' });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('app-playlist')).toBeNull();
  });

  it('opens the playlist ON the station whose row was clicked', () => {
    // The Playlist button on a station row emits an output the shell never
    // bound, so it did nothing at all. And when it does something, it has to
    // land on THAT station -- not on whichever happens to be first.
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-section="stations"]').click();
    fixture.detectChanges();
    settle(fixture);

    // Clicked for real, so the output BINDING is exercised too -- it was the
    // binding that was missing, not the handler.
    const row = fixture.nativeElement.querySelector('[data-playlist]');
    expect(row).not.toBeNull();
    row.click();
    fixture.detectChanges();
    ctrl.expectOne('/admin/stations').flush([
      { id: 1, name: 'Rock', genre: 'rock', enabled: true, tracks: 90 },
      { id: 7, name: 'Calm', genre: 'ambient', enabled: true, tracks: 60 },
    ]);
    fixture.detectChanges();

    expect(fixture.componentInstance.showing()).toBe('playlist');
    // The station whose row was clicked, not whichever is first.
    expect(fixture.componentInstance.picked()).toBe(1);
    settle(fixture);
  });

  it('marks the open section for a screen reader', () => {
    const fixture = mounted();
    expect(
      fixture.nativeElement.querySelector('[data-section="overview"]').getAttribute('aria-current'),
    ).toBe('page');
    expect(
      fixture.nativeElement.querySelector('[data-section="jocks"]').getAttribute('aria-current'),
    ).toBeNull();
  });

  it('is guarded', () => {
    // Without this the console renders for a listener for as long as it takes
    // the redirect to happen, and every one of its calls 403s in the console.
    expect(adminRoutes[0].path).toBe('');
    expect(adminRoutes[0].component).toBe(Admin);
    expect(adminRoutes[0].canActivate).toEqual([adminGuard]);
  });

  it('says who is signed in, so the title is not read as a name', () => {
    // Reported as "i logged in as mandark but the admin page shows operator":
    // the title said "Jockora — operator", meaning the operator CONSOLE, and a
    // page that never named the account left that as the only candidate.
    const fixture = mounted();
    const who = fixture.nativeElement.querySelector('[data-whoami]').textContent;
    expect(who).toContain('mandark');
    expect(who).toContain('admin');
    expect(fixture.nativeElement.querySelector('h1').textContent).toContain('console');
  });

  it('names nobody when the session has gone', () => {
    const fixture = TestBed.createComponent(Admin);
    fixture.detectChanges();
    ctrl.expectOne('/me').flush(null, { status: 401, statusText: 'Unauthorized' });
    for (const req of ctrl.match(() => true)) {
      req.flush({});
    }
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-whoami]')).toBeNull();
  });

  it('opens the model page', () => {
    const fixture = mounted();
    fixture.nativeElement.querySelector('[data-section="model"]').click();
    fixture.detectChanges();
    ctrl.expectOne('/admin/llm').flush({ providers: [], current: { has_key: false } });
    fixture.detectChanges();
    expect(fixture.nativeElement.textContent).toContain('Language model');
  });
});
