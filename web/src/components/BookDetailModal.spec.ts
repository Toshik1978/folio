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

  it('shows the last-requested book when two rapid id changes race (stale fetch ignored)', async () => {
    // Arrange: two deferred resolvers so we can control resolution order
    let resolveFirst!: (book: ReturnType<typeof makeBook>) => void;
    let resolveSecond!: (book: ReturnType<typeof makeBook>) => void;
    const firstFetch = new Promise<ReturnType<typeof makeBook>>((res) => {
      resolveFirst = res;
    });
    const secondFetch = new Promise<ReturnType<typeof makeBook>>((res) => {
      resolveSecond = res;
    });

    const book1 = makeBook({ id: 1, title: 'Book One' });
    const book2 = makeBook({ id: 2, title: 'Book Two' });

    vi.mocked(fetchBook).mockReturnValueOnce(firstFetch).mockReturnValueOnce(secondFetch);

    // Mount with id=1 (first fetch in flight)
    const wrapper = mount(BookDetailModal, { props: { id: 1 }, ...opts });

    // Quickly switch to id=2 (second fetch in flight)
    await wrapper.setProps({ id: 2 });

    // Resolve the OLDER fetch last (stale result arriving after the newer one)
    resolveSecond(book2);
    await flushPromises();
    resolveFirst(book1); // stale — should be ignored
    await flushPromises();

    // Only book2 should be displayed; book1's stale resolution must not overwrite it
    expect(wrapper.find('[data-testid="detail-title"]').text()).toBe('Book Two');
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
