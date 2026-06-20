import { mount } from '@vue/test-utils';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { defineComponent, h, nextTick, ref } from 'vue';

import { useModalFocus } from './useModalFocus';

function mountModal(onClose: () => void) {
  const open = ref(false);
  const wrapper = mount(
    defineComponent({
      setup() {
        const box = ref<HTMLElement | null>(null);
        useModalFocus(open, box, onClose);
        return () =>
          h('div', { ref: box }, [
            h('button', { id: 'first' }, 'a'),
            h('button', { id: 'last' }, 'b'),
          ]);
      },
    }),
    { attachTo: document.body },
  );
  return { open, wrapper };
}

afterEach(() => {
  document.body.innerHTML = '';
});

describe('useModalFocus', () => {
  it('moves focus into the modal on open and restores it on close', async () => {
    const outside = document.createElement('button');
    document.body.appendChild(outside);
    outside.focus();

    const { open } = mountModal(vi.fn());
    open.value = true;
    await nextTick();
    await nextTick(); // focus is applied on the tick after activation
    expect(document.activeElement?.id).toBe('first');

    open.value = false;
    await nextTick();
    expect(document.activeElement).toBe(outside);
  });

  it('Escape closes only the top-most modal', async () => {
    const closeBottom = vi.fn();
    const closeTop = vi.fn();
    const bottom = mountModal(closeBottom);
    const top = mountModal(closeTop);
    bottom.open.value = true;
    await nextTick();
    top.open.value = true;
    await nextTick();

    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    expect(closeTop).toHaveBeenCalledTimes(1);
    expect(closeBottom).not.toHaveBeenCalled();
  });

  it('Tab wraps from the last focusable back to the first', async () => {
    const { open } = mountModal(vi.fn());
    open.value = true;
    await nextTick();
    await nextTick();

    document.getElementById('last')?.focus();
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab' }));
    expect(document.activeElement?.id).toBe('first');
  });
});
