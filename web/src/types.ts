export interface Book {
  id: number;
  title: string;
  authors: BookAuthor[];
  series: string | null;
  series_index: number | null;
  tags: string[];
  publisher: string | null;
  year: number | null;
  pages: number | null;
  rating: number | null;
  language: string | null;
  annotation: string | null;
  formats: BookFormat[];
  identifiers: BookIdentifier[];
  cover_url: string | null;
}

export interface BookFormat {
  type: string;
  size_bytes: number;
  download_url: string;
}

export interface BookIdentifier {
  type: string;
  value: string;
  url: string | null;
}

// MatchCandidate is one Google Books result shown in the Fix Match modal.
export interface MatchCandidate {
  volume_id: string;
  title: string;
  authors: string[] | null;
  year: number;
  thumbnail: string;
}

export interface Author {
  id: number;
  name: string;
  book_count: number;
}

// BookAuthor is the author shape embedded in a Book (detail/list). It omits
// book_count, which is meaningless there (see authorView vs bookAuthorView in
// the Go api package); the browse Author type keeps the count.
export interface BookAuthor {
  id: number;
  name: string;
}

export interface Series {
  id: number;
  name: string;
  book_count: number;
}

export interface Tag {
  name: string;
  book_count: number;
}

export interface Publisher {
  name: string;
  book_count: number;
}

export interface Library {
  id: number;
  name: string;
  type: 'calibre' | 'inpx' | 'folder';
  path: string;
  sync_interval_seconds: number;
  status: 'active' | 'syncing' | 'pending_purge' | 'error' | 'queued';
  purge_at: number | null;
  last_sync_at: number | null;
  last_sync_error: string | null;
  book_count: number;
}

// SyncStatus is the engine's live snapshot from GET /api/sync/status.
export interface SyncStatus {
  running: boolean;
  current: number; // library id being synced, 0 when idle
  queued: number[]; // library ids waiting their turn
}

// LibraryEvent is the payload of an SSE "library" event: one library row settled
// (sync success or error) or was reclaimed (purged). The client refetches the
// libraries list on receipt.
export interface LibraryEvent {
  id: number;
  status: 'active' | 'error' | 'purged';
}

// SyncProgress is the payload of an SSE "progress" event. total is absent when the
// parser cannot cheaply know it (the UI then shows an indeterminate count-up bar).
export interface SyncProgress {
  library: number;
  processed: number;
  total?: number;
}

export interface Settings {
  opds_user: string;
  opds_pass_set: boolean;
}

export interface SettingsUpdate {
  opds_user?: string;
  opds_pass?: string;
}

export interface NewLibrary {
  name: string;
  type: Library['type'];
  path: string;
  sync_interval_seconds: number;
}

export interface PaginatedResponse<T> {
  items: T[];
  total: number;
  page: number;
  limit: number;
}

export interface BookFilters {
  q?: string;
  title?: string;
  author?: string;
  series?: string;
  tag?: string;
  publisher?: string;
  format?: string;
  lang?: string;
  library?: number;
  sort?: string;
  page?: number;
  limit?: number;
}

export interface Stats {
  total_books: number;
  total_size_bytes: number;
  authors: number;
  series: number;
  libraries: number;
  formats: Record<string, number>;
  languages: Record<string, number>;
}

export interface FacetsResponse {
  formats: string[];
  languages: string[];
}
