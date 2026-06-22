import { afterEach, describe, expect, it, vi } from 'vitest';

import { fetchCoverCandidates } from '@/api';

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

describe('fetchCoverCandidates', () => {
  it('GETs the cover-search endpoint and returns candidates', async () => {
    const f = mockFetch([{ source: 'amazon', thumb_url: 't', full_url: 'f', width: 1, height: 2 }]);
    vi.stubGlobal('fetch', f);

    const got = await fetchCoverCandidates(1, 'Dune');
    expect(got).toHaveLength(1);
    expect(got[0].full_url).toBe('f');
    const [url] = (f as unknown as { mock: { calls: [string][] } }).mock.calls[0];
    expect(url).toContain('/api/books/1/cover/search?q=Dune');
  });

  it('omits the q param when no query is given', async () => {
    const f = mockFetch([]);
    vi.stubGlobal('fetch', f);
    await fetchCoverCandidates(2);
    const [url] = (f as unknown as { mock: { calls: [string][] } }).mock.calls[0];
    expect(url).toContain('/api/books/2/cover/search');
    expect(url).not.toContain('?q=');
  });
});
