import type { LocationQueryValue } from 'vue-router';

// asString collapses a route query value to one string. vue-router yields an
// array when a param repeats (?author=a&author=b), so a blind `as string` cast
// lies about the type; first value wins, null/absent becomes ''.
export function asString(v: LocationQueryValue | LocationQueryValue[] | undefined): string {
  const first = Array.isArray(v) ? v[0] : v;
  return first ?? '';
}
