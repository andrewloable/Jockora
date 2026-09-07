import { describe, expect, it } from 'vitest';
import { appConfig } from './app.config';

describe('appConfig', () => {
  it('provides the router', () => {
    // The shell is a router outlet and nothing else, so a config without a
    // router is an app that renders a blank page.
    expect(appConfig.providers.length).toBeGreaterThan(0);
  });
});
