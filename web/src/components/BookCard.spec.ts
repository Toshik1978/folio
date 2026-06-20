import { mount, RouterLinkStub } from '@vue/test-utils';
import { describe, expect, it, vi } from 'vitest';

import BookCard from '@/components/BookCard.vue';
import { makeAuthor, makeBook } from '@/test/factories';

vi.mock('vue-router', () => ({ useRoute: () => ({ query: { author: 'Asimov' } }) }));

const opts = { global: { stubs: { RouterLink: RouterLinkStub } } };

describe('BookCard', () => {
  it('renders the title and comma-joined author names', () => {
    const book = makeBook({
      title: 'Foundation',
      authors: [makeAuthor({ name: 'Isaac Asimov' }), makeAuthor({ id: 2, name: 'Co Author' })],
    });
    const wrapper = mount(BookCard, { props: { book }, ...opts });
    expect(wrapper.text()).toContain('Foundation');
    expect(wrapper.text()).toContain('Isaac Asimov, Co Author');
  });

  it('links to the book detail route preserving the current filters', () => {
    const wrapper = mount(BookCard, { props: { book: makeBook({ id: 42 }) }, ...opts });
    expect(wrapper.getComponent(RouterLinkStub).props('to')).toEqual({
      path: '/books/42',
      query: { author: 'Asimov' },
    });
  });

  it('shows the cover image when cover_url is set', () => {
    const wrapper = mount(BookCard, {
      props: { book: makeBook({ cover_url: '/api/books/7/cover' }) },
      ...opts,
    });
    const img = wrapper.find('img');
    expect(img.exists()).toBe(true);
    expect(img.attributes('src')).toBe('/api/books/7/cover');
  });

  it('falls back to a placeholder when cover_url is null', () => {
    const wrapper = mount(BookCard, { props: { book: makeBook({ cover_url: null }) }, ...opts });
    expect(wrapper.find('img').exists()).toBe(false);
    expect(wrapper.find('[data-testid="cover-placeholder"]').exists()).toBe(true);
  });

  it('renders filled stars for the rating', () => {
    const wrapper = mount(BookCard, { props: { book: makeBook({ rating: 4 }) }, ...opts });
    expect(wrapper.findAll('.pi-star-fill')).toHaveLength(4);
    expect(wrapper.findAll('.pi-star')).toHaveLength(1);
  });

  it('shows no stars when the book is unrated', () => {
    const wrapper = mount(BookCard, { props: { book: makeBook({ rating: null }) }, ...opts });
    expect(wrapper.find('.pi-star-fill').exists()).toBe(false);
    expect(wrapper.find('.pi-star').exists()).toBe(false);
  });
});
