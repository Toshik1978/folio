import type { Ref } from 'vue';
import { onMounted, onUnmounted, ref } from 'vue';

export function useInfiniteScroll(
  triggerEl: Ref<HTMLElement | null>,
  onLoadMore: () => Promise<void>,
  // Optional predicate reporting whether more pages remain. When supplied, the
  // observer re-arms after each successful load so a short page that leaves the
  // sentinel still on-screen keeps loading instead of stalling. Without it the
  // observer would have to wait for a manual scroll to fire again.
  hasMore?: () => boolean,
) {
  const loading = ref(false);
  let observer: IntersectionObserver | null = null;

  async function load(): Promise<void> {
    if (loading.value) return;
    loading.value = true;
    try {
      await onLoadMore();
    } finally {
      // Always clear loading, even if onLoadMore rejects, so a single failed
      // fetch doesn't permanently pin the observer (dead scroll).
      loading.value = false;
    }

    // IntersectionObserver only fires on transitions. If the freshly loaded page
    // didn't push the sentinel out of view, it stays intersecting and no further
    // callback arrives. Re-arming (unobserve + observe) forces the observer to
    // re-report the sentinel's *current* state, which loads the next page when
    // it's still visible and quietly stops once the viewport is filled.
    //
    // hasMore() gates this: at end-of-list onLoadMore is a no-op, so re-arming an
    // intersecting sentinel would spin forever. Skipping it leaves the original
    // observation intact, so a later manual scroll still works.
    if (observer && triggerEl.value && hasMore?.()) {
      observer.unobserve(triggerEl.value);
      observer.observe(triggerEl.value);
    }
  }

  onMounted(() => {
    observer = new IntersectionObserver(
      (entries) => {
        // load() already resets loading and skips re-arm on failure, so a
        // rejected onLoadMore has nothing left to handle here — swallow it
        // rather than leak an unhandled rejection.
        if (entries[0].isIntersecting) void load().catch(() => {});
      },
      { rootMargin: '200px' },
    );

    if (triggerEl.value) {
      observer.observe(triggerEl.value);
    }
  });

  onUnmounted(() => {
    observer?.disconnect();
  });

  return { loading };
}
