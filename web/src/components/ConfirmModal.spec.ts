import { flushPromises, mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';

import ConfirmModal from '@/components/ConfirmModal.vue';
import { useConfirm } from '@/composables/useConfirm';

describe('ConfirmModal', () => {
  it('is closed until a confirm is requested', () => {
    const wrapper = mount(ConfirmModal);
    expect(wrapper.find('.modal').classes()).not.toContain('modal-open');
  });

  it('resolves the confirm promise on the buttons', async () => {
    const wrapper = mount(ConfirmModal);
    const { confirm } = useConfirm();

    const p = confirm({ title: 'Delete?', body: 'Sure?' });
    await flushPromises();
    expect(wrapper.find('.modal').classes()).toContain('modal-open');

    await wrapper.find('[data-testid="confirm-ok"]').trigger('click');
    await expect(p).resolves.toBe(true);
  });

  it('resolves false on Escape when open (F5)', async () => {
    const wrapper = mount(ConfirmModal);
    const { confirm } = useConfirm();

    const p = confirm({ title: 'Delete?', body: 'Sure?' });
    await flushPromises();

    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    await expect(p).resolves.toBe(false);
    await flushPromises();
    expect(wrapper.find('.modal').classes()).not.toContain('modal-open');
  });
});
