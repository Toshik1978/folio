import type { Author, Book, Library } from '@/types';

// Test data builders — sensible defaults with per-test overrides, so each spec
// states only the fields it cares about.

export function makeAuthor(overrides: Partial<Author> = {}): Author {
  return { id: 1, name: 'Isaac Asimov', book_count: 0, ...overrides };
}

export function makeBook(overrides: Partial<Book> = {}): Book {
  return {
    id: 7,
    title: 'Foundation',
    authors: [makeAuthor()],
    series: null,
    series_index: null,
    tags: [],
    publisher: null,
    year: null,
    pages: null,
    rating: null,
    language: null,
    annotation: null,
    formats: [],
    identifiers: [],
    cover_url: '/api/books/7/cover',
    thumbnail_url: '/api/books/7/cover/thumbnail',
    ...overrides,
  };
}

export function makeLibrary(overrides: Partial<Library> = {}): Library {
  return {
    id: 1,
    name: 'My Library',
    type: 'calibre',
    path: '/library/metadata.db',
    sync_interval_seconds: 3600,
    status: 'active',
    purge_at: null,
    last_sync_at: null,
    last_sync_error: null,
    book_count: 0,
    ...overrides,
  };
}
