import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';

import { ALPHABET } from '@/alphabet';
import AlphabetSelector from '@/components/AlphabetSelector.vue';

function mountSelector(activeLetter: string | null, available: string[]) {
  return mount(AlphabetSelector, {
    props: { activeLetter, availableLetters: new Set(available) },
  });
}

function button(wrapper: ReturnType<typeof mountSelector>, letter: string) {
  return wrapper.findAll('[data-testid="letter-btn"]').find((b) => b.text() === letter)!;
}

describe('AlphabetSelector', () => {
  it('renders a button for every alphabet bucket when both scripts have items', () => {
    const wrapper = mountSelector(null, ['А', 'Z']);
    expect(wrapper.findAll('[data-testid="letter-btn"]')).toHaveLength(ALPHABET.length);
    expect(button(wrapper, 'А')).toBeTruthy(); // Cyrillic
    expect(button(wrapper, 'Z')).toBeTruthy(); // Latin
    expect(button(wrapper, '#')).toBeTruthy(); // catch-all
  });

  it('hides the Cyrillic block when no Cyrillic bucket has items', () => {
    const wrapper = mountSelector(null, ['A', 'C']);
    expect(button(wrapper, 'А')).toBeUndefined(); // Cyrillic А
    expect(button(wrapper, 'Я')).toBeUndefined();
    expect(button(wrapper, 'A')).toBeTruthy(); // Latin retained
    expect(button(wrapper, '#')).toBeTruthy();
  });

  it('hides the Latin block when no Latin bucket has items', () => {
    const wrapper = mountSelector(null, ['А', 'Б']);
    expect(button(wrapper, 'A')).toBeUndefined(); // Latin A
    expect(button(wrapper, 'Z')).toBeUndefined();
    expect(button(wrapper, 'А')).toBeTruthy(); // Cyrillic retained
    expect(button(wrapper, '#')).toBeTruthy();
  });

  it('disables letters that have no items', () => {
    const wrapper = mountSelector(null, ['A', 'C']);
    expect(button(wrapper, 'A').attributes('disabled')).toBeUndefined();
    expect(button(wrapper, 'B').attributes('disabled')).toBeDefined();
    expect(button(wrapper, 'C').attributes('disabled')).toBeUndefined();
  });

  it('marks the active letter', () => {
    const wrapper = mountSelector('C', ['A', 'C']);
    expect(button(wrapper, 'C').attributes('aria-pressed')).toBe('true');
    expect(button(wrapper, 'A').attributes('aria-pressed')).toBe('false');
  });

  it('emits select with the clicked letter', async () => {
    const wrapper = mountSelector(null, ['A', 'C']);
    await button(wrapper, 'C').trigger('click');
    expect(wrapper.emitted('select')).toEqual([['C']]);
  });
});
