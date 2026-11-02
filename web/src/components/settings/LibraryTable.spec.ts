import { mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import LibraryTable from '@/components/settings/LibraryTable.vue';
import { useLibrary } from '@/composables/useLibrary';
import { useSyncStatus } from '@/composables/useSyncStatus';
import { makeLibrary } from '@/test/factories';

const { push } = vi.hoisted(() => ({ push: vi.fn() }));
vi.mock('vue-router', () => ({
  useRouter: () => ({ push }),
}));

describe('LibraryTable', () => {
  beforeEach(() => {
    push.mockReset();
    const sync = useSyncStatus();
    sync.running.value = false;
    sync.current.value = 0;
    sync.queued.value = [];
    sync.currentProgress.value = null;
    sync.currentKind.value = null;
  });
  afterEach(() => {
    const sync = useSyncStatus();
    sync.running.value = false;
    sync.current.value = 0;
    sync.queued.value = [];
    sync.currentProgress.value = null;
    sync.currentKind.value = null;
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

  it('emits deepIndex with the library when Deep Index is clicked', async () => {
    const lib = makeLibrary({ id: 5, status: 'active' });
    const wrapper = mount(LibraryTable, { props: { libraries: [lib] } });
    const btn = wrapper.findAll('[data-testid="library-action"]')[2];
    expect(btn.text()).toBe('Deep Index');
    await btn.trigger('click');
    expect(wrapper.emitted('deepIndex')).toEqual([[5]]);
  });

  it('disables Deep Index while the row is busy', () => {
    const sync = useSyncStatus();
    sync.running.value = true;
    sync.current.value = 5;
    const wrapper = mount(LibraryTable, {
      props: { libraries: [makeLibrary({ id: 5, status: 'active' })] },
    });
    const btn = wrapper.findAll('[data-testid="library-action"]')[2];
    expect(btn.attributes('disabled')).toBeDefined();
  });

  it('labels the current row "Deep indexing…" during a deep_index run', () => {
    const sync = useSyncStatus();
    sync.running.value = true;
    sync.current.value = 5;
    sync.currentKind.value = 'deep_index';
    const wrapper = mount(LibraryTable, {
      props: {
        libraries: [
          makeLibrary({ id: 5, status: 'active' }),
          makeLibrary({ id: 6, status: 'active' }),
        ],
      },
    });
    const cards = wrapper.findAll('[data-testid="library-card"]');
    expect(cards[0].find('.badge').text()).toBe('Deep indexing…');
    expect(cards[1].find('.badge').text()).toBe('Active');
  });

  it('keeps "Syncing" for a sync run', () => {
    const sync = useSyncStatus();
    sync.running.value = true;
    sync.current.value = 5;
    sync.currentKind.value = 'sync';
    const wrapper = mount(LibraryTable, {
      props: { libraries: [makeLibrary({ id: 5, status: 'active' })] },
    });
    expect(wrapper.find('.badge').text()).toBe('Syncing');
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

  it('renders a determinate progress bar when total is 0', async () => {
    const sync = useSyncStatus();
    sync.running.value = true;
    sync.current.value = 7;
    sync.currentProgress.value = { processed: 0, total: 0 };
    const wrapper = mount(LibraryTable, {
      props: { libraries: [makeLibrary({ id: 7, status: 'syncing' })] },
    });
    const bar = wrapper.find('progress');
    expect(bar.exists()).toBe(true);
    // A determinate bar must have a value attribute (indeterminate bars have none)
    expect(bar.attributes('value')).toBe('0');
  });

  it('shows the unreadable link with the count when N > 0', () => {
    const wrapper = mount(LibraryTable, {
      props: { libraries: [makeLibrary({ id: 5, status: 'active', unreadable_count: 3 })] },
    });
    const link = wrapper.find('[data-testid="library-unreadable"]');
    expect(link.exists()).toBe(true);
    expect(link.text()).toBe('⚠ 3 unreadable');
    expect(link.classes()).toEqual(expect.arrayContaining(['text-warning', 'text-xs']));
  });

  it('hides the unreadable link when the count is 0', () => {
    const wrapper = mount(LibraryTable, {
      props: { libraries: [makeLibrary({ id: 5, status: 'active', unreadable_count: 0 })] },
    });
    expect(wrapper.find('[data-testid="library-unreadable"]').exists()).toBe(false);
  });

  it('selects the library and navigates with unreadable=1 on click', async () => {
    const { libraryId } = useLibrary();
    libraryId.value = null;
    const wrapper = mount(LibraryTable, {
      props: { libraries: [makeLibrary({ id: 5, status: 'active', unreadable_count: 2 })] },
    });
    await wrapper.find('[data-testid="library-unreadable"]').trigger('click');
    expect(libraryId.value).toBe(5);
    expect(push).toHaveBeenCalledWith({ path: '/', query: { unreadable: '1' } });
  });

  it('keeps the unreadable link enabled while the library syncs', () => {
    const sync = useSyncStatus();
    sync.running.value = true;
    sync.current.value = 5;
    const wrapper = mount(LibraryTable, {
      props: { libraries: [makeLibrary({ id: 5, status: 'syncing', unreadable_count: 2 })] },
    });
    const link = wrapper.find('[data-testid="library-unreadable"]');
    expect(link.exists()).toBe(true);
    expect(link.attributes('disabled')).toBeUndefined();
  });
});
