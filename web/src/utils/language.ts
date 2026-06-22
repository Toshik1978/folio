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

// COMMON_LANGUAGE_CODES is the curated shortlist offered by the edit form's
// language picker — the languages most likely to appear in a personal library.
// It is not exhaustive: editLanguageOptions() always folds in the book's current
// code so an out-of-list value (e.g. a rare subtag from ingest) stays selectable
// and is never silently dropped on save.
const COMMON_LANGUAGE_CODES = [
  'en',
  'ru',
  'uk',
  'de',
  'fr',
  'es',
  'it',
  'pt',
  'nl',
  'pl',
  'cs',
  'sv',
  'da',
  'no',
  'fi',
  'tr',
  'el',
  'ja',
  'zh',
  'ko',
  'ar',
  'he',
  'hi',
  'la',
];

export interface LanguageOption {
  code: string;
  label: string;
}

// editLanguageOptions builds the {code,label} list for the edit form's language
// <select>, sorted by display name with 'und'/"Unknown" first. The book's current
// code is always included (even when outside the curated shortlist) so editing an
// uncommon language never forces it to change.
export function editLanguageOptions(current?: string | null): LanguageOption[] {
  const codes = new Set(COMMON_LANGUAGE_CODES);
  codes.add('und');
  if (current) codes.add(current);

  return sortLanguageCodes([...codes]).map((code) => ({ code, label: languageLabel(code) }));
}
