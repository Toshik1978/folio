import { afterEach, describe, expect, it, vi } from 'vitest';

import { fetchGenres, setCoverFromUrl, updateBookMetadata, uploadCover } from '@/api';
import { makeBook } from '@/test/factories';

const book = makeBook({ id: 1, title: 'Dune' });

function mockFetch(body: unknown): typeof fetch {
  return vi.fn(
    async () =>
      new Response(JSON.stringify(body), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
  ) as unknown as typeof fetch;
}

afterEach(() => vi.restoreAllMocks());

describe('edit api', () => {
  it('PUTs metadata to /books/:id', async () => {
    const f = mockFetch(book);
    vi.stubGlobal('fetch', f);
    const got = await updateBookMetadata(1, {
      title: 'Dune',
      authors: ['Frank Herbert'],
      genres: [],
    });
    expect(got.title).toBe('Dune');
    expect(f).toHaveBeenCalledWith('/api/books/1', expect.objectContaining({ method: 'PUT' }));
  });

  it('POSTs a cover url', async () => {
    const f = mockFetch(book);
    vi.stubGlobal('fetch', f);
    await setCoverFromUrl(1, 'https://x/c.jpg');
    expect(f).toHaveBeenCalledWith(
      '/api/books/1/cover',
      expect.objectContaining({ method: 'POST' }),
    );
  });

  it('PUTs raw cover bytes', async () => {
    const f = mockFetch(book);
    vi.stubGlobal('fetch', f);
    await uploadCover(1, new Blob([new Uint8Array([1, 2, 3])], { type: 'image/png' }));
    const [, init] = (f as unknown as { mock: { calls: [string, RequestInit][] } }).mock.calls[0];
    expect(init.method).toBe('PUT');
  });

  it('GETs the genre taxonomy', async () => {
    vi.stubGlobal('fetch', mockFetch(['Science Fiction']));
    const got = await fetchGenres();
    expect(got).toContain('Science Fiction');
  });
});
