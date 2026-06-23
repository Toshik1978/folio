import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';

import StarRating from '@/components/StarRating.vue';

describe('StarRating', () => {
  it('renders the correct number of filled and empty stars', () => {
    const wrapper = mount(StarRating, { props: { rating: 3 } });
    expect(wrapper.findAll('.pi-star-fill')).toHaveLength(3);
    expect(wrapper.findAll('.pi-star')).toHaveLength(2);
  });

  it('renders all filled stars for rating 5', () => {
    const wrapper = mount(StarRating, { props: { rating: 5 } });
    expect(wrapper.findAll('.pi-star-fill')).toHaveLength(5);
    expect(wrapper.findAll('.pi-star')).toHaveLength(0);
  });

  it('renders all empty stars for rating 0', () => {
    const wrapper = mount(StarRating, { props: { rating: 0 } });
    expect(wrapper.findAll('.pi-star-fill')).toHaveLength(0);
    expect(wrapper.findAll('.pi-star')).toHaveLength(5);
  });

  it('sets the aria-label to "Rating: N of 5"', () => {
    const wrapper = mount(StarRating, { props: { rating: 4 } });
    expect(wrapper.attributes('aria-label')).toBe('Rating: 4 of 5');
  });
});
