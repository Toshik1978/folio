import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('@/api', () => ({
  fetchSyncStatus: vi.fn(),
  fetchLibraries: vi.fn(),
}));

import { fetchLibraries, fetchSyncStatus } from '@/api';

import { useSyncStatus } from './useSyncStatus';

// Minimal controllable EventSource stand-in.
class MockEventSource {
  static instances: MockEventSource[] = [];
  url: string;
  readyState = 0; // CONNECTING
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  listeners: Record<string, ((e: MessageEvent) => void)[]> = {};
  closed = false;

  constructor(url: string) {
    this.url = url;
    MockEventSource.instances.push(this);
  }
  addEventListener(type: string, fn: (e: MessageEvent) => void) {
    (this.listeners[type] ??= []).push(fn);
  }
  emit(type: string, data: unknown) {
    this.readyState = 1; // OPEN
    for (const fn of this.listeners[type] ?? []) {
      fn({ data: JSON.stringify(data) } as MessageEvent);
    }
  }
  failClosed() {
    this.readyState = 2; // CLOSED
    this.onerror?.();
  }
  close() {
    this.closed = true;
    this.readyState = 2;
  }
}

describe('useSyncStatus', () => {
  beforeEach(() => {
    MockEventSource.instances = [];
    vi.stubGlobal('EventSource', MockEventSource as unknown as typeof EventSource);
    const { running, current, queued, currentProgress, disconnect } = useSyncStatus();
    disconnect();
    running.value = false;
    current.value = 0;
    queued.value = [];
    currentProgress.value = null;
    vi.mocked(fetchSyncStatus).mockReset();
    vi.mocked(fetchLibraries).mockReset();
    vi.mocked(fetchLibraries).mockResolvedValue([]);
  });

  it('applies a status event to running/current/queued', () => {
    const s = useSyncStatus();
    s.connect();
    const es = MockEventSource.instances[MockEventSource.instances.length - 1]!;

    es.emit('status', { running: true, current: 5, queued: [6, 7] });

    expect(s.running.value).toBe(true);
    expect(s.isSyncing(5)).toBe(true);
    expect(s.isQueued(6)).toBe(true);
    expect(s.isBusy(7)).toBe(true);
    expect(s.isBusy(99)).toBe(false);
    s.disconnect();
  });

  it('refreshes the library list on a library event', () => {
    const s = useSyncStatus();
    s.connect();
    const es = MockEventSource.instances[MockEventSource.instances.length - 1]!;

    es.emit('library', { id: 5, status: 'active' });

    expect(fetchLibraries).toHaveBeenCalled();
    s.disconnect();
  });

  it('falls back to polling when the stream opens but stays silent', async () => {
    vi.useFakeTimers();
    vi.mocked(fetchSyncStatus).mockResolvedValue({ running: false, current: 0, queued: [] });
    const s = useSyncStatus();
    s.connect();

    await vi.advanceTimersByTimeAsync(30000); // past WATCHDOG_MS with no frames
    expect(fetchSyncStatus).toHaveBeenCalled();

    s.disconnect();
    vi.useRealTimers();
  });

  it('refreshes the library list on every fallback poll, even when idle', async () => {
    vi.useFakeTimers();
    vi.mocked(fetchSyncStatus).mockResolvedValue({ running: false, current: 0, queued: [] });
    const s = useSyncStatus();
    s.connect();

    // Watchdog fires -> fallback poll runs while the engine is idle. A row change
    // (e.g. a completed purge) must still reach the UI via a list refresh.
    await vi.advanceTimersByTimeAsync(30000);
    expect(fetchLibraries).toHaveBeenCalled();

    s.disconnect();
    vi.useRealTimers();
  });

  it('falls back when the connection closes', async () => {
    vi.useFakeTimers();
    vi.mocked(fetchSyncStatus).mockResolvedValue({ running: false, current: 0, queued: [] });
    const s = useSyncStatus();
    s.connect();
    const es = MockEventSource.instances[MockEventSource.instances.length - 1]!;

    es.failClosed();
    await vi.advanceTimersByTimeAsync(0);
    expect(fetchSyncStatus).toHaveBeenCalled();

    s.disconnect();
    vi.useRealTimers();
  });

  it('tracks progress for the current library and clears it when idle', () => {
    const s = useSyncStatus();
    s.connect();
    const es = MockEventSource.instances[MockEventSource.instances.length - 1];

    es.emit('status', { running: true, current: 5, queued: [] });
    es.emit('progress', { library: 5, processed: 1200, total: 5000 });
    expect(s.currentProgress.value).toEqual({ processed: 1200, total: 5000 });

    // Run finishes -> progress clears.
    es.emit('status', { running: false, current: 0, queued: [] });
    expect(s.currentProgress.value).toBeNull();
    s.disconnect();
  });

  it('re-arms the watchdog after an event so a stall AFTER the first frame still triggers fallback', async () => {
    vi.useFakeTimers();
    vi.mocked(fetchSyncStatus).mockResolvedValue({ running: false, current: 0, queued: [] });
    const s = useSyncStatus();
    s.connect();
    const es = MockEventSource.instances[MockEventSource.instances.length - 1]!;

    // Deliver an event — this should re-arm (not permanently clear) the watchdog.
    es.emit('status', { running: true, current: 1, queued: [] });

    // Advance past WATCHDOG_MS with no further events.
    await vi.advanceTimersByTimeAsync(30000);

    // The stream has been silent since the event: fallback poll must have fired.
    expect(fetchSyncStatus).toHaveBeenCalled();

    s.disconnect();
    vi.useRealTimers();
  });

  it('keeps re-arming as long as events keep arriving so there is no premature fallback', async () => {
    vi.useFakeTimers();
    vi.mocked(fetchSyncStatus).mockResolvedValue({ running: false, current: 0, queued: [] });
    const s = useSyncStatus();
    s.connect();
    const es = MockEventSource.instances[MockEventSource.instances.length - 1]!;

    // Emit events every 20 s — each gap is under WATCHDOG_MS (30000) but the
    // total exceeds it, so only the per-event re-arm can be holding fallback off.
    await vi.advanceTimersByTimeAsync(20000);
    es.emit('status', { running: true, current: 1, queued: [] });
    await vi.advanceTimersByTimeAsync(20000);
    es.emit('status', { running: true, current: 1, queued: [] });
    await vi.advanceTimersByTimeAsync(20000);
    es.emit('status', { running: true, current: 1, queued: [] });
    // Total elapsed: 60000ms, but each event reset the 30000ms watchdog, so no fallback yet.
    expect(fetchSyncStatus).not.toHaveBeenCalled();

    s.disconnect();
    vi.useRealTimers();
  });

  it('treats heartbeat pings as liveness: no fallback or reconnect on an idle stream', async () => {
    vi.useFakeTimers();
    vi.mocked(fetchSyncStatus).mockResolvedValue({ running: false, current: 0, queued: [] });
    const s = useSyncStatus();
    s.connect();
    const es = MockEventSource.instances[MockEventSource.instances.length - 1]!;

    // A healthy but idle stream emits only the server's keepalive ping (~20s).
    // Each ping must re-arm the watchdog so the stream is never declared dead.
    await vi.advanceTimersByTimeAsync(20000);
    es.emit('ping', {});
    await vi.advanceTimersByTimeAsync(20000);
    es.emit('ping', {});
    await vi.advanceTimersByTimeAsync(5000); // 45s elapsed — well past the old 6s watchdog

    // No false-positive death: no polling fallback, and no teardown/reopen of the
    // stream (which is what produced a logged GET /api/sync/events every ~30s).
    expect(fetchSyncStatus).not.toHaveBeenCalled();
    expect(MockEventSource.instances).toHaveLength(1);

    s.disconnect();
    vi.useRealTimers();
  });

  it('clears progress when the current library advances and ignores other libraries', () => {
    const s = useSyncStatus();
    s.connect();
    const es = MockEventSource.instances[MockEventSource.instances.length - 1];

    es.emit('status', { running: true, current: 5, queued: [6] });
    es.emit('progress', { library: 5, processed: 500, total: 1000 });
    expect(s.currentProgress.value).toEqual({ processed: 500, total: 1000 });

    // Library 6 promoted while still running — progress for library 5 must clear.
    es.emit('status', { running: true, current: 6, queued: [] });
    expect(s.currentProgress.value).toBeNull();

    // A progress event for the no-longer-current library 5 is ignored.
    es.emit('progress', { library: 5, processed: 999, total: 1000 });
    expect(s.currentProgress.value).toBeNull();

    // An indeterminate progress event (no total) for the current library is accepted.
    es.emit('progress', { library: 6, processed: 200 });
    expect(s.currentProgress.value).toEqual({ processed: 200, total: undefined });

    s.disconnect();
  });
});
