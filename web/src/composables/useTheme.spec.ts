import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// useTheme is a module-level singleton: it resolves the initial theme and applies
// it at import time. Tests that probe the initial resolution re-import the module
// fresh (vi.resetModules) after seeding localStorage / matchMedia.

function stubMatchMedia(prefersLight: boolean) {
  vi.stubGlobal('matchMedia', vi.fn().mockReturnValue({ matches: prefersLight }));
}

describe('useTheme', () => {
  beforeEach(() => {
    localStorage.clear();
    document.documentElement.removeAttribute('data-theme');
    stubMatchMedia(false);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.resetModules();
  });

  it('setTheme applies the data-theme attribute, persists it, and exposes the theme list', async () => {
    const { useTheme, THEMES } = await import('./useTheme');
    const { theme, themes, setTheme } = useTheme();
    expect(themes).toBe(THEMES);

    setTheme('fantasy');
    expect(theme.value).toBe('fantasy');
    expect(document.documentElement.getAttribute('data-theme')).toBe('fantasy');
    expect(localStorage.getItem('folio-theme')).toBe('fantasy');
  });

  async function loadFresh(opts: { stored?: string; prefersLight?: boolean }) {
    vi.resetModules();
    localStorage.clear();
    if (opts.stored !== undefined) localStorage.setItem('folio-theme', opts.stored);
    stubMatchMedia(opts.prefersLight ?? false);
    return import('./useTheme');
  }

  it('uses a valid stored theme and applies it on load', async () => {
    const { useTheme } = await loadFresh({ stored: 'abyss' });
    expect(useTheme().theme.value).toBe('abyss');
    expect(document.documentElement.getAttribute('data-theme')).toBe('abyss');
  });

  it('ignores an unknown stored theme and follows the OS preference (light)', async () => {
    const { useTheme } = await loadFresh({ stored: 'not-a-real-theme', prefersLight: true });
    expect(useTheme().theme.value).toBe('light');
  });

  it('falls back to dark when nothing is stored and the OS does not prefer light', async () => {
    const { useTheme } = await loadFresh({ prefersLight: false });
    expect(useTheme().theme.value).toBe('dark');
  });
});
