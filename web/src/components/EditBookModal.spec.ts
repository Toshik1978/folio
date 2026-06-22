import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

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

// For Escape-key integration tests we need events to bubble to window so the
// useModalFocus window listener fires. attachTo: document.body enables that.
const mountModalAttached = (props: { book: Book; open: boolean }) =>
  mount(EditBookModal, {
    props,
    attachTo: document.body,
    global: { stubs: { teleport: true } },
  });

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

  describe('Escape key — dropdown vs modal', () => {
    afterEach(() => {
      // Unmount any lingering attached components so they don't pollute the window
      // listener stack across tests.
      document.body.innerHTML = '';
    });

    it('Esc on Tags input while dropdown is open closes only the dropdown, not the modal', async () => {
      // Mount closed first so the watch fires when we open it (the watcher runs
      // on open→true transitions; it would be a no-op if open starts as true).
      const wrapper = mountModalAttached({ book, open: false });
      await wrapper.setProps({ open: true });
      await flushPromises();

      const tagsInput = wrapper.find('[data-testid="edit-tags"]');
      await tagsInput.trigger('focus');
      await wrapper.vm.$nextTick();
      // Dropdown should be visible (fetchGenres returned ['Science Fiction', 'History']).
      expect(wrapper.find('.menu').exists()).toBe(true);

      // Dispatch a bubbling Escape from the tags input.
      const escEvent = new KeyboardEvent('keydown', {
        key: 'Escape',
        bubbles: true,
        cancelable: true,
      });
      tagsInput.element.dispatchEvent(escEvent);
      await wrapper.vm.$nextTick();

      // The dropdown closes but the modal is still open — no 'close' emit.
      expect(wrapper.find('.menu').exists()).toBe(false);
      expect(wrapper.emitted('close')).toBeFalsy();

      wrapper.unmount();
    });

    it('Esc on a plain text field (title) propagates and closes the modal', async () => {
      const wrapper = mountModalAttached({ book, open: false });
      await wrapper.setProps({ open: true });
      await flushPromises();

      const titleInput = wrapper.find('[data-testid="edit-title"]');
      const escEvent = new KeyboardEvent('keydown', {
        key: 'Escape',
        bubbles: true,
        cancelable: true,
      });
      titleInput.element.dispatchEvent(escEvent);
      await wrapper.vm.$nextTick();

      expect(wrapper.emitted('close')).toBeTruthy();

      wrapper.unmount();
    });

    it('Esc on the language select does not close the modal', async () => {
      const wrapper = mountModalAttached({ book, open: false });
      await wrapper.setProps({ open: true });
      await flushPromises();

      const langSelect = wrapper.find('[data-testid="edit-language"]');
      const escEvent = new KeyboardEvent('keydown', {
        key: 'Escape',
        bubbles: true,
        cancelable: true,
      });
      langSelect.element.dispatchEvent(escEvent);
      await wrapper.vm.$nextTick();

      expect(wrapper.emitted('close')).toBeFalsy();

      wrapper.unmount();
    });
  });
});
