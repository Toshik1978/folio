import { ref } from 'vue';

export interface ThemeOption {
  id: string;
  label: string;
}

// Curated subset surfaced in the picker. All ~35 DaisyUI themes are still
// valid (style.css enables `themes: all`); edit this list to change the picker.
export const THEMES: ThemeOption[] = [
  { id: 'light', label: 'Light' },
  { id: 'fantasy', label: 'Fantasy' },
  { id: 'cmyk', label: 'CMYK' },
  { id: 'dark', label: 'Dark' },
  { id: 'abyss', label: 'Abyss' },
  { id: 'dim', label: 'Dim' },
];

const STORAGE_KEY = 'folio-theme';
const DEFAULT_DARK = 'dark';
const DEFAULT_LIGHT = 'light';

function prefersLight(): boolean {
  return (
    typeof window !== 'undefined' &&
    typeof window.matchMedia === 'function' &&
    window.matchMedia('(prefers-color-scheme: light)').matches
  );
}

function getStoredTheme(): string {
  try {
    const stored = localStorage.getItem(STORAGE_KEY);
    if (stored && THEMES.some((t) => t.id === stored)) return stored;
  } catch {
    /* storage unavailable */
  }
  return prefersLight() ? DEFAULT_LIGHT : DEFAULT_DARK;
}

const theme = ref<string>(getStoredTheme());

function applyTheme(t: string): void {
  document.documentElement.setAttribute('data-theme', t);
  try {
    localStorage.setItem(STORAGE_KEY, t);
  } catch {
    /* storage unavailable */
  }
}

applyTheme(theme.value);

export function useTheme() {
  function setTheme(id: string): void {
    theme.value = id;
    applyTheme(id);
  }

  return { theme, themes: THEMES, setTheme };
}
