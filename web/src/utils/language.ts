// Language codes are stored as canonical lowercase ISO 639 base subtags (en, ru,
// ceb); the ingest 'und' (ISO 639-2 "undetermined") sentinel marks a book whose
// language could not be parsed. Filter values stay the raw code — only the label
// changes here.
//
// Intl.DisplayNames renders codes as English names with no dependency. 'und' is
// special-cased to "Unknown" (Intl renders it as "root"), and .of() can throw a
// RangeError on malformed legacy values, so we fall back to the raw code.
const LANGUAGE_NAMES = new Intl.DisplayNames(['en'], {
  type: 'language',
  fallback: 'code',
});

export function languageLabel(code: string): string {
  if (code === 'und') return 'Unknown';
  try {
    return LANGUAGE_NAMES.of(code) ?? code;
  } catch {
    return code;
  }
}

// sortLanguageCodes orders facet language codes for display: the 'und'/"Unknown"
// bucket first, then the rest alphabetically by their English display name. The
// backend returns them ordered by raw code, which is meaningless once labeled.
export function sortLanguageCodes(codes: string[]): string[] {
  return [...codes].sort((a, b) => {
    if (a === 'und') return -1;
    if (b === 'und') return 1;
    return languageLabel(a).localeCompare(languageLabel(b));
  });
}
