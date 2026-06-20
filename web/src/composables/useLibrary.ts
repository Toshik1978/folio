import { ref } from 'vue';

import { fetchLibraries } from '@/api';
import type { Library } from '@/types';

const STORAGE_KEY = 'folio-library';

function getStored(): number | null {
  const raw = localStorage.getItem(STORAGE_KEY);
  if (raw === null) return null;
  const n = Number(raw);
  return Number.isInteger(n) && n > 0 ? n : null;
}

// Module-level singletons (mirrors useTheme): the selected library is ambient
// app state shared by every page and persisted to localStorage.
const libraryId = ref<number | null>(getStored());
const libraries = ref<Library[]>([]);

function setLibrary(id: number | null): void {
  libraryId.value = id;
  if (id === null) localStorage.removeItem(STORAGE_KEY);
  else localStorage.setItem(STORAGE_KEY, String(id));
}

async function refreshLibraries(): Promise<void> {
  libraries.value = await fetchLibraries();
  // Drop a stale selection whose library was deleted, so we don't keep filtering
  // by a library that no longer exists.
  if (libraryId.value !== null && !libraries.value.some((l) => l.id === libraryId.value)) {
    setLibrary(null);
  }
}

export function useLibrary() {
  return { libraryId, libraries, setLibrary, refreshLibraries };
}
