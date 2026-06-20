import { describe, expect, it } from 'vitest';

import { ALPHABET } from '@/alphabet';
import { makeBook } from '@/test/factories';
import type { Book } from '@/types';

import alphabetFixture from './fixtures/alphabet.json';
import bookFixture from './fixtures/book-contract.json';

// These assert against the same fixtures internal/api/contract_test.go checks
// on the Go side; drift on either side fails exactly one of the two suites.
describe('backend/frontend contract', () => {
  it('Book carries exactly the fields the backend serializes', () => {
    // Compile-time: the fixture must satisfy the Book type (vue-tsc checks
    // this during `npm run build`).
    const book: Book = bookFixture;
    // Runtime: no missing or extra keys relative to the factory's full Book.
    expect(Object.keys(book).sort()).toEqual(Object.keys(makeBook()).sort());
  });

  it('ALPHABET mirrors the backend bucket order', () => {
    expect(ALPHABET).toEqual(alphabetFixture);
  });
});
