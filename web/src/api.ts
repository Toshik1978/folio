import type {
  Author,
  Book,
  BookFilters,
  BookMetadataUpdate,
  CoverCandidate,
  FacetsResponse,
  Library,
  MatchCandidate,
  NewLibrary,
  PaginatedResponse,
  Publisher,
  Series,
  Settings,
  SettingsUpdate,
  Stats,
  SyncStatus,
  Tag,
} from './types';

// All requests are relative to /api; the Vite dev server proxies this to the
// Go backend on :8080 (see vite.config.ts), and in production the SPA is served
// from the same origin as the API.
const BASE = '/api';

// REQUEST_TIMEOUT_MS aborts a hung fetch so spinners and modals fail into the
// normal toast path instead of spinning forever. Long-running work (sync,
// purge) is asynchronous server-side, so no endpoint legitimately exceeds this.
const REQUEST_TIMEOUT_MS = 30_000;

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  let res: Response;
  try {
    res = await fetch(`${BASE}${path}`, {
      signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
      ...init,
      headers: {
        ...(init?.body ? { 'Content-Type': 'application/json' } : {}),
        ...init?.headers,
      },
    });
  } catch (err) {
    if (err instanceof DOMException && err.name === 'TimeoutError') {
      throw new Error('request timed out', { cause: err });
    }
    throw err;
  }
  if (!res.ok) {
    let message = `${res.status} ${res.statusText}`;
    try {
      const body = (await res.json()) as { error?: string };
      if (body.error) message = body.error;
    } catch {
      // non-JSON error body; keep the status line
    }
    throw new Error(message);
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

// buildQuery maps frontend book filters onto the backend's /api/books query
// params. Title/author/series values may carry a leading "=" to request an exact
// match instead of token-level FTS; the backend parses that prefix.
function buildQuery(filters: BookFilters): string {
  const params = new URLSearchParams();
  if (filters.q) params.set('q', filters.q);
  if (filters.title) params.set('title', filters.title);
  if (filters.author) params.set('author', filters.author);
  if (filters.series) params.set('series', filters.series);
  if (filters.tag) params.set('tag', filters.tag);
  if (filters.publisher) params.set('publisher', filters.publisher);
  if (filters.format) params.set('format', filters.format);
  if (filters.lang) params.set('lang', filters.lang);
  if (filters.library) params.set('library', String(filters.library));
  if (filters.sort) params.set('sort', filters.sort);
  if (filters.page) params.set('page', String(filters.page));
  if (filters.limit) params.set('limit', String(filters.limit));
  const qs = params.toString();
  return qs ? `?${qs}` : '';
}

export function fetchBooks(filters: BookFilters = {}): Promise<PaginatedResponse<Book>> {
  return request<PaginatedResponse<Book>>(`/books${buildQuery(filters)}`);
}

export function fetchBook(id: number): Promise<Book> {
  return request<Book>(`/books/${id}`);
}

// searchMatch queries the online metadata providers for Fix Match candidates for a book.
export function searchMatch(id: number, q: string): Promise<MatchCandidate[]> {
  return request<MatchCandidate[]>(`/books/${id}/match?q=${encodeURIComponent(q)}`);
}

// applyMatch overwrites a book's metadata from a chosen candidate, returning the
// updated book. source routes the candidate to its provider.
export function applyMatch(id: number, source: string, volumeId: string): Promise<Book> {
  return request<Book>(`/books/${id}/match`, {
    method: 'POST',
    body: JSON.stringify({ source, volume_id: volumeId }),
  });
}

// updateBookMetadata saves a manual metadata edit and returns the updated book.
export function updateBookMetadata(id: number, payload: BookMetadataUpdate): Promise<Book> {
  return request<Book>(`/books/${id}`, { method: 'PUT', body: JSON.stringify(payload) });
}

// setCoverFromUrl tells the server to fetch an image URL and use it as the cover.
export function setCoverFromUrl(id: number, url: string): Promise<Book> {
  return request<Book>(`/books/${id}/cover`, { method: 'POST', body: JSON.stringify({ url }) });
}

// uploadCover sends raw image bytes (file, paste, or drag-drop) as the cover.
// The Content-Type header overrides request()'s JSON default; the server sniffs
// the image bytes regardless, but a correct type keeps the request honest.
export function uploadCover(id: number, file: Blob): Promise<Book> {
  return request<Book>(`/books/${id}/cover`, {
    method: 'PUT',
    body: file,
    headers: { 'Content-Type': file.type || 'application/octet-stream' },
  });
}

// fetchGenres returns the canonical genre taxonomy for the edit autocomplete.
export function fetchGenres(): Promise<string[]> {
  return request<string[]>('/genres');
}

// fetchCoverCandidates asks the server to aggregate cover options across
// providers. q overrides the server's title seed when present.
export function fetchCoverCandidates(id: number, q?: string): Promise<CoverCandidate[]> {
  const query = q && q.trim() ? `?q=${encodeURIComponent(q.trim())}` : '';

  return request<CoverCandidate[]>(`/books/${id}/cover/search${query}`);
}

// fetchStats returns catalog totals.
export function fetchStats(): Promise<Stats> {
  return request<Stats>('/stats');
}

// fetchFacets returns format and language facets for the search menu.
export function fetchFacets(library?: number): Promise<FacetsResponse> {
  return request<FacetsResponse>(`/facets${libQuery(library)}`);
}

// libQuery appends ?library=<id> to an endpoint so it scopes to the selected
// library (omitted for "All").
function libQuery(library?: number): string {
  return library ? `?library=${library}` : '';
}

// browseQuery builds the query string for a paginated browse-by-letter request.
// URLSearchParams escapes the letter (notably '#' -> %23 and Cyrillic letters).
function browseQuery(letter: string, library?: number, page = 1, limit = 100): string {
  const params = new URLSearchParams();
  params.set('letter', letter);
  if (library) params.set('library', String(library));
  if (page > 1) params.set('page', String(page));
  if (limit !== 100) params.set('limit', String(limit));
  return `?${params.toString()}`;
}

// The browse endpoints come in pairs: fetch<Entity>Letters returns the alphabet
// buckets that have data (driving the selector), and fetch<Entity> returns one
// bucket's entries, paginated. See internal/api/lists.go.

export function fetchAuthorLetters(library?: number): Promise<string[]> {
  return request<string[]>(`/authors/letters${libQuery(library)}`);
}

export function fetchAuthors(
  letter: string,
  library?: number,
  page = 1,
  limit = 100,
): Promise<Author[]> {
  return request<Author[]>(`/authors${browseQuery(letter, library, page, limit)}`);
}

export function fetchSeriesLetters(library?: number): Promise<string[]> {
  return request<string[]>(`/series/letters${libQuery(library)}`);
}

export function fetchSeries(
  letter: string,
  library?: number,
  page = 1,
  limit = 100,
): Promise<Series[]> {
  return request<Series[]>(`/series${browseQuery(letter, library, page, limit)}`);
}

export function fetchTagLetters(library?: number): Promise<string[]> {
  return request<string[]>(`/tags/letters${libQuery(library)}`);
}

export function fetchTags(letter: string, library?: number, page = 1, limit = 100): Promise<Tag[]> {
  return request<Tag[]>(`/tags${browseQuery(letter, library, page, limit)}`);
}

export function fetchPublisherLetters(library?: number): Promise<string[]> {
  return request<string[]>(`/publishers/letters${libQuery(library)}`);
}

export function fetchPublishers(
  letter: string,
  library?: number,
  page = 1,
  limit = 100,
): Promise<Publisher[]> {
  return request<Publisher[]>(`/publishers${browseQuery(letter, library, page, limit)}`);
}

export function fetchLibraries(): Promise<Library[]> {
  return request<Library[]>('/libraries');
}

export function createLibrary(library: NewLibrary): Promise<Library> {
  return request<Library>('/libraries', { method: 'POST', body: JSON.stringify(library) });
}

export function updateLibrary(id: number, library: NewLibrary): Promise<Library> {
  return request<Library>(`/libraries/${id}`, { method: 'PUT', body: JSON.stringify(library) });
}

export function deleteLibrary(id: number): Promise<unknown> {
  return request(`/libraries/${id}`, { method: 'DELETE' });
}

export function syncLibrary(id: number): Promise<unknown> {
  return request(`/libraries/${id}/sync`, { method: 'POST' });
}

export function reindexLibrary(id: number): Promise<unknown> {
  return request(`/libraries/${id}/reindex`, { method: 'POST' });
}

export function reactivateLibrary(id: number): Promise<unknown> {
  return request(`/libraries/${id}/reactivate`, { method: 'POST' });
}

export function forcePurgeLibrary(id: number): Promise<unknown> {
  return request(`/libraries/${id}/purge`, { method: 'POST' });
}

// triggerReindexAll asks the backend to re-read every library from scratch,
// bypassing checkpoint gating (manual triggers always force).
export function triggerReindexAll(): Promise<unknown> {
  return request('/sync', { method: 'POST' });
}

export function fetchSyncStatus(): Promise<SyncStatus> {
  return request<SyncStatus>('/sync/status');
}

export function fetchSettings(): Promise<Settings> {
  return request<Settings>('/settings');
}

export function updateSettings(update: SettingsUpdate): Promise<Settings> {
  return request<Settings>('/settings', { method: 'PUT', body: JSON.stringify(update) });
}
