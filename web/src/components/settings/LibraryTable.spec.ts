import { mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';

import LibraryTable from '@/components/settings/LibraryTable.vue';
import { useSyncStatus } from '@/composables/useSyncStatus';
import { makeLibrary } from '@/test/factories';

describe('LibraryTable', () => {
  beforeEach(() => {
    const sync = useSyncStatus();
    sync.running.value = false;
    sync.current.value = 0;
    sync.queued.value = [];
    sync.currentProgress.value = null;
  });
  afterEach(() => {
    const sync = useSyncStatus();
    sync.running.value = false;
    sync.current.value = 0;
    sync.queued.value = [];
    sync.currentProgress.value = null;
  });

  it('shows the empty state with no libraries', () => {
    const wrapper = mount(LibraryTable, { props: { libraries: [] } });
    expect(wrapper.find('[data-testid="library-empty"]').exists()).toBe(true);
    expect(wrapper.findAll('[data-testid="library-card"]')).toHaveLength(0);
  });

  it('emits sync with the library id when Sync Now is clicked', async () => {
    const wrapper = mount(LibraryTable, {
      props: { libraries: [makeLibrary({ id: 5, status: 'active' })] },
    });
    // [0] Sync Now, [1] Re-index, [2] Edit, [3] Delete
    await wrapper.findAll('[data-testid="library-action"]')[0].trigger('click');
    expect(wrapper.emitted('sync')).toEqual([[5]]);
  });

  it('renders the queued badge from live sync state', () => {
    const sync = useSyncStatus();
    sync.running.value = true;
    sync.current.value = 1;
    sync.queued.value = [6];
    const wrapper = mount(LibraryTable, {
      props: { libraries: [makeLibrary({ id: 6, status: 'active' })] },
    });
    expect(wrapper.find('[data-testid="library-card"]').text()).toContain('Queued');
  });
});
