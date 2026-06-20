import { describe, expect, it } from 'vitest';

import router, { isChunkLoadError } from '@/router';

describe('router', () => {
  it('redirects unknown paths home instead of rendering a blank view (F3)', async () => {
    await router.push('/no/such/page');
    await router.isReady();
    expect(router.currentRoute.value.path).toBe('/');
  });

  it('keeps known routes intact', async () => {
    await router.push('/settings');
    await router.isReady();
    expect(router.currentRoute.value.path).toBe('/settings');
  });

  it('recognizes chunk-load failures and nothing else (F5)', () => {
    expect(
      isChunkLoadError(new Error('Failed to fetch dynamically imported module: /assets/x.js')),
    ).toBe(true);
    expect(isChunkLoadError(new Error('Importing a module script failed.'))).toBe(true);
    expect(isChunkLoadError(new Error('something else'))).toBe(false);
    expect(isChunkLoadError('not an error')).toBe(false);
  });
});
