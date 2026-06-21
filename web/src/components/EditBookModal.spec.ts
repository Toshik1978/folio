import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import EditBookModal from '@/components/EditBookModal.vue';
import type { Book } from '@/types';

vi.mock('@/api', () => ({
  updateBookMetadata: vi.fn(async () => ({ id: 1, title: 'Dune' }) as Book),
  fetchGenres: vi.fn(async () => ['Science Fiction', 'History']),
}));
vi.mock('@/composables/useToast', () => ({
  useToast: () => ({ success: vi.fn(), error: vi.fn() }),
}));

import { updateBookMetadata } from '@/api';

const book: Book = {
  id: 1,
  title: 'Old',
  authors: [{ id: 1, name: 'Nobody' }],
  series: null,
  series_index: null,
  tags: ['History'],
  publisher: null,
  year: null,
  pages: null,
  rating: null,
  language: 'en',
  annotation: null,
  formats: [],
  identifiers: [],
  cover_url: null,
};

// The dialog is teleported to <body>; stub teleport so it renders inline and
// the in-wrapper queries below keep resolving.
const mountModal = (props: { book: Book; open: boolean }) =>
  mount(EditBookModal, { props, global: { stubs: { teleport: true } } });

describe('EditBookModal', () => {
  beforeEach(() => vi.clearAllMocks());

  it('saves edited title via updateBookMetadata and emits applied', async () => {
    const wrapper = mountModal({ book, open: true });
    await flushPromises();

    await wrapper.find('[data-testid="edit-title"]').setValue('Dune');
    await wrapper.find('[data-testid="edit-save"]').trigger('click');
    await flushPromises();

    expect(updateBookMetadata).toHaveBeenCalledWith(1, expect.objectContaining({ title: 'Dune' }));
    expect(wrapper.emitted('applied')).toBeTruthy();
  });

  it('blocks save when the title is empty', async () => {
    const wrapper = mountModal({ book, open: true });
    await flushPromises();

    await wrapper.find('[data-testid="edit-title"]').setValue('   ');
    await wrapper.find('[data-testid="edit-save"]').trigger('click');
    await flushPromises();

    expect(updateBookMetadata).not.toHaveBeenCalled();
  });
});
