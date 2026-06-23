import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';

import IdentifierEditor from '@/components/IdentifierEditor.vue';
import type { IdentifierInput } from '@/types';

const mountEditor = (modelValue: IdentifierInput[]) =>
  mount(IdentifierEditor, { props: { modelValue } });

describe('IdentifierEditor', () => {
  it('renders one row per identifier with its type and value', () => {
    const wrapper = mountEditor([
      { type: 'isbn', value: '9780441172719' },
      { type: 'google', value: 'qVZ1AAAAMAAJ' },
    ]);
    const selects = wrapper.findAll('select');
    const inputs = wrapper.findAll('input');
    expect(selects).toHaveLength(2);
    expect((selects[0].element as HTMLSelectElement).value).toBe('isbn');
    expect((inputs[1].element as HTMLInputElement).value).toBe('qVZ1AAAAMAAJ');
  });

  it('preserves an uncommon existing type as a selectable option', () => {
    const wrapper = mountEditor([{ type: 'doi', value: '10.1/x' }]);
    const opts = wrapper
      .findAll('select option')
      .map((o) => (o.element as HTMLOptionElement).value);
    expect(opts).toContain('doi'); // not dropped to a known type
    expect(opts).toContain('isbn');
  });

  it('adds a row defaulting to the first unused known type', async () => {
    const wrapper = mountEditor([{ type: 'isbn', value: 'x' }]);
    await wrapper.find('button.btn-sm').trigger('click'); // "Add identifier"
    const emitted = wrapper.emitted('update:modelValue')?.[0][0] as IdentifierInput[];
    expect(emitted).toEqual([
      { type: 'isbn', value: 'x' },
      { type: 'amazon', value: '' }, // isbn taken -> next known type
    ]);
  });

  it('edits a row value without mutating the prop', async () => {
    const wrapper = mountEditor([{ type: 'isbn', value: '' }]);
    await wrapper.find('input').setValue('9780441172719');
    const emitted = wrapper.emitted('update:modelValue')?.[0][0] as IdentifierInput[];
    expect(emitted).toEqual([{ type: 'isbn', value: '9780441172719' }]);
  });

  it('removes a row', async () => {
    const wrapper = mountEditor([
      { type: 'isbn', value: 'a' },
      { type: 'google', value: 'b' },
    ]);
    await wrapper.findAll('button[aria-label^="Remove"]')[0].trigger('click');
    const emitted = wrapper.emitted('update:modelValue')?.[0][0] as IdentifierInput[];
    expect(emitted).toEqual([{ type: 'google', value: 'b' }]);
  });

  it('keeps the same input element across an edit (focus/IME survives)', async () => {
    // Mount one row, then round-trip every emit back through setProps exactly as
    // the parent (plain v-model) does. If the row's :key changes on a value edit
    // Vue tears down and recreates the <input>, dropping focus/caret/IME.
    const wrapper = mountEditor([{ type: 'isbn', value: '' }]);

    const inputBefore = wrapper.find('input').element;

    await wrapper.find('input').setValue('9');
    const afterFirst = wrapper.emitted('update:modelValue')?.[0][0] as IdentifierInput[];
    await wrapper.setProps({ modelValue: afterFirst });

    // The edited row's input element must be the very same node — not a fresh one.
    expect(wrapper.find('input').element).toBe(inputBefore);

    // A second keystroke must also keep the same element.
    await wrapper.find('input').setValue('97');
    const afterSecond = wrapper.emitted('update:modelValue')?.[1][0] as IdentifierInput[];
    await wrapper.setProps({ modelValue: afterSecond });
    expect(wrapper.find('input').element).toBe(inputBefore);
  });

  it('gives duplicate-content rows distinct keys (two empty rows stay separate)', async () => {
    const wrapper = mountEditor([{ type: '', value: '' }]);

    // Add a second row, then feed the emitted value back (controlled pattern).
    await wrapper.find('button.btn-sm').trigger('click');
    const emitted = wrapper.emitted('update:modelValue')?.[0][0] as IdentifierInput[];
    await wrapper.setProps({ modelValue: emitted });

    const inputs = wrapper.findAll('input');
    expect(inputs).toHaveLength(2);
    // Two distinct DOM nodes even though both rows have identical (type, value).
    expect(inputs[0].element).not.toBe(inputs[1].element);
  });

  it('keeps correct DOM nodes when a middle row is removed', async () => {
    // Render three rows.
    const wrapper = mountEditor([
      { type: 'isbn', value: 'first' },
      { type: 'amazon', value: 'middle' },
      { type: 'google', value: 'last' },
    ]);

    // Capture the DOM element for the third row's input before the removal.
    const inputsBefore = wrapper.findAll('input');
    expect(inputsBefore).toHaveLength(3);
    const thirdInputElementBefore = inputsBefore[2].element;

    // Remove the middle row (index 1).
    const removeButtons = wrapper.findAll('button[aria-label^="Remove"]');
    await removeButtons[1].trigger('click');

    // The emitted payload must be the first and last rows only.
    const emitted = wrapper.emitted('update:modelValue')?.[0][0] as IdentifierInput[];
    expect(emitted).toEqual([
      { type: 'isbn', value: 'first' },
      { type: 'google', value: 'last' },
    ]);

    // Simulate the parent feeding the new modelValue back (controlled-input pattern).
    await wrapper.setProps({ modelValue: emitted });

    // With stable keys the third row's DOM element must be reused — not torn down
    // and recreated — so focus and composition state survive.
    const inputsAfter = wrapper.findAll('input');
    expect(inputsAfter).toHaveLength(2);
    expect(inputsAfter[1].element).toBe(thirdInputElementBefore);
  });
});
