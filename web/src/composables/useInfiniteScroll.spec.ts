import { mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { Ref } from 'vue';
import { defineComponent, h, ref } from 'vue';

import { useInfiniteScroll } from './useInfiniteScroll';

// A controllable IntersectionObserver stub (the global setup stub is inert).
// It records observed elements and lets a test fire the intersection callback.
type IOEntry = { isIntersecting: boolean };
let observers: ControllableIO[] = [];

class ControllableIO {
  observed: Element[] = [];
  observe = vi.fn((el: Element) => this.observed.push(el));
  unobserve = vi.fn();
  disconnect = vi.fn();
  takeRecords = vi.fn(() => []);
  constructor(public cb: (entries: IOEntry[]) => void | Promise<void>) {
    observers.push(this);
  }
  fire(isIntersecting = true): void | Promise<void> {
    return this.cb([{ isIntersecting }]);
  }
}

function mountScroll(triggerEl: Ref<HTMLElement | null>, onLoadMore: () => Promise<void>) {
  let result!: ReturnType<typeof useInfiniteScroll>;
  const wrapper = mount(
    defineComponent({
      setup() {
        result = useInfiniteScroll(triggerEl, onLoadMore);
        return () => h('div');
      },
    }),
  );
  return { result, wrapper };
}

// deferred returns a promise plus its resolve handle, to suspend onLoadMore
// mid-flight and observe the loading flag.
function deferred() {
  let resolve!: () => void;
  const promise = new Promise<void>((r) => (resolve = r));
  return { promise, resolve };
}

describe('useInfiniteScroll', () => {
  beforeEach(() => {
    observers = [];
    vi.stubGlobal('IntersectionObserver', ControllableIO);
  });

  it('observes the trigger element on mount', () => {
    const el = document.createElement('div');
    mountScroll(ref(el), vi.fn().mockResolvedValue(undefined));
    expect(observers).toHaveLength(1);
    expect(observers[0].observed).toEqual([el]);
  });

  it('does not observe when the trigger element is null', () => {
    mountScroll(ref(null), vi.fn().mockResolvedValue(undefined));
    expect(observers[0].observe).not.toHaveBeenCalled();
  });

  it('fires onLoadMore on intersection and toggles loading around it', async () => {
    const d = deferred();
    const onLoadMore = vi.fn(() => d.promise);
    const { result } = mountScroll(ref(document.createElement('div')), onLoadMore);

    const pending = observers[0].fire(true); // runs cb up to the await
    expect(result.loading.value).toBe(true);
    expect(onLoadMore).toHaveBeenCalledOnce();

    d.resolve();
    await pending;
    expect(result.loading.value).toBe(false);
  });

  it('ignores re-intersection while a load is already in flight', async () => {
    const d = deferred();
    const onLoadMore = vi.fn(() => d.promise);
    mountScroll(ref(document.createElement('div')), onLoadMore);

    const pending = observers[0].fire(true);
    observers[0].fire(true); // guarded by loading.value
    expect(onLoadMore).toHaveBeenCalledOnce();

    d.resolve();
    await pending;
  });

  it('does nothing when the entry is not intersecting', async () => {
    const onLoadMore = vi.fn().mockResolvedValue(undefined);
    const { result } = mountScroll(ref(document.createElement('div')), onLoadMore);

    await observers[0].fire(false);
    expect(onLoadMore).not.toHaveBeenCalled();
    expect(result.loading.value).toBe(false);
  });

  it('clears loading even when onLoadMore rejects (no dead scroll)', async () => {
    const onLoadMore = vi.fn().mockRejectedValue(new Error('boom'));
    const { result } = mountScroll(ref(document.createElement('div')), onLoadMore);

    await Promise.resolve(observers[0].fire(true)).catch(() => {});
    expect(onLoadMore).toHaveBeenCalledOnce();
    expect(result.loading.value).toBe(false);
  });

  it('disconnects the observer on unmount', () => {
    const { wrapper } = mountScroll(ref(document.createElement('div')), vi.fn());
    wrapper.unmount();
    expect(observers[0].disconnect).toHaveBeenCalledOnce();
  });
});
