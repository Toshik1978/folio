import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import CoverPickerModal from '@/components/CoverPickerModal.vue';
import type { Book } from '@/types';

vi.mock('@/api', () => ({
  uploadCover: vi.fn(async () => ({ id: 1, title: 'Dune' }) as Book),
  setCoverFromUrl: vi.fn(async () => ({ id: 1, title: 'Dune' }) as Book),
}));
vi.mock('@/composables/useToast', () => ({
  useToast: () => ({ success: vi.fn(), error: vi.fn() }),
}));

import { setCoverFromUrl, uploadCover } from '@/api';

const book = { id: 1, title: 'Dune', cover_url: null } as Book;

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
});
