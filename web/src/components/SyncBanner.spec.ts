import { mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('@/api', () => ({
  fetchSyncStatus: vi.fn(),
  fetchLibraries: vi.fn(),
}));

import SyncBanner from '@/components/SyncBanner.vue';
import { useSyncStatus } from '@/composables/useSyncStatus';

describe('SyncBanner', () => {
  beforeEach(() => {
    const { running, current, queued, currentProgress, disconnect } = useSyncStatus();
    disconnect();
    running.value = false;
    current.value = 0;
    queued.value = [];
    currentProgress.value = null;
  });

  it('is hidden when idle', () => {
    const wrapper = mount(SyncBanner);
    expect(wrapper.find('[data-testid="sync-banner"]').exists()).toBe(false);
  });

  it('shows the queued count while running', async () => {
    const { running, queued } = useSyncStatus();
    running.value = true;
    queued.value = [2, 3];

    const wrapper = mount(SyncBanner);
    await wrapper.vm.$nextTick();

    const banner = wrapper.find('[data-testid="sync-banner"]');
    expect(banner.exists()).toBe(true);
    expect(banner.text()).toContain('2 queued');
  });

  it('shows determinate progress as X / N when total is known', async () => {
    const { running, currentProgress } = useSyncStatus();
    running.value = true;
    currentProgress.value = { processed: 1200, total: 5000 };

    const wrapper = mount(SyncBanner);
    await wrapper.vm.$nextTick();

    expect(wrapper.find('[data-testid="sync-banner"]').text()).toContain('1,200 / 5,000');
  });

  it('shows indeterminate progress as "X books" when total is unknown', async () => {
    const { running, currentProgress } = useSyncStatus();
    running.value = true;
    currentProgress.value = { processed: 1200 };

    const wrapper = mount(SyncBanner);
    await wrapper.vm.$nextTick();

    expect(wrapper.find('[data-testid="sync-banner"]').text()).toContain('1,200 books');
  });
});
