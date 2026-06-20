import { describe, expect, it } from 'vitest';

import { useConfirm } from './useConfirm';

describe('useConfirm', () => {
  it('opens, then resolves true when confirmed', async () => {
    const { state, confirm, respond } = useConfirm();
    const p = confirm({ title: 'Delete?', body: 'Sure?' });
    expect(state.value.open).toBe(true);

    respond(true);
    await expect(p).resolves.toBe(true);
    expect(state.value.open).toBe(false);
  });

  it('resolves false when cancelled', async () => {
    const { confirm, respond } = useConfirm();
    const p = confirm({ title: 'X', body: 'Y' });
    respond(false);
    await expect(p).resolves.toBe(false);
  });

  it('resolves a still-pending confirm as false when reopened', async () => {
    const { confirm, respond } = useConfirm();
    const first = confirm({ title: 'First', body: '?' });
    // A second confirm opens before the first is answered.
    const second = confirm({ title: 'Second', body: '?' });

    // The first promise must settle (false) rather than hang forever.
    await expect(first).resolves.toBe(false);

    respond(true);
    await expect(second).resolves.toBe(true);
  });
});
