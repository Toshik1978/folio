import { Window } from 'happy-dom';
import { vi } from 'vitest';

// Node 24+ defines native experimental Web Storage globals. On Node 26 the
// `localStorage`/`sessionStorage` getters exist on globalThis but return
// `undefined` unless `--localstorage-file` is passed, and they shadow the
// implementations happy-dom installs — so `window`/`globalThis` storage is
// undefined and any code that touches localStorage throws. Rebind the globals
// to working Storage objects from a fresh happy-dom Window.
const storageSource = new Window();
for (const key of ['localStorage', 'sessionStorage'] as const) {
  Object.defineProperty(globalThis, key, {
    value: storageSource[key],
    configurable: true,
    writable: true,
  });
}

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
