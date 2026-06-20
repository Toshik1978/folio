import { flushPromises, mount, RouterLinkStub } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { fetchBook } from '@/api';
import BookDetail from '@/components/BookDetail.vue';
import BookDetailModal from '@/components/BookDetailModal.vue';
import { makeBook } from '@/test/factories';

vi.mock('@/api', () => ({ fetchBook: vi.fn() }));

const opts = { global: { stubs: { RouterLink: RouterLinkStub } } };

describe('BookDetailModal', () => {
  beforeEach(() => vi.mocked(fetchBook).mockReset());

  it('renders nothing and is closed when id is null', () => {
    const wrapper = mount(BookDetailModal, { props: { id: null }, ...opts });
    expect(wrapper.findComponent(BookDetail).exists()).toBe(false);
    expect(wrapper.find('.modal').classes()).not.toContain('modal-open');
  });

  it('fetches the book by id and renders its detail', async () => {
    vi.mocked(fetchBook).mockResolvedValue(makeBook({ id: 7, title: 'Foundation' }));
    const wrapper = mount(BookDetailModal, { props: { id: 7 }, ...opts });
    await flushPromises();

    expect(fetchBook).toHaveBeenCalledWith(7);
    expect(wrapper.find('.modal').classes()).toContain('modal-open');
    expect(wrapper.findComponent(BookDetail).exists()).toBe(true);
    expect(wrapper.find('[data-testid="detail-title"]').text()).toBe('Foundation');
  });

  it('shows a loading spinner until the book resolves', async () => {
    vi.mocked(fetchBook).mockResolvedValue(makeBook());
    const wrapper = mount(BookDetailModal, { props: { id: 7 }, ...opts });
    expect(wrapper.find('[data-testid="detail-loading"]').exists()).toBe(true);
    await flushPromises();
    expect(wrapper.find('[data-testid="detail-loading"]').exists()).toBe(false);
  });

  it('emits close on the close button', async () => {
    vi.mocked(fetchBook).mockResolvedValue(makeBook());
    const wrapper = mount(BookDetailModal, { props: { id: 7 }, ...opts });
    await wrapper.find('[data-testid="modal-close"]').trigger('click');
    expect(wrapper.emitted('close')).toHaveLength(1);
  });

  it('emits close on backdrop click', async () => {
    vi.mocked(fetchBook).mockResolvedValue(makeBook());
    const wrapper = mount(BookDetailModal, { props: { id: 7 }, ...opts });
    await wrapper.find('[data-testid="modal-backdrop"]').trigger('click');
    expect(wrapper.emitted('close')).toHaveLength(1);
  });

  it('emits close on Escape when open', async () => {
    vi.mocked(fetchBook).mockResolvedValue(makeBook());
    const wrapper = mount(BookDetailModal, { props: { id: 7 }, ...opts });
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    await flushPromises();
    expect(wrapper.emitted('close')).toHaveLength(1);
  });

  it('re-emits updated and refreshes its detail when the book is enriched/matched', async () => {
    vi.mocked(fetchBook).mockResolvedValue(makeBook({ id: 7, title: 'Old Title' }));
    const wrapper = mount(BookDetailModal, { props: { id: 7 }, ...opts });
    await flushPromises();

    const updated = makeBook({ id: 7, title: 'Corrected Title' });
    wrapper.findComponent(BookDetail).vm.$emit('updated', updated);
    await flushPromises();

    expect(wrapper.emitted('updated')).toHaveLength(1);
    expect(wrapper.emitted('updated')?.[0]).toEqual([updated]);
    expect(wrapper.find('[data-testid="detail-title"]').text()).toBe('Corrected Title');
  });
});
