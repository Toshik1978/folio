// The browse pages load one alphabet bucket at a time. ALPHABET is the fixed
// superset of buckets the selector renders, in display order: Cyrillic А-Я,
// then Latin A-Z, then '#' (the catch-all for digits, punctuation, lowercase
// and other scripts). It mirrors the backend's alphabet/bucketOf in
// internal/api/letters.go — keep the two in sync.
export const HASH_BUCKET = '#';

function buildAlphabet(): string[] {
  const out: string[] = [];
  for (let c = 0x0410; c <= 0x042f; c++) out.push(String.fromCharCode(c)); // А..Я
  for (let c = 0x41; c <= 0x5a; c++) out.push(String.fromCharCode(c)); // A..Z
  out.push(HASH_BUCKET);
  return out;
}

export const ALPHABET: string[] = buildAlphabet();
