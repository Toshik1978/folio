import { flushPromises, mount, RouterLinkStub } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { fetchAuthorLetters, fetchAuthors } from '@/api';
import { useLibrary } from '@/composables/useLibrary';
import AuthorListPage from '@/pages/AuthorListPage.vue';

vi.mock('@/api', () => ({ fetchAuthors: vi.fn(), fetchAuthorLetters: vi.fn() }));

const opts = { global: { stubs: { RouterLink: RouterLinkStub } } };

describe('AuthorListPage', () => {
  beforeEach(() => {
    vi.mocked(fetchAuthors).mockReset();
    vi.mocked(fetchAuthorLetters).mockReset();
    localStorage.clear();
    useLibrary().setLibrary(null);
  });

  it('loads available letters, defaults to the first, and lists its authors', async () => {
    vi.mocked(fetchAuthorLetters).mockResolvedValue(['A', 'C']);
    vi.mocked(fetchAuthors).mockResolvedValue([{ id: 1, name: 'Asimov', book_count: 42 }]);
    const wrapper = mount(AuthorListPage, opts);
    await flushPromises();

    expect(fetchAuthorLetters).toHaveBeenCalledOnce();
    // Defaults to the first available letter and loads its first page.
    expect(fetchAuthors).toHaveBeenCalledWith('A', undefined, 1, 100);
    expect(wrapper.text()).toContain('Asimov');
    expect(wrapper.text()).toContain('42 books');

    // Only letters with data are enabled in the selector.
    const enabled = wrapper
      .findAll('[data-testid="letter-btn"]')
      .filter((b) => b.attributes('disabled') === undefined)
      .map((b) => b.text());
    expect(enabled).toEqual(['A', 'C']);
  });

  it('loads the selected letter when a selector button is clicked', async () => {
    vi.mocked(fetchAuthorLetters).mockResolvedValue(['A', 'C']);
    vi.mocked(fetchAuthors).mockResolvedValue([]);
    const wrapper = mount(AuthorListPage, opts);
    await flushPromises();
    vi.mocked(fetchAuthors).mockClear();

    const cButton = wrapper.findAll('[data-testid="letter-btn"]').find((b) => b.text() === 'C');
    await cButton!.trigger('click');
    await flushPromises();

    expect(fetchAuthors).toHaveBeenCalledWith('C', undefined, 1, 100);
  });
});
