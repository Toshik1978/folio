import { describe, expect, it } from 'vitest';

import { languageLabel, sortLanguageCodes } from './language';

describe('languageLabel', () => {
  it('maps the und sentinel to Unknown', () => {
    expect(languageLabel('und')).toBe('Unknown');
  });

  it('renders ISO codes as English names', () => {
    expect(languageLabel('en')).toBe('English');
    expect(languageLabel('ru')).toBe('Russian');
    expect(languageLabel('fr')).toBe('French');
  });

  it('handles three-letter codes without a two-letter form', () => {
    expect(languageLabel('ceb')).toBe('Cebuano');
  });

  it('falls back to the raw code for unrecognized values', () => {
    expect(languageLabel('xyz')).toBe('xyz');
  });

  it('does not throw on malformed legacy values', () => {
    expect(languageLabel('zzzz')).toBe('zzzz');
  });
});

describe('sortLanguageCodes', () => {
  it('puts Unknown first, then alphabetical by English name', () => {
    expect(sortLanguageCodes(['ru', 'en', 'und', 'fr'])).toEqual(['und', 'en', 'fr', 'ru']);
  });

  it('does not mutate the input array', () => {
    const input = ['ru', 'en'];
    sortLanguageCodes(input);
    expect(input).toEqual(['ru', 'en']);
  });

  it('handles an empty list', () => {
    expect(sortLanguageCodes([])).toEqual([]);
  });
});
