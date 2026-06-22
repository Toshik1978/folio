import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { applyMatch, searchMatch } from '@/api';
import FixMatchModal from '@/components/FixMatchModal.vue';
import { makeBook } from '@/test/factories';
import type { MatchCandidate } from '@/types';

vi.mock('@/api', () => ({ searchMatch: vi.fn(), applyMatch: vi.fn() }));

// The dialog is teleported to <body>; stub teleport so it renders inline and
// the in-wrapper queries below keep resolving.
const mountModal = (props: { bookId: number; open: boolean; initialQuery: string }) =>
  mount(FixMatchModal, { props, global: { stubs: { teleport: true } } });

const candidate: MatchCandidate = {
  source: 'googlebooks',
  volume_id: 'vol1',
  title: 'Dune',
  authors: ['Frank Herbert'],
  year: 1965,
  thumbnail: 'http://img/t.jpg',
};

describe('FixMatchModal', () => {
  beforeEach(() => {
    vi.mocked(searchMatch).mockReset();
    vi.mocked(applyMatch).mockReset();
  });

  it('is hidden when open is false', () => {
    const wrapper = mountModal({ bookId: 7, open: false, initialQuery: '' });
    expect(wrapper.find('.modal').classes()).not.toContain('modal-open');
  });

  it('prefills the query on open and lists search candidates', async () => {
    vi.mocked(searchMatch).mockResolvedValue([candidate]);
    const wrapper = mountModal({ bookId: 7, open: false, initialQuery: 'Dune Frank Herbert' });
    await wrapper.setProps({ open: true }); // triggers the prefill watch

    await wrapper.find('form').trigger('submit');
    await flushPromises();

    expect(searchMatch).toHaveBeenCalledWith(7, 'Dune Frank Herbert');
    const results = wrapper.find('[data-testid="fixmatch-results"]');
    expect(results.exists()).toBe(true);
    expect(results.text()).toContain('Dune');
    expect(results.text()).toContain('Frank Herbert');
  });

  it('applies a chosen candidate and emits the updated book then closes', async () => {
    vi.mocked(searchMatch).mockResolvedValue([candidate]);
    const updated = makeBook({ id: 7, title: 'Dune', annotation: 'Updated.' });
    vi.mocked(applyMatch).mockResolvedValue(updated);

    const wrapper = mountModal({ bookId: 7, open: false, initialQuery: 'Dune' });
    await wrapper.setProps({ open: true });
    await wrapper.find('form').trigger('submit');
    await flushPromises();

    await wrapper.find('[data-testid="fixmatch-results"] button').trigger('click');
    await flushPromises();

    expect(applyMatch).toHaveBeenCalledWith(7, 'googlebooks', 'vol1');
    expect(wrapper.emitted('applied')?.[0]).toEqual([updated]);
    expect(wrapper.emitted('close')).toBeTruthy();
  });
});
