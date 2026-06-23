import { computed, ref } from 'vue';

import { fetchSyncStatus } from '@/api';
import { useLibrary } from '@/composables/useLibrary';
import type { LibraryEvent, SyncProgress, SyncStatus } from '@/types';

// Live sync state is pushed over SSE (GET /api/sync/events). EventSource auto-
// reconnects on transient drops; we additionally fall back to polling when the
// stream opens but never delivers (a buffering proxy) — the one failure mode
// EventSource cannot detect on its own.
const EVENTS_URL = '/api/sync/events';
// Silence grace before declaring SSE dead. An idle-but-healthy stream emits only
// the server's keepalive ping (every 20s; see sseHeartbeat in events.go), so this
// MUST exceed that interval — otherwise the watchdog fires between pings and the
// stream is torn down and reopened on a loop even when nothing is wrong.
const WATCHDOG_MS = 30000;
const FALLBACK_POLL_MS = 3000; // legacy cadence while polling
const SSE_RETRY_MS = 30000; // re-attempt SSE from fallback

// Module-level singletons: one connection for the app.
const running = ref(false);
const current = ref(0);
const queued = ref<number[]>([]);
const currentProgress = ref<{ processed: number; total?: number } | null>(null);
const currentName = computed<string>(() => {
  const lib = useLibrary().libraries.value.find((l) => l.id === current.value);
  return lib?.name ?? '';
});

let source: EventSource | null = null;
let watchdog: ReturnType<typeof setTimeout> | null = null;
let fallbackTimer: ReturnType<typeof setInterval> | null = null;
let retryTimer: ReturnType<typeof setTimeout> | null = null;

// parseEvent safely parses an SSE frame's JSON payload; a malformed frame (a
// server bug, not a transport failure) is ignored rather than thrown out of the
// listener — consistent with the swallow-and-continue discipline elsewhere here.
function parseEvent<T>(e: MessageEvent): T | null {
  try {
    return JSON.parse(e.data) as T;
  } catch {
    return null;
  }
}

function applyStatus(status: SyncStatus): void {
  const prevCurrent = current.value;
  running.value = status.running;
  current.value = status.current ?? 0;
  queued.value = status.queued ?? [];
  if (!running.value || current.value !== prevCurrent) {
    currentProgress.value = null;
  }
}

function clearWatchdog(): void {
  if (watchdog) {
    clearTimeout(watchdog);
    watchdog = null;
  }
}

function armWatchdog(): void {
  clearWatchdog();
  watchdog = setTimeout(startFallback, WATCHDOG_MS);
}

// onEvent marks the stream live: stop any active fallback and re-arm the silence
// watchdog so a stream that stalls AFTER delivering frames is still detected.
function onEvent(): void {
  stopFallback();
  armWatchdog();
}

// --- Fallback polling (engaged only when SSE is non-functional) ---

async function fallbackPoll(): Promise<void> {
  let status: SyncStatus;
  try {
    status = await fetchSyncStatus();
  } catch {
    return; // transient failure: retry on the next tick
  }
  applyStatus(status);
  // No library events flow while polling, so refresh the list every tick — that
  // is the only way a row change reaches the UI in fallback mode. Gating this on
  // "running" would miss out-of-band settles like a completed purge or delete,
  // leaving a reclaimed library stuck on "Pending Purge" until a manual reload.
  await useLibrary()
    .refreshLibraries()
    .catch(() => undefined);
}

function startFallback(): void {
  if (fallbackTimer) return;
  void fallbackPoll();
  fallbackTimer = setInterval(() => void fallbackPoll(), FALLBACK_POLL_MS);
  // Keep trying to restore the live stream.
  if (!retryTimer) {
    retryTimer = setTimeout(() => {
      retryTimer = null;
      openStream();
    }, SSE_RETRY_MS);
  }
}

function stopFallback(): void {
  if (fallbackTimer) {
    clearInterval(fallbackTimer);
    fallbackTimer = null;
  }
  if (retryTimer) {
    clearTimeout(retryTimer);
    retryTimer = null;
  }
}

// --- SSE stream ---

function openStream(): void {
  closeStream();
  const es = new EventSource(EVENTS_URL);
  source = es;
  armWatchdog();

  es.addEventListener('status', (e) => {
    onEvent();
    const status = parseEvent<SyncStatus>(e as MessageEvent);
    if (status) applyStatus(status);
  });
  es.addEventListener('library', (e) => {
    onEvent();
    // The payload isn't needed (we refetch the list), but validate the frame.
    const evt = parseEvent<LibraryEvent>(e as MessageEvent);
    if (evt)
      void useLibrary()
        .refreshLibraries()
        .catch(() => undefined);
  });
  es.addEventListener('progress', (e) => {
    onEvent();
    const p = parseEvent<SyncProgress>(e as MessageEvent);
    if (p && p.library === current.value) {
      currentProgress.value = { processed: p.processed, total: p.total };
    }
  });
  // Heartbeat pings are liveness, not data: the server sends them between sync
  // events so an otherwise-silent stream still proves it is alive. Route them
  // through onEvent() to re-arm the watchdog (and cancel any fallback) without
  // touching any state.
  es.addEventListener('ping', () => onEvent());
  es.onerror = () => {
    // EventSource retries on its own unless it gave up (CLOSED); only then fall back.
    // Compare against 2 directly (not EventSource.CLOSED) so this works in test
    // environments where the mocked EventSource has no static CLOSED constant.
    if (es.readyState === 2 /* CLOSED */) {
      startFallback();
    }
  };
}

function closeStream(): void {
  if (source) {
    source.close();
    source = null;
  }
  clearWatchdog();
}

function connect(): void {
  if (source) return;
  openStream();
}

function disconnect(): void {
  closeStream();
  stopFallback();
}

export function useSyncStatus() {
  const isSyncing = (id: number): boolean => running.value && current.value === id;
  const isQueued = (id: number): boolean => queued.value.includes(id);
  const isBusy = (id: number): boolean => isSyncing(id) || isQueued(id);

  return {
    running,
    current,
    queued,
    currentProgress,
    currentName,
    isSyncing,
    isQueued,
    isBusy,
    refresh: fallbackPoll,
    connect,
    disconnect,
  };
}
