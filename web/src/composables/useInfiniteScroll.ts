import type { Ref } from 'vue';
import { onMounted, onUnmounted, ref } from 'vue';

export function useInfiniteScroll(
  triggerEl: Ref<HTMLElement | null>,
  onLoadMore: () => Promise<void>,
) {
  const loading = ref(false);
  let observer: IntersectionObserver | null = null;

  onMounted(() => {
    observer = new IntersectionObserver(
      async (entries) => {
        if (entries[0].isIntersecting && !loading.value) {
          loading.value = true;
          try {
            await onLoadMore();
          } finally {
            // Always clear loading, even if onLoadMore rejects, so a single
            // failed fetch doesn't permanently pin the observer (dead scroll).
            loading.value = false;
          }
        }
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
