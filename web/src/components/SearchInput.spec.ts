import { flushPromises, mount } from '@vue/test-utils';
import { describe, expect, it, vi } from 'vitest';
import { createMemoryHistory, createRouter, type Router } from 'vue-router';

import SearchInput from '@/components/SearchInput.vue';

vi.mock('@/composables/useFacetValues', () => ({
  useFacetValues: () => ({
    formats: { value: ['epub', 'fb2'] },
    languages: { value: ['en'] },
    load: vi.fn(),
  }),
}));

function makeRouter(): Router {
  return createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div />' } },
      { path: '/authors', component: { template: '<div />' } },
    ],
  });
}

async function mountAt(router: Router, location: string) {
  await router.push(location);
  await router.isReady();
  return mount(SearchInput, {
    global: { plugins: [router] },
    attachTo: document.body,
  });
}

describe('SearchInput', () => {
  it('shows the facet menu on focus', async () => {
    const router = makeRouter();
    const wrapper = await mountAt(router, '/');
    expect(wrapper.find('[data-testid="facet-menu"]').exists()).toBe(false);

    await wrapper.find('input').trigger('focus');
    const items = wrapper.findAll('[data-testid="facet-option"]');
    expect(items.map((i) => i.text())).toEqual(['Author', 'Book Title', 'Book Series']);
  });

  it('commits a partial facet chip on Enter and resets the input', async () => {
    const router = makeRouter();
    const wrapper = await mountAt(router, '/');
    await wrapper.find('input').trigger('focus');
    await wrapper.findAll('[data-testid="facet-option"]')[0].trigger('mousedown'); // Author
    await wrapper.find('input').setValue('Pratchett');
    await wrapper.find('input').trigger('keydown.enter');
    await flushPromises();

    expect(router.currentRoute.value.query).toEqual({ author: 'Pratchett' });
    expect((wrapper.find('input').element as HTMLInputElement).value).toBe('');
  });

  it('commits an exact chip when the value starts with =', async () => {
    const router = makeRouter();
    const wrapper = await mountAt(router, '/');
    await wrapper.find('input').trigger('focus');
    await wrapper.findAll('[data-testid="facet-option"]')[0].trigger('mousedown'); // Author
    await wrapper.find('input').setValue('=Terry Pratchett');
    await wrapper.find('input').trigger('keydown.enter');
    await flushPromises();

    expect(router.currentRoute.value.query).toEqual({ author: '=Terry Pratchett' });
  });

  it('commits free text as a q chip when no facet is chosen', async () => {
    const router = makeRouter();
    const wrapper = await mountAt(router, '/');
    await wrapper.find('input').setValue('robots');
    await wrapper.find('input').trigger('keydown.enter');
    await flushPromises();

    expect(router.currentRoute.value.query).toEqual({ q: 'robots' });
  });

  it('does not navigate on empty/whitespace input', async () => {
    const router = makeRouter();
    const wrapper = await mountAt(router, '/authors');
    await wrapper.find('input').setValue('   ');
    await wrapper.find('input').trigger('keydown.enter');
    await flushPromises();

    expect(router.currentRoute.value.path).toBe('/authors');
  });

  it('renders chips hydrated from the route query', async () => {
    const router = makeRouter();
    const wrapper = await mountAt(router, '/?q=dune&author=%3DTerry%20Pratchett&tag=SciFi');
    const chips = wrapper.findAll('[data-testid="chip"]').map((c) => c.text());
    expect(chips.some((t) => t.includes('Search: dune'))).toBe(true);
    expect(chips.some((t) => t.includes('Author = Terry Pratchett'))).toBe(true);
    expect(chips.some((t) => t.includes('Tag: SciFi'))).toBe(true);
  });

  it('removes a single filter when its chip ✕ is clicked', async () => {
    const router = makeRouter();
    const wrapper = await mountAt(router, '/?q=dune&author=Pratchett');
    const authorChip = wrapper
      .findAll('[data-testid="chip"]')
      .find((c) => c.text().includes('Author'))!;
    await authorChip.find('button').trigger('click');
    await flushPromises();

    expect(router.currentRoute.value.query).toEqual({ q: 'dune' });
  });

  // L12: the sort order is owned by BooksPage's select, but editing the search
  // chips must carry it through rather than silently resetting it.
  it('preserves the sort order when a chip is removed', async () => {
    const router = makeRouter();
    const wrapper = await mountAt(router, '/?author=Pratchett&sort=rating');
    const authorChip = wrapper
      .findAll('[data-testid="chip"]')
      .find((c) => c.text().includes('Author'))!;
    await authorChip.find('button').trigger('click');
    await flushPromises();

    expect(router.currentRoute.value.query).toEqual({ sort: 'rating' });
  });

  it('preserves the sort order when a chip is committed', async () => {
    const router = makeRouter();
    const wrapper = await mountAt(router, '/?sort=rating');
    await wrapper.find('input').setValue('robots');
    await wrapper.find('input').trigger('keydown.enter');
    await flushPromises();

    expect(router.currentRoute.value.query).toEqual({ q: 'robots', sort: 'rating' });
  });

  it('applies a Format chip when a value is picked from the dropdown', async () => {
    const router = makeRouter();
    const wrapper = await mountAt(router, '/');
    await wrapper.find('input').trigger('focus');

    // Format is a value facet (separate testid from the text facets).
    const formatFacet = wrapper
      .findAll('[data-testid="value-facet-option"]')
      .find((b) => b.text() === 'Format')!;
    await formatFacet.trigger('mousedown');

    const opts = wrapper.findAll('[data-testid="value-option"]');
    expect(opts.map((o) => o.text().trim())).toEqual(['epub', 'fb2']);
    await opts[0].trigger('mousedown');
    await flushPromises();

    expect(router.currentRoute.value.query).toEqual({ format: 'epub' });
  });

  it('renders and removes a format chip from the route query', async () => {
    const router = makeRouter();
    const wrapper = await mountAt(router, '/?format=epub');
    const chip = wrapper.findAll('[data-testid="chip"]').find((c) => c.text().includes('Format'))!;
    expect(chip.text()).toContain('Format: epub');
    await chip.find('button').trigger('click');
    await flushPromises();
    expect(router.currentRoute.value.query).toEqual({});
  });

  it('clears chips when navigating to a page with no filters', async () => {
    const router = makeRouter();
    const wrapper = await mountAt(router, '/?q=dune');
    expect(wrapper.findAll('[data-testid="chip"]')).toHaveLength(1);
    await router.push('/authors');
    await flushPromises();
    expect(wrapper.findAll('[data-testid="chip"]')).toHaveLength(0);
  });
});
