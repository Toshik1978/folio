import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import CoverPickerModal from '@/components/CoverPickerModal.vue';
import { makeBook } from '@/test/factories';
import type { Book } from '@/types';

vi.mock('@/api', () => ({
  uploadCover: vi.fn(async () => makeBook({ id: 1, title: 'Dune' })),
  setCoverFromUrl: vi.fn(async () => makeBook({ id: 1, title: 'Dune' })),
  fetchCoverCandidates: vi.fn(async () => [
    {
      source: 'amazon',
      thumb_url: 'https://amz/t.jpg',
      full_url: 'https://amz/f.jpg',
      width: 1,
      height: 1,
    },
  ]),
}));
vi.mock('@/composables/useToast', () => ({
  useToast: () => ({ success: vi.fn(), error: vi.fn() }),
}));

import { fetchCoverCandidates, setCoverFromUrl, uploadCover } from '@/api';

const book = makeBook({ id: 1, title: 'Dune', cover_url: null });

// The dialog is teleported to <body>; stub teleport so it renders inline and
// the in-wrapper queries below keep resolving.
const mountModal = (props: { book: Book; open: boolean }) =>
  mount(CoverPickerModal, { props, global: { stubs: { teleport: true } } });

describe('CoverPickerModal', () => {
  beforeEach(() => vi.clearAllMocks());

  it('applies a cover from a URL', async () => {
    const wrapper = mountModal({ book, open: true });
    await wrapper.find('[data-testid="cover-url-input"]').setValue('https://x/c.jpg');
    await wrapper.find('[data-testid="cover-url-apply"]').trigger('click');
    await flushPromises();

    expect(setCoverFromUrl).toHaveBeenCalledWith(1, 'https://x/c.jpg');
    expect(wrapper.emitted('applied')).toBeTruthy();
  });

  it('uploads a chosen file', async () => {
    const wrapper = mountModal({ book, open: true });
    const file = new File([new Uint8Array([1, 2, 3])], 'c.png', { type: 'image/png' });
    const input = wrapper.find('[data-testid="cover-file"]').element as HTMLInputElement;
    Object.defineProperty(input, 'files', { value: [file] });
    await wrapper.find('[data-testid="cover-file"]').trigger('change');
    await flushPromises();

    expect(uploadCover).toHaveBeenCalledWith(1, file);
    expect(wrapper.emitted('applied')).toBeTruthy();
  });

  it('searches and renders a candidate grid, applying a clicked cover', async () => {
    const wrapper = mountModal({ book, open: true });
    await wrapper.find('[data-testid="cover-search-input"]').setValue('Dune');
    await wrapper.find('[data-testid="cover-search-go"]').trigger('click');
    await flushPromises();

    expect(fetchCoverCandidates).toHaveBeenCalledWith(1, 'Dune');
    const thumbs = wrapper.findAll('[data-testid="cover-candidate"]');
    expect(thumbs).toHaveLength(1);

    await thumbs[0].trigger('click');
    await flushPromises();
    expect(setCoverFromUrl).toHaveBeenCalledWith(1, 'https://amz/f.jpg');
    expect(wrapper.emitted('applied')).toBeTruthy();
  });

  it('exposes provider deep-link buttons', () => {
    const wrapper = mountModal({ book, open: true });
    expect(wrapper.find('[data-testid="deeplink-amazon"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="deeplink-goodreads"]').exists()).toBe(true);
  });
});
