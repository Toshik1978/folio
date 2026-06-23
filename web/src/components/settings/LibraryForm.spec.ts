import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import LibraryForm from '@/components/settings/LibraryForm.vue';
import { useToast } from '@/composables/useToast';
import { makeLibrary } from '@/test/factories';

describe('LibraryForm', () => {
  beforeEach(() => {
    window.HTMLElement.prototype.scrollIntoView = vi.fn();
  });
  afterEach(() => vi.clearAllMocks());

  it('emits submit with a NewLibrary payload in add mode', async () => {
    const wrapper = mount(LibraryForm, { props: { editing: null } });
    const text = wrapper.findAll('input[type="text"]');
    await text[0].setValue('New'); // name
    await text[1].setValue('/p'); // path
    await wrapper.find('[data-testid="library-save"]').trigger('click');
    expect(wrapper.emitted('submit')).toEqual([
      [{ name: 'New', type: 'calibre', path: '/p', sync_interval_seconds: 3600 }],
    ]);
  });

  it('does not emit and toasts when required fields are missing', async () => {
    const wrapper = mount(LibraryForm, { props: { editing: null } });
    await wrapper.find('[data-testid="library-save"]').trigger('click');
    expect(wrapper.emitted('submit')).toBeUndefined();
    expect(useToast().toasts.value.some((t) => t.message === 'Name is required')).toBe(true);
  });

  it('seeds fields and shows the edit title when editing', async () => {
    const wrapper = mount(LibraryForm, {
      props: { editing: makeLibrary({ id: 5, name: 'Lib', type: 'inpx', path: '/x' }) },
    });
    await flushPromises();
    expect(wrapper.find('[data-testid="library-form-title"]').text()).toBe('Edit Library');
    expect((wrapper.findAll('input[type="text"]')[0].element as HTMLInputElement).value).toBe(
      'Lib',
    );
  });

  it('emits cancel when Cancel is clicked in edit mode', async () => {
    const wrapper = mount(LibraryForm, { props: { editing: makeLibrary({ id: 5 }) } });
    await flushPromises();
    await wrapper
      .findAll('button')
      .find((b) => b.text() === 'Cancel')!
      .trigger('click');
    expect(wrapper.emitted('cancel')).toHaveLength(1);
  });

  it('defaults sync_interval_seconds to 3600 when the interval field is cleared', async () => {
    const wrapper = mount(LibraryForm, { props: { editing: null } });
    const text = wrapper.findAll('input[type="text"]');
    await text[0].setValue('New'); // name
    await text[1].setValue('/p'); // path
    // Clear the interval field — Vue's .number modifier yields '' for an empty input
    const intervalInput = wrapper.find('input[type="number"]');
    await intervalInput.setValue('');
    await wrapper.find('[data-testid="library-save"]').trigger('click');
    const emitted = wrapper.emitted('submit') as unknown[][];
    expect(emitted).toHaveLength(1);
    const payload = emitted[0][0] as { sync_interval_seconds: unknown };
    expect(typeof payload.sync_interval_seconds).toBe('number');
    expect(payload.sync_interval_seconds).toBe(3600);
  });
});
