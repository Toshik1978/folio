import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import {
  createLibrary,
  deleteLibrary,
  fetchAuthorLetters,
  fetchAuthors,
  fetchBook,
  fetchBooks,
  fetchFacets,
  fetchLibraries,
  fetchPublisherLetters,
  fetchSeriesLetters,
  fetchSettings,
  fetchStats,
  fetchSyncStatus,
  fetchTagLetters,
  forcePurgeLibrary,
  reindexLibrary,
  syncLibrary,
  updateLibrary,
  updateSettings,
} from '@/api';
import type { BookFilters } from '@/types';

// A minimal fetch Response stand-in; only the bits api.ts reads.
function res(
  body: unknown,
  init: { ok?: boolean; status?: number; statusText?: string } = {},
): unknown {
  return {
    ok: init.ok ?? true,
    status: init.status ?? 200,
    statusText: init.statusText ?? 'OK',
    json: async () => body,
  };
}

const fetchMock = vi.fn();

// The arguments fetch was called with on the most recent call.
function lastCall(): [string, RequestInit?] {
  const calls = fetchMock.mock.calls;
  return calls[calls.length - 1] as [string, RequestInit?];
}
function calledUrl(): string {
  return lastCall()[0];
}
function calledInit(): RequestInit {
  return lastCall()[1] as RequestInit;
}

describe('api', () => {
  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal('fetch', fetchMock);
  });
  afterEach(() => vi.unstubAllGlobals());

  describe('fetchBooks query building', () => {
    it('hits /api/books with no query string when there are no filters', async () => {
      fetchMock.mockResolvedValue(res({ items: [], total: 0, page: 1, limit: 24 }));
      await fetchBooks();
      expect(calledUrl()).toBe('/api/books');
    });

    it('maps filters to query params and url-encodes values', async () => {
      fetchMock.mockResolvedValue(res({ items: [], total: 0, page: 1, limit: 24 }));
      await fetchBooks({ q: 'robots', author: 'Le Guin', page: 2, limit: 50 });
      expect(calledUrl()).toBe('/api/books?q=robots&author=Le+Guin&page=2&limit=50');
    });

    it('drops the dead scope field', async () => {
      fetchMock.mockResolvedValue(res({ items: [], total: 0, page: 1, limit: 24 }));
      // scope is no longer part of BookFilters; cast to prove buildQuery ignores it.
      await fetchBooks({ q: 'dune', scope: 'author' } as BookFilters);
      expect(calledUrl()).toBe('/api/books?q=dune');
    });

    it('passes facet params (incl. = exact prefix) through', async () => {
      fetchMock.mockResolvedValue(res({ items: [], total: 0, page: 1, limit: 24 }));
      await fetchBooks({ author: 'Pratchett', title: '=Dune', series: 'Discworld' });
      expect(calledUrl()).toBe('/api/books?title=%3DDune&author=Pratchett&series=Discworld');
    });

    it('omits empty/falsy filter values', async () => {
      fetchMock.mockResolvedValue(res({ items: [], total: 0, page: 1, limit: 24 }));
      await fetchBooks({ q: '', author: 'Asimov', page: 0 });
      expect(calledUrl()).toBe('/api/books?author=Asimov');
    });

    it('returns the parsed JSON body', async () => {
      const payload = { items: [{ id: 1 }], total: 1, page: 1, limit: 24 };
      fetchMock.mockResolvedValue(res(payload));
      await expect(fetchBooks()).resolves.toEqual(payload);
    });
  });

  describe('GET endpoints', () => {
    it.each([
      ['fetchBook', () => fetchBook(7), '/api/books/7'],
      ['fetchAuthorLetters', () => fetchAuthorLetters(), '/api/authors/letters'],
      ['fetchSeriesLetters', () => fetchSeriesLetters(), '/api/series/letters'],
      ['fetchTagLetters', () => fetchTagLetters(), '/api/tags/letters'],
      ['fetchPublisherLetters', () => fetchPublisherLetters(), '/api/publishers/letters'],
      ['fetchLibraries', () => fetchLibraries(), '/api/libraries'],
      ['fetchSettings', () => fetchSettings(), '/api/settings'],
    ])('%s requests %s', async (_name, call, url) => {
      fetchMock.mockResolvedValue(res([]));
      await call();
      expect(calledUrl()).toBe(url);
      // GET requests carry no body.
      expect(calledInit()?.body).toBeUndefined();
    });

    it('scopes the letters endpoint to a library via ?library=<id>', async () => {
      fetchMock.mockResolvedValue(res([]));
      await fetchAuthorLetters(5);
      expect(calledUrl()).toBe('/api/authors/letters?library=5');
    });

    it('passes the library filter through to /books', async () => {
      fetchMock.mockResolvedValue(res({ items: [], total: 0, page: 1, limit: 24 }));
      await fetchBooks({ library: 5 });
      expect(calledUrl()).toContain('library=5');
    });
  });

  describe('browse-by-letter query building', () => {
    it('requests a single letter bucket', async () => {
      fetchMock.mockResolvedValue(res([]));
      await fetchAuthors('A');
      expect(calledUrl()).toBe('/api/authors?letter=A');
    });

    it('escapes the # bucket', async () => {
      fetchMock.mockResolvedValue(res([]));
      await fetchAuthors('#');
      expect(calledUrl()).toBe('/api/authors?letter=%23');
    });

    it('includes library and non-default pagination params', async () => {
      fetchMock.mockResolvedValue(res([]));
      await fetchAuthors('A', 5, 2, 50);
      expect(calledUrl()).toBe('/api/authors?letter=A&library=5&page=2&limit=50');
    });
  });

  describe('mutating endpoints', () => {
    it('createLibrary POSTs JSON with a Content-Type header', async () => {
      const library = {
        name: 'My Library',
        type: 'folder' as const,
        path: '/books',
        sync_interval_seconds: 3600,
      };
      fetchMock.mockResolvedValue(res({ id: 1, ...library }));
      await createLibrary(library);

      expect(calledUrl()).toBe('/api/libraries');
      const init = calledInit();
      expect(init.method).toBe('POST');
      expect(init.body).toBe(JSON.stringify(library));
      expect((init.headers as Record<string, string>)['Content-Type']).toBe('application/json');
    });

    it('updateLibrary PUTs JSON with a Content-Type header', async () => {
      const library = {
        name: 'My Updated Library',
        type: 'folder' as const,
        path: '/new-books',
        sync_interval_seconds: 7200,
      };
      fetchMock.mockResolvedValue(res({ id: 5, ...library }));
      await updateLibrary(5, library);

      expect(calledUrl()).toBe('/api/libraries/5');
      const init = calledInit();
      expect(init.method).toBe('PUT');
      expect(init.body).toBe(JSON.stringify(library));
      expect((init.headers as Record<string, string>)['Content-Type']).toBe('application/json');
    });

    it('updateSettings PUTs the update body', async () => {
      fetchMock.mockResolvedValue(res({ opds_user: 'reader', opds_pass_set: true }));
      await updateSettings({ opds_user: 'reader', opds_pass: 'secret' });

      expect(calledUrl()).toBe('/api/settings');
      const init = calledInit();
      expect(init.method).toBe('PUT');
      expect(init.body).toBe(JSON.stringify({ opds_user: 'reader', opds_pass: 'secret' }));
    });

    it('syncLibrary POSTs to the per-library sync path', async () => {
      fetchMock.mockResolvedValue(res({ status: 'queued' }, { status: 202 }));
      await syncLibrary(5);
      expect(calledUrl()).toBe('/api/libraries/5/sync');
      expect(calledInit().method).toBe('POST');
    });

    it('reindexLibrary POSTs to the per-library reindex path', async () => {
      fetchMock.mockResolvedValue(res({ status: 'queued' }, { status: 202 }));
      await reindexLibrary(5);
      expect(calledUrl()).toBe('/api/libraries/5/reindex');
      expect(calledInit().method).toBe('POST');
    });

    it('fetchSyncStatus GETs the sync status', async () => {
      fetchMock.mockResolvedValue(res({ running: true, current: 5, queued: [6, 7] }));
      const status = await fetchSyncStatus();
      expect(calledUrl()).toBe('/api/sync/status');
      expect(status).toEqual({ running: true, current: 5, queued: [6, 7] });
    });

    it('deleteLibrary DELETEs and tolerates a 204 (no body)', async () => {
      fetchMock.mockResolvedValue(res(undefined, { status: 204 }));
      await expect(deleteLibrary(5)).resolves.toBeUndefined();
      expect(calledUrl()).toBe('/api/libraries/5');
      expect(calledInit().method).toBe('DELETE');
    });

    it('forcePurgeLibrary POSTs to the per-library purge path', async () => {
      fetchMock.mockResolvedValue(res({ status: 'purging' }));
      await forcePurgeLibrary(5);
      expect(calledUrl()).toBe('/api/libraries/5/purge');
      expect(calledInit().method).toBe('POST');
    });
  });

  describe('stats and value filters', () => {
    it('fetchStats requests /stats with no query for global totals', async () => {
      fetchMock.mockResolvedValueOnce(
        res({
          total_books: 1,
          total_size_bytes: 2,
          authors: 3,
          series: 4,
          libraries: 5,
          formats: {},
          languages: {},
        }),
      );
      await fetchStats();
      expect(lastCall()[0]).toBe('/api/stats');
    });

    it('fetchFacets requests /facets without library', async () => {
      fetchMock.mockResolvedValueOnce(res({ formats: [], languages: [] }));
      await fetchFacets();
      expect(lastCall()[0]).toBe('/api/facets');
    });

    it('fetchFacets scopes to a library when given', async () => {
      fetchMock.mockResolvedValueOnce(res({ formats: [], languages: [] }));
      await fetchFacets(3);
      expect(lastCall()[0]).toBe('/api/facets?library=3');
    });

    it('buildQuery includes format and lang filters', async () => {
      fetchMock.mockResolvedValueOnce(res({ items: [], total: 0, page: 1, limit: 24 }));
      await fetchBooks({ format: 'epub', lang: 'ru' });
      expect(lastCall()[0]).toBe('/api/books?format=epub&lang=ru');
    });
  });

  describe('error handling', () => {
    it('throws the backend error message from a JSON error body', async () => {
      fetchMock.mockResolvedValue(
        res({ error: 'book not found' }, { ok: false, status: 404, statusText: 'Not Found' }),
      );
      await expect(fetchBook(99)).rejects.toThrow('book not found');
    });

    it('falls back to the status line when the error body is not JSON', async () => {
      fetchMock.mockResolvedValue({
        ok: false,
        status: 500,
        statusText: 'Internal Server Error',
        json: async () => {
          throw new Error('not json');
        },
      });
      await expect(fetchBook(1)).rejects.toThrow('500 Internal Server Error');
    });
  });

  describe('request timeout (F7)', () => {
    it('attaches an abort signal to every request', async () => {
      fetchMock.mockResolvedValue(res({ items: [], total: 0, page: 1, limit: 24 }));
      await fetchBooks();
      expect(calledInit().signal).toBeInstanceOf(AbortSignal);
    });

    it('maps a fetch timeout to a readable error', async () => {
      fetchMock.mockRejectedValue(new DOMException('The operation timed out.', 'TimeoutError'));
      await expect(fetchBooks()).rejects.toThrow('request timed out');
    });

    it('rethrows non-timeout fetch failures untouched', async () => {
      fetchMock.mockRejectedValue(new TypeError('network down'));
      await expect(fetchBooks()).rejects.toThrow('network down');
    });
  });
});
