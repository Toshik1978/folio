import { beforeEach, describe, expect, it, vi } from 'vitest';

import { useToast } from './useToast';

describe('useToast', () => {
  beforeEach(() => {
    useToast().toasts.value = [];
    vi.useFakeTimers();
  });

  it('queues a success toast and auto-dismisses it', () => {
    const { toasts, success } = useToast();
    success('Saved');
    expect(toasts.value).toHaveLength(1);
    expect(toasts.value[0]).toMatchObject({ type: 'success', message: 'Saved' });

    vi.advanceTimersByTime(4000);
    expect(toasts.value).toHaveLength(0);
  });

  it('queues an error toast', () => {
    const { toasts, error } = useToast();
    error('Boom');
    expect(toasts.value[0]).toMatchObject({ type: 'error', message: 'Boom' });
  });
});
