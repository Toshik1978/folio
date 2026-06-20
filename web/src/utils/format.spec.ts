import { describe, expect, it } from 'vitest';

import { formatProgress, formatSize } from './format';

describe('formatSize', () => {
  it('formats bytes across decimal unit boundaries', () => {
    expect(formatSize(999)).toBe('999 B');
    expect(formatSize(1500)).toBe('2 KB'); // 1.5 → toFixed(0) rounds
    expect(formatSize(1_500_000)).toBe('1.5 MB');
    expect(formatSize(2_750_000_000)).toBe('2.8 GB');
  });
});

describe('formatProgress', () => {
  it('formats a determinate count', () => {
    expect(formatProgress(1200, 5000)).toBe('1,200 / 5,000');
  });
  it('formats an indeterminate count', () => {
    expect(formatProgress(1200)).toBe('1,200 books');
  });
});
