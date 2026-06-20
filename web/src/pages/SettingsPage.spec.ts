import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import {
  createLibrary,
  deleteLibrary,
  fetchLibraries,
  fetchSettings,
  fetchSyncStatus,
  forcePurgeLibrary,
  reactivateLibrary,
  reindexLibrary,
  syncLibrary,
  triggerReindexAll,
  updateLibrary,
  updateSettings,
} from '@/api';
import { useConfirm } from '@/composables/useConfirm';
import { useLibrary } from '@/composables/useLibrary';
import { useSyncStatus } from '@/composables/useSyncStatus';
import { useToast } from '@/composables/useToast';
import SettingsPage from '@/pages/SettingsPage.vue';
import { makeLibrary } from '@/test/factories';

const { route, replace } = vi.hoisted(() => ({
  route: { query: {} as Record<string, string> },
  replace: vi.fn(),
}));
vi.mock('vue-router', () => ({
  useRoute: () => route,
  useRouter: () => ({ replace }),
}));

vi.mock('@/api', () => ({
  fetchSettings: vi.fn(),
  fetchLibraries: vi.fn(),
  updateSettings: vi.fn(),
  createLibrary: vi.fn(),
  updateLibrary: vi.fn(),
  deleteLibrary: vi.fn(),
  syncLibrary: vi.fn(),
  reindexLibrary: vi.fn(),
  fetchSyncStatus: vi.fn(),
  reactivateLibrary: vi.fn(),
  forcePurgeLibrary: vi.fn(),
  triggerReindexAll: vi.fn(),
  fetchStats: vi.fn().mockResolvedValue({
    total_books: 0,
    total_size_bytes: 0,
    authors: 0,
    series: 0,
    libraries: 0,
    formats: {},
    languages: {},
  }),
}));

function mountPage() {
  return mount(SettingsPage);
}

describe('SettingsPage', () => {
  beforeEach(() => {
    route.query = {};
    vi.mocked(fetchSettings).mockResolvedValue({ opds_user: 'reader', opds_pass_set: true });
    vi.mocked(fetchLibraries).mockResolvedValue([makeLibrary({ id: 5, type: 'inpx' })]);
    vi.mocked(updateSettings).mockResolvedValue({ opds_user: 'newuser', opds_pass_set: true });
    vi.mocked(createLibrary).mockResolvedValue(makeLibrary());
    vi.mocked(updateLibrary).mockResolvedValue(makeLibrary());
    vi.mocked(deleteLibrary).mockResolvedValue(undefined);
    vi.mocked(syncLibrary).mockResolvedValue(undefined);
    vi.mocked(reactivateLibrary).mockResolvedValue(undefined);
    vi.mocked(forcePurgeLibrary).mockResolvedValue(undefined);
    vi.mocked(triggerReindexAll).mockResolvedValue(undefined);
    vi.mocked(reindexLibrary).mockResolvedValue(undefined);
    vi.mocked(fetchSyncStatus).mockResolvedValue({ running: false, current: 0, queued: [] });
    const sync = useSyncStatus();
    sync.disconnect();
    sync.running.value = false;
    sync.current.value = 0;
    sync.queued.value = [];
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
    vi.useRealTimers();
  });

  // Switching to the Libraries tab triggers a one-shot refresh so the list is
  // instantly fresh (SSE handles subsequent live updates; no polling needed).
  it('re-fetches the library list once when switching to the libraries tab', async () => {
    // First load on mount returns syncing; switching tabs returns error.
    vi.mocked(fetchLibraries)
      .mockResolvedValueOnce([makeLibrary({ id: 5, status: 'syncing' })])
      .mockResolvedValue([makeLibrary({ id: 5, status: 'error' })]);
    const wrapper = mountPage();
    await flushPromises();

    await wrapper.findAll('[role="tab"]')[1].trigger('click'); // switch to libraries tab
    await flushPromises();

    expect(wrapper.find('[data-testid="library-card"] .badge').text()).toBe('Error');
    wrapper.unmount();
  });

  it('loads OPDS settings on mount', async () => {
    const wrapper = mountPage();
    await flushPromises();
    expect(fetchSettings).toHaveBeenCalledOnce();
    expect((wrapper.find('input[type="text"]').element as HTMLInputElement).value).toBe('reader');
    expect(wrapper.text()).toContain('Status: Set');
  });

  it('toasts when the settings load fails on mount (F2)', async () => {
    vi.mocked(fetchSettings).mockRejectedValueOnce(new Error('api down'));
    mountPage();
    await flushPromises();

    const { toasts } = useToast();
    expect(toasts.value.some((t) => t.message === 'Failed to load settings: api down')).toBe(true);
  });

  it('saves OPDS settings, sending the password only when entered', async () => {
    const wrapper = mountPage();
    await flushPromises();

    await wrapper.find('input[type="text"]').setValue('newuser');
    await wrapper.find('input[type="password"]').setValue('s3cret');
    await wrapper.find('[data-testid="opds-save"]').trigger('click');
    await flushPromises();

    expect(updateSettings).toHaveBeenCalledWith({ opds_user: 'newuser', opds_pass: 's3cret' });
    // Password field is cleared after a successful save.
    expect((wrapper.find('input[type="password"]').element as HTMLInputElement).value).toBe('');
  });

  it('lists libraries on the Libraries tab and triggers a sync', async () => {
    const wrapper = mountPage();
    await flushPromises();

    await wrapper.findAll('[role="tab"]')[1].trigger('click');
    expect(wrapper.findAll('[data-testid="library-card"]')).toHaveLength(1);

    await wrapper.find('[data-testid="library-action"]').trigger('click');
    await flushPromises();
    expect(syncLibrary).toHaveBeenCalledWith(5);
  });

  it('refreshes the shared library list (header combo box) after adding a library', async () => {
    vi.mocked(fetchLibraries)
      .mockResolvedValueOnce([makeLibrary({ id: 5 })]) // onMounted
      .mockResolvedValueOnce([makeLibrary({ id: 5 })]) // tab switch one-shot refresh
      .mockResolvedValue([makeLibrary({ id: 5 }), makeLibrary({ id: 7, name: 'Added' })]);
    const wrapper = mountPage();
    await flushPromises();
    await wrapper.findAll('[role="tab"]')[1].trigger('click');

    const { libraries } = useLibrary();
    expect(libraries.value.map((l) => l.id)).toEqual([5]);

    const textInputs = wrapper.findAll('input[type="text"]');
    await textInputs[0].setValue('Added'); // Name
    await textInputs[1].setValue('/new/path'); // Path
    await wrapper.find('[data-testid="library-save"]').trigger('click');
    await flushPromises();

    expect(createLibrary).toHaveBeenCalled();
    expect(fetchLibraries).toHaveBeenCalledTimes(3);
    // The shared list the header LibrarySelect renders now includes the new one.
    expect(libraries.value.map((l) => l.id)).toContain(7);
    wrapper.unmount();
  });

  it('deletes a library after the confirm modal is accepted', async () => {
    const wrapper = mountPage();
    await flushPromises();
    await wrapper.findAll('[role="tab"]')[1].trigger('click');

    await wrapper.find('[data-testid="library-action"][data-danger]').trigger('click');
    await flushPromises();

    const { state, respond } = useConfirm();
    expect(state.value.open).toBe(true);
    respond(true);
    await flushPromises();
    expect(deleteLibrary).toHaveBeenCalledWith(5);
  });

  it('does not delete when the confirm modal is cancelled', async () => {
    const wrapper = mountPage();
    await flushPromises();
    await wrapper.findAll('[role="tab"]')[1].trigger('click');

    await wrapper.find('[data-testid="library-action"][data-danger]').trigger('click');
    await flushPromises();

    useConfirm().respond(false);
    await flushPromises();
    expect(deleteLibrary).not.toHaveBeenCalled();
  });

  it('supports editing a library and saving changes', async () => {
    const scrollIntoViewMock = vi.fn();
    window.HTMLElement.prototype.scrollIntoView = scrollIntoViewMock;

    const wrapper = mountPage();
    await flushPromises();
    await wrapper.findAll('[role="tab"]')[1].trigger('click');

    const editBtn = wrapper.findAll('[data-testid="library-action"]')[2];
    expect(editBtn.text()).toBe('Edit');
    await editBtn.trigger('click');

    expect(wrapper.find('[data-testid="library-form-title"]').text()).toBe('Edit Library');
    const saveBtn = wrapper.find('[data-testid="library-save"]');
    expect(saveBtn.text()).toBe('Save Changes');

    const inputs = wrapper.findAll('[data-testid="add-library"] input[type="text"]');
    await inputs[0].setValue('Updated Library');
    await inputs[1].setValue('/updated/path');

    await saveBtn.trigger('click');
    await flushPromises();

    expect(updateLibrary).toHaveBeenCalledWith(5, {
      name: 'Updated Library',
      type: 'inpx',
      path: '/updated/path',
      sync_interval_seconds: 3600,
    });

    expect(wrapper.find('[data-testid="library-form-title"]').text()).toBe('Add Library');
    wrapper.unmount();
  });

  it('offers Reactivate (not Sync/Delete) for a pending-purge library', async () => {
    vi.mocked(fetchLibraries).mockResolvedValue([
      makeLibrary({ id: 9, status: 'pending_purge', purge_at: 1700000000 }),
    ]);
    const wrapper = mountPage();
    await flushPromises();
    await wrapper.findAll('[role="tab"]')[1].trigger('click');

    const actions = wrapper.find('[data-testid="library-actions"]');
    expect(actions.text()).toContain('Reactivate');
    expect(actions.text()).not.toContain('Sync Now');
    expect(actions.text()).not.toContain('Delete');

    await actions.find('[data-testid="library-action"]').trigger('click');
    await flushPromises();
    expect(reactivateLibrary).toHaveBeenCalledWith(9);
    wrapper.unmount();
  });

  it('confirms before force-purging a pending-purge library', async () => {
    vi.mocked(fetchLibraries).mockResolvedValue([
      makeLibrary({ id: 9, status: 'pending_purge', purge_at: 1700000000 }),
    ]);
    const wrapper = mountPage();
    await flushPromises();
    await wrapper.findAll('[role="tab"]')[1].trigger('click');

    await wrapper.find('[data-testid="library-action"][data-danger]').trigger('click');
    await flushPromises();

    const { state, respond } = useConfirm();
    expect(state.value.open).toBe(true);
    respond(true);
    await flushPromises();
    expect(forcePurgeLibrary).toHaveBeenCalledWith(9);

    const { toasts } = useToast();
    expect(toasts.value.some((t) => t.message === 'Purging library…')).toBe(true);
    wrapper.unmount();
  });

  it('re-indexes all libraries after the confirm modal is accepted (L10)', async () => {
    const wrapper = mountPage();
    await flushPromises();
    await wrapper.findAll('[role="tab"]')[1].trigger('click');

    await wrapper.find('[data-testid="reindex-all"]').trigger('click');
    await flushPromises();

    const { state, respond } = useConfirm();
    expect(state.value.open).toBe(true);
    respond(true);
    await flushPromises();
    expect(triggerReindexAll).toHaveBeenCalledOnce();
    wrapper.unmount();
  });

  it('does not re-index when the confirm modal is cancelled (L10)', async () => {
    const wrapper = mountPage();
    await flushPromises();
    await wrapper.findAll('[role="tab"]')[1].trigger('click');

    await wrapper.find('[data-testid="reindex-all"]').trigger('click');
    await flushPromises();

    useConfirm().respond(false);
    await flushPromises();
    expect(triggerReindexAll).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  it('shows an empty-state placeholder when there are no libraries', async () => {
    vi.mocked(fetchLibraries).mockResolvedValue([]);
    const wrapper = mountPage();
    await flushPromises();
    await wrapper.findAll('[role="tab"]')[1].trigger('click');

    expect(wrapper.find('[data-testid="library-empty"]').exists()).toBe(true);
    expect(wrapper.text()).toContain('No libraries yet');
    expect(wrapper.findAll('[data-testid="library-card"]')).toHaveLength(0);
  });

  it('hides the empty-state placeholder when libraries exist', async () => {
    const wrapper = mountPage();
    await flushPromises();
    await wrapper.findAll('[role="tab"]')[1].trigger('click');

    expect(wrapper.find('[data-testid="library-empty"]').exists()).toBe(false);
    expect(wrapper.findAll('[data-testid="library-card"]')).toHaveLength(1);
  });

  it('disables every action for the library that is syncing only', async () => {
    vi.mocked(fetchLibraries).mockResolvedValue([
      makeLibrary({ id: 5, status: 'syncing' }),
      makeLibrary({ id: 6, status: 'active' }),
    ]);
    const wrapper = mountPage();
    await flushPromises();
    await wrapper.findAll('[role="tab"]')[1].trigger('click');

    const libraryCards = wrapper.findAll('[data-testid="library-card"]');
    expect(libraryCards).toHaveLength(2);

    // Card 0 (syncing): Sync Now, Re-index, Edit, Delete all disabled.
    const card0Buttons = libraryCards[0].findAll('[data-testid="library-action"]');
    expect(card0Buttons).toHaveLength(4);
    card0Buttons.forEach((b) => expect(b.attributes('disabled')).toBeDefined());

    // Card 1 (active): all enabled.
    const card1Buttons = libraryCards[1].findAll('[data-testid="library-action"]');
    expect(card1Buttons).toHaveLength(4);
    card1Buttons.forEach((b) => expect(b.attributes('disabled')).toBeUndefined());

    wrapper.unmount();
  });

  it('re-indexes a single library after the confirm modal is accepted', async () => {
    const wrapper = mountPage();
    await flushPromises();
    await wrapper.findAll('[role="tab"]')[1].trigger('click');

    // [0] Sync Now, [1] Re-index, [2] Edit, [3] Delete
    const reindexBtn = wrapper.findAll('[data-testid="library-action"]')[1];
    expect(reindexBtn.text()).toBe('Re-index');
    await reindexBtn.trigger('click');
    await flushPromises();

    const { state, respond } = useConfirm();
    expect(state.value.open).toBe(true);
    respond(true);
    await flushPromises();
    expect(reindexLibrary).toHaveBeenCalledWith(5);
  });

  it('does not re-index a single library when the confirm modal is cancelled', async () => {
    const wrapper = mountPage();
    await flushPromises();
    await wrapper.findAll('[role="tab"]')[1].trigger('click');

    await wrapper.findAll('[data-testid="library-action"]')[1].trigger('click');
    await flushPromises();
    useConfirm().respond(false);
    await flushPromises();
    expect(reindexLibrary).not.toHaveBeenCalled();
  });

  it('triggers Sync Now without a confirm modal', async () => {
    const wrapper = mountPage();
    await flushPromises();
    await wrapper.findAll('[role="tab"]')[1].trigger('click');

    await wrapper.findAll('[data-testid="library-action"]')[0].trigger('click');
    await flushPromises();
    expect(syncLibrary).toHaveBeenCalledWith(5);
    expect(useConfirm().state.value.open).toBe(false);
  });

  it('disables Re-index All while a sync is running', async () => {
    useSyncStatus().running.value = true;
    const wrapper = mountPage();
    await flushPromises();
    await wrapper.findAll('[role="tab"]')[1].trigger('click');

    expect(wrapper.find('[data-testid="reindex-all"]').attributes('disabled')).toBeDefined();
    wrapper.unmount();
  });

  it('shows a queued badge for a library waiting in the engine queue', async () => {
    vi.mocked(fetchLibraries).mockResolvedValue([makeLibrary({ id: 6, status: 'active' })]);
    const sync = useSyncStatus();
    sync.running.value = true;
    sync.current.value = 5;
    sync.queued.value = [6];

    const wrapper = mountPage();
    await flushPromises();
    await wrapper.findAll('[role="tab"]')[1].trigger('click');

    expect(wrapper.find('[data-testid="library-card"]').text()).toContain('Queued');
    wrapper.unmount();
  });

  it('renders badge-ghost class and "Queued" label for a library with status queued (L5)', async () => {
    vi.mocked(fetchLibraries).mockResolvedValue([makeLibrary({ id: 7, status: 'queued' })]);

    const wrapper = mountPage();
    await flushPromises();
    await wrapper.findAll('[role="tab"]')[1].trigger('click');

    const badge = wrapper.find('[data-testid="library-card"] .badge');
    expect(badge.classes()).toContain('badge-ghost');
    expect(badge.text()).toBe('Queued');
    wrapper.unmount();
  });

  it('renders a determinate progress bar and label while a library is syncing', async () => {
    const sync = useSyncStatus();
    sync.running.value = true;
    sync.current.value = 5;
    sync.currentProgress.value = { processed: 1200, total: 5000 };

    const wrapper = mountPage();
    await flushPromises();
    await wrapper.findAll('[role="tab"]')[1].trigger('click');

    const card = wrapper.find('[data-testid="library-card"]');
    const bar = card.find('progress');
    expect(bar.exists()).toBe(true);
    expect(bar.attributes('value')).toBe('1200');
    expect(bar.attributes('max')).toBe('5000');
    expect(card.text()).toContain('1,200 / 5,000');

    sync.currentProgress.value = null;
    wrapper.unmount();
  });
});
