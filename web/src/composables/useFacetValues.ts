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
//
// loadSeq is a monotonic counter used to detect out-of-order resolutions: if a
// newer load() call supersedes an in-flight one, the earlier response is dropped
// so it cannot overwrite fresher data or corrupt the loadedFor cache key.
export function useFacetValues() {
  const { libraryId } = useLibrary();
  const toast = useToast();

  const formats = ref<string[]>([]);
  const languages = ref<string[]>([]);
  let loadedFor: number | undefined | typeof UNLOADED = UNLOADED;
  let loadSeq = 0;

  async function load(): Promise<void> {
    const lib = libraryId.value ?? undefined;
    if (loadedFor === lib) return;
    const seq = ++loadSeq;
    try {
      const facets = await fetchFacets(lib);
      if (seq !== loadSeq) return;
      formats.value = facets.formats;
      languages.value = facets.languages;
      loadedFor = lib;
    } catch (err) {
      if (seq !== loadSeq) return;
      toast.error(`Failed to load filters: ${(err as Error).message}`);
    }
  }

  return { formats, languages, load };
}
