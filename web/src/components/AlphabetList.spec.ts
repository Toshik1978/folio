import { mount, RouterLinkStub } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';

import AlphabetList from '@/components/AlphabetList.vue';

const items = [
  { name: 'Asimov', book_count: 42 },
  { name: 'Clarke', book_count: 7 },
  { name: 'Atwood', book_count: 3 },
];

function mountList() {
  return mount(AlphabetList, {
    props: { items, filterKey: 'author' },
    global: { stubs: { RouterLink: RouterLinkStub } },
  });
}

describe('AlphabetList', () => {
  it('groups items by first letter, sorted alphabetically', () => {
    const headings = mountList()
      .findAll('[data-testid="group-heading"]')
      .map((h) => h.text());
    expect(headings).toEqual(['A', 'C']);
  });

  it('keeps input order within a group', () => {
    const names = mountList()
      .findAll('[data-testid="item-name"]')
      .map((n) => n.text());
    // A-group (Asimov, Atwood) precedes C-group (Clarke).
    expect(names).toEqual(['Asimov', 'Atwood', 'Clarke']);
  });

  it('renders each item book count', () => {
    expect(mountList().findAll('[data-testid="item-count"]')[0].text()).toBe('42 books');
  });

  it('uses the singular noun for a count of one', () => {
    const wrapper = mount(AlphabetList, {
      props: { items: [{ name: 'Solo', book_count: 1 }], filterKey: 'author' },
      global: { stubs: { RouterLink: RouterLinkStub } },
    });
    expect(wrapper.find('[data-testid="item-count"]').text()).toBe('1 book');
  });

  it('links each item to a filtered books query', () => {
    const link = mountList().findAllComponents(RouterLinkStub)[0];
    expect(link.props('to')).toEqual({ path: '/', query: { author: '=Asimov' } });
  });
});
