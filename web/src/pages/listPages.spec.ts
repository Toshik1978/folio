import { flushPromises, mount, RouterLinkStub } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { Component } from 'vue';

import {
  fetchPublisherLetters,
  fetchPublishers,
  fetchSeries,
  fetchSeriesLetters,
  fetchTagLetters,
  fetchTags,
} from '@/api';
import { useLibrary } from '@/composables/useLibrary';
import PublisherListPage from '@/pages/PublisherListPage.vue';
import SeriesListPage from '@/pages/SeriesListPage.vue';
import TagListPage from '@/pages/TagListPage.vue';

// These three pages are near-identical thin wrappers that wire useAlphabetBrowse
// to a specific (lettersFn, listFn) pair and an AlphabetList filter-key. The bugs
// they invite are copy-paste wiring mistakes: the wrong fetch function or the
// wrong filter-key. This shared spec pins both for each page.
vi.mock('@/api', () => ({
  fetchSeries: vi.fn(),
  fetchSeriesLetters: vi.fn(),
  fetchPublishers: vi.fn(),
  fetchPublisherLetters: vi.fn(),
  fetchTags: vi.fn(),
  fetchTagLetters: vi.fn(),
}));

const opts = { global: { stubs: { RouterLink: RouterLinkStub } } };

interface Case {
  name: string;
  component: Component;
  lettersFn: ReturnType<typeof vi.fn>;
  listFn: ReturnType<typeof vi.fn>;
  // The query key AlphabetList must emit on each item link.
  filterKey: string;
  // What that link's query value should be for an item named "Foo": series is an
  // FTS facet and gets the exact-match '=' prefix; publisher/tag are exact-only.
  expectedLinkValue: string;
}

const cases: Case[] = [
  {
    name: 'SeriesListPage',
    component: SeriesListPage,
    lettersFn: vi.mocked(fetchSeriesLetters),
    listFn: vi.mocked(fetchSeries),
    filterKey: 'series',
    expectedLinkValue: '=Foo',
  },
  {
    name: 'PublisherListPage',
    component: PublisherListPage,
    lettersFn: vi.mocked(fetchPublisherLetters),
    listFn: vi.mocked(fetchPublishers),
    filterKey: 'publisher',
    expectedLinkValue: 'Foo',
  },
  {
    name: 'TagListPage',
    component: TagListPage,
    lettersFn: vi.mocked(fetchTagLetters),
    listFn: vi.mocked(fetchTags),
    filterKey: 'tag',
    expectedLinkValue: 'Foo',
  },
];

describe.each(cases)('$name', ({ component, lettersFn, listFn, filterKey, expectedLinkValue }) => {
  beforeEach(() => {
    // Reset every api mock so a sibling page's wiring can't satisfy this one.
    for (const fn of [
      fetchSeries,
      fetchSeriesLetters,
      fetchPublishers,
      fetchPublisherLetters,
      fetchTags,
      fetchTagLetters,
    ]) {
      vi.mocked(fn).mockReset();
    }
    localStorage.clear();
    useLibrary().setLibrary(null);
  });

  it('loads its own letters and lists its own first letter on mount', async () => {
    lettersFn.mockResolvedValue(['A', 'C']);
    listFn.mockResolvedValue([{ id: 1, name: 'Foo', book_count: 7 }]);

    const wrapper = mount(component, opts);
    await flushPromises();

    // The page is wired to its own pair, not a sibling's.
    expect(lettersFn).toHaveBeenCalledOnce();
    expect(listFn).toHaveBeenCalledWith('A', undefined, 1, 100);

    expect(wrapper.text()).toContain('Foo');
    expect(wrapper.text()).toContain('7 books');

    // Only letters that have data are enabled in the selector.
    const enabled = wrapper
      .findAll('[data-testid="letter-btn"]')
      .filter((b) => b.attributes('disabled') === undefined)
      .map((b) => b.text());
    expect(enabled).toEqual(['A', 'C']);
  });

  it('renders item links with its own filter-key and exact-match semantics', async () => {
    lettersFn.mockResolvedValue(['A']);
    listFn.mockResolvedValue([{ id: 1, name: 'Foo', book_count: 1 }]);

    const wrapper = mount(component, opts);
    await flushPromises();

    const link = wrapper.findComponent(RouterLinkStub);
    expect(link.props('to')).toEqual({ path: '/', query: { [filterKey]: expectedLinkValue } });
  });

  it('loads the selected letter when a selector button is clicked', async () => {
    lettersFn.mockResolvedValue(['A', 'C']);
    listFn.mockResolvedValue([]);

    const wrapper = mount(component, opts);
    await flushPromises();
    listFn.mockClear();

    const cButton = wrapper.findAll('[data-testid="letter-btn"]').find((b) => b.text() === 'C');
    await cButton!.trigger('click');
    await flushPromises();

    expect(listFn).toHaveBeenCalledWith('C', undefined, 1, 100);
  });
});
