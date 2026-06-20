import { mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it } from 'vitest';

import ThemePicker from './ThemePicker.vue';

describe('ThemePicker', () => {
  beforeEach(() => {
    localStorage.clear();
    document.documentElement.removeAttribute('data-theme');
  });

  it('renders the curated themes and applies a selection', async () => {
    const wrapper = mount(ThemePicker);
    const nord = wrapper.get('[data-testid="theme-abyss"]');
    await nord.trigger('click');

    expect(document.documentElement.getAttribute('data-theme')).toBe('abyss');
    expect(localStorage.getItem('folio-theme')).toBe('abyss');
  });
});
