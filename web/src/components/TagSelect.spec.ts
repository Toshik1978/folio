import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';

import TagSelect from '@/components/TagSelect.vue';

const mountSelect = (modelValue: string[], options: string[]) =>
  mount(TagSelect, { props: { modelValue, options } });

describe('TagSelect', () => {
  it('renders chosen tags as chips, including values outside the options list', () => {
    const wrapper = mountSelect(['History', 'Gone'], ['History', 'Science']);
    const chips = wrapper.findAll('.badge');
    expect(chips.map((c) => c.text().replace(/\s+/g, ' ').trim())).toEqual(['History ✕', 'Gone ✕']);
  });

  it('only offers unselected options, and adds the clicked one', async () => {
    const wrapper = mountSelect(['History'], ['History', 'Science', 'Art']);
    await wrapper.find('input').trigger('focus');

    const items = wrapper.findAll('.menu button');
    expect(items.map((i) => i.text())).toEqual(['Science', 'Art']); // History already chosen

    await items[0].trigger('mousedown');
    expect(wrapper.emitted('update:modelValue')?.[0]).toEqual([['History', 'Science']]);
  });

  it('filters the dropdown by the typed query without creating a free-text tag', async () => {
    const wrapper = mountSelect([], ['Science Fiction', 'History', 'Science Fact']);
    const input = wrapper.find('input');
    await input.setValue('scien');
    await input.trigger('focus');

    const items = wrapper.findAll('.menu button');
    expect(items.map((i) => i.text())).toEqual(['Science Fiction', 'Science Fact']);

    // Enter commits the first match (an existing option), never the raw text.
    await input.trigger('keydown', { key: 'Enter' });
    expect(wrapper.emitted('update:modelValue')?.[0]).toEqual([['Science Fiction']]);
  });

  it('removes a tag when its chip ✕ is clicked', async () => {
    const wrapper = mountSelect(['History', 'Science'], ['History', 'Science']);
    await wrapper.findAll('.badge button')[0].trigger('click');
    expect(wrapper.emitted('update:modelValue')?.[0]).toEqual([['Science']]);
  });
});
