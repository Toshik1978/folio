// The browse pages load one alphabet bucket at a time. ALPHABET is the fixed
// superset of buckets the selector renders, in display order: Cyrillic А-Я,
// then Latin A-Z, then '#' (the catch-all for digits, punctuation, lowercase
// and other scripts). It mirrors the backend's alphabet/bucketOf in
// internal/api/letters.go — keep the two in sync.
export const HASH_BUCKET = '#';

function range(from: number, to: number): string[] {
  const out: string[] = [];
  for (let c = from; c <= to; c++) out.push(String.fromCharCode(c));
  return out;
}

// The script blocks the selector groups by. The selector hides a whole block
// when none of its buckets have entries (see AlphabetSelector.vue), so a
// Latin-only library shows no Cyrillic buttons and vice versa.
export const CYRILLIC: string[] = range(0x0410, 0x042f); // А..Я
export const LATIN: string[] = range(0x41, 0x5a); // A..Z

export const ALPHABET: string[] = [...CYRILLIC, ...LATIN, HASH_BUCKET];
