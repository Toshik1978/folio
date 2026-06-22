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
});
