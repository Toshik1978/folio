import { describe, expect, it } from 'vitest';

import { statusClass, statusLabel, typeLabel } from '@/utils/libraryStatus';

describe('libraryStatus', () => {
  it('maps statuses to DaisyUI badge classes', () => {
    expect(statusClass('active')).toBe('badge-success');
    expect(statusClass('syncing')).toBe('badge-info');
    expect(statusClass('error')).toBe('badge-error');
    expect(statusClass('pending_purge')).toBe('badge-warning');
    expect(statusClass('queued')).toBe('badge-ghost');
    expect(statusClass('whatever')).toBe('badge-ghost');
  });

  it('maps statuses to human labels', () => {
    expect(statusLabel('active')).toBe('Active');
    expect(statusLabel('pending_purge')).toBe('Pending Purge');
    expect(statusLabel('queued')).toBe('Queued');
    expect(statusLabel('mystery')).toBe('mystery');
  });

  it('labels library types', () => {
    expect(typeLabel('calibre')).toBe('Calibre');
    expect(typeLabel('inpx')).toBe('INPX');
    expect(typeLabel('folder')).toBe('Folder');
  });
});
