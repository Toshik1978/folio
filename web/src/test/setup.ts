import { vi } from 'vitest';

// happy-dom has no IntersectionObserver; useInfiniteScroll constructs one on
// mount. Provide an inert stub so components that observe a scroll trigger can
// mount without firing load callbacks.
class IntersectionObserverStub implements IntersectionObserver {
  readonly root = null;
  readonly rootMargin = '';
  readonly scrollMargin = '';
  readonly thresholds = [];
  observe = vi.fn();
  unobserve = vi.fn();
  disconnect = vi.fn();
  takeRecords = vi.fn(() => []);
}

vi.stubGlobal('IntersectionObserver', IntersectionObserverStub);
