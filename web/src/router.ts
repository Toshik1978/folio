import { createRouter, createWebHistory } from 'vue-router';

const router = createRouter({
  history: createWebHistory(),
  routes: [
    {
      path: '/',
      component: () => import('./pages/BooksPage.vue'),
    },
    {
      path: '/books/:id',
      component: () => import('./pages/BooksPage.vue'),
    },
    {
      path: '/authors',
      component: () => import('./pages/AuthorListPage.vue'),
    },
    {
      path: '/series',
      component: () => import('./pages/SeriesListPage.vue'),
    },
    {
      path: '/tags',
      component: () => import('./pages/TagListPage.vue'),
    },
    {
      path: '/publishers',
      component: () => import('./pages/PublisherListPage.vue'),
    },
    {
      path: '/settings',
      component: () => import('./pages/SettingsPage.vue'),
    },
    {
      // Unknown paths: the backend SPA fallback serves index.html, so without
      // this the router would match nothing and render a blank view. Send them home.
      path: '/:pathMatch(.*)*',
      redirect: '/',
    },
  ],
});

// After a redeploy, the previous build's hashed chunk files 404; a stale tab's
// next navigation then fails its lazy route import and renders nothing. Reload
// once to pick up the new manifest. The sessionStorage flag (cleared on the
// next successful navigation) prevents a reload loop when the failure has some
// other cause.
const RELOADED_KEY = 'folio-chunk-reloaded';

export function isChunkLoadError(err: unknown): boolean {
  return (
    err instanceof Error &&
    /dynamically imported module|importing a module script failed/i.test(err.message)
  );
}

router.onError((err) => {
  if (!isChunkLoadError(err)) return;
  if (sessionStorage.getItem(RELOADED_KEY) === '1') return;
  sessionStorage.setItem(RELOADED_KEY, '1');
  location.reload();
});

router.afterEach(() => {
  sessionStorage.removeItem(RELOADED_KEY);
});

export default router;
