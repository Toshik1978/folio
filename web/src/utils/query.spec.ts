import { describe, expect, it } from 'vitest';

import { asString } from './query';

describe('asString', () => {
  it('returns a scalar value as-is', () => {
    expect(asString('a')).toBe('a');
  });

  it('takes the first element of a repeated param', () => {
    expect(asString(['a', 'b'])).toBe('a');
  });

  it('returns empty string for null, undefined, and empty array', () => {
    expect(asString(null)).toBe('');
    expect(asString(undefined)).toBe('');
    expect(asString([])).toBe('');
  });
});
