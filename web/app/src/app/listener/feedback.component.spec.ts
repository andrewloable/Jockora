import { describe, expect, it, beforeEach } from 'vitest';
import { TestBed } from '@angular/core/testing';
import { Component, signal } from '@angular/core';
import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { Feedback } from './feedback.component';

@Component({
  standalone: true,
  imports: [Feedback],
  template: `<app-feedback [station]="station()" [transcript]="transcript()" />`,
})
class Host {
  readonly station = signal<number | null>(null);
  readonly transcript = signal('');
}

describe('Feedback', () => {
  let ctrl: HttpTestingController;

  beforeEach(async () => {
    await TestBed.configureTestingModule({
      imports: [Host],
      providers: [provideHttpClient(), provideHttpClientTesting()],
    }).compileComponents();
    ctrl = TestBed.inject(HttpTestingController);
  });

  function mounted() {
    const fixture = TestBed.createComponent(Host);
    fixture.detectChanges();
    return fixture;
  }

  it('offers nothing until a break has aired', () => {
    // Nothing to rate means nothing to press.
    const fixture = mounted();
    expect(fixture.nativeElement.querySelector('[data-thumbsdown]')).toBeNull();
  });

  it('sends an attributed thumbs-down and says it landed', () => {
    const fixture = mounted();
    fixture.componentInstance.station.set(3);
    fixture.componentInstance.transcript.set('Three in the morning.');
    fixture.detectChanges();

    fixture.nativeElement.querySelector('[data-thumbsdown]').click();
    const req = ctrl.expectOne('/feedback');
    expect(req.request.body).toEqual({ station_id: 3, verdict: 'down' });
    req.flush(null);
    fixture.detectChanges();

    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('Noted');
    // Once. Pressing it again on the same break is not more information.
    expect(fixture.nativeElement.querySelector('[data-thumbsdown]').disabled).toBe(true);
  });

  it('has no thumbs-up', () => {
    // Asking a listener to rate every break turns listening into work.
    const fixture = mounted();
    fixture.componentInstance.transcript.set('x');
    fixture.detectChanges();
    const text = fixture.nativeElement.textContent.toLowerCase();
    expect(text).not.toContain('good');
    expect(text).not.toContain('like');
    expect(fixture.nativeElement.querySelectorAll('button').length).toBe(1);
  });

  it('says so when it could not be recorded', () => {
    const fixture = mounted();
    fixture.componentInstance.station.set(3);
    fixture.componentInstance.transcript.set('x');
    fixture.detectChanges();

    fixture.nativeElement.querySelector('[data-thumbsdown]').click();
    ctrl.expectOne('/feedback').flush(null, { status: 500, statusText: 'Error' });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-said]').textContent).toContain('not record');
  });

  it('does nothing with no station', () => {
    const fixture = mounted();
    fixture.componentInstance.transcript.set('x');
    fixture.detectChanges();
    fixture.nativeElement.querySelector('[data-thumbsdown]').click();
    ctrl.expectNone('/feedback');
  });

  // ONE VERDICT PER BREAK, NOT ONE PER PAGE LOAD. sent() is what stops a double
  // press on the same break; leaving it set through the next break meant a
  // listener could rate exactly one thing per session, and the admin panel that
  // reads these would see one row where there should be a shift's worth.
  it('rating a break arms again when the next break airs', () => {
    const fixture = mounted();
    fixture.componentInstance.station.set(3);
    fixture.componentInstance.transcript.set('Three in the morning.');
    fixture.detectChanges();

    fixture.nativeElement.querySelector('[data-thumbsdown]').click();
    ctrl.expectOne('/feedback').flush(null);
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[data-thumbsdown]').disabled).toBe(true);

    fixture.componentInstance.transcript.set('And that is the news.');
    fixture.detectChanges();
    const button = fixture.nativeElement.querySelector('[data-thumbsdown]');
    expect(button.disabled).toBe(false);
    // And the acknowledgement of the PREVIOUS break is gone with it.
    expect(fixture.nativeElement.querySelector('[data-said]').textContent.trim()).toBe('');
  });
});
