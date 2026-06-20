import { ref } from 'vue';

import { fetchFacets } from '@/api';

import { useLibrary } from './useLibrary';
import { useToast } from './useToast';

const UNLOADED = Symbol('unloaded');

// useFacetValues lazily loads the per-library format/language value lists that
// feed the search bar's Format/Language dropdowns. Values come from /api/facets
// scoped to the selected library. load() is a no-op when loadedFor matches the
// current library (cache hit); a mismatch — triggered on focus or library switch
// — causes a re-fetch. A failed load resets loadedFor so the next call retries.
export function useFacetValues() {
  const { libraryId } = useLibrary();
  const toast = useToast();

  const formats = ref<string[]>([]);
  const languages = ref<string[]>([]);
  let loadedFor: number | undefined | typeof UNLOADED = UNLOADED;

  async function load(): Promise<void> {
    const lib = libraryId.value ?? undefined;
    if (loadedFor === lib) return;
    try {
      const facets = await fetchFacets(lib);
      formats.value = facets.formats;
      languages.value = facets.languages;
      loadedFor = lib;
    } catch (err) {
      toast.error(`Failed to load filters: ${(err as Error).message}`);
    }
  }

  return { formats, languages, load };
}
