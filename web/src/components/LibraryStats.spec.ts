import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { fetchStats } from '@/api';
import LibraryStats from '@/components/LibraryStats.vue';
import { useLibrary } from '@/composables/useLibrary';
import { makeLibrary } from '@/test/factories';
import type { Stats } from '@/types';

vi.mock('@/api', () => ({ fetchStats: vi.fn(), fetchLibraries: vi.fn() }));
const toastError = vi.fn();
vi.mock('@/composables/useToast', () => ({
  useToast: () => ({
    error: toastError,
    success: vi.fn(),
    toasts: { value: [] },
    dismiss: vi.fn(),
  }),
}));

function makeStats(overrides: Partial<Stats> = {}): Stats {
  return {
    total_books: 0,
    total_size_bytes: 0,
    authors: 0,
    series: 0,
    libraries: 0,
    formats: {},
    languages: {},
    ...overrides,
  };
}

describe('LibraryStats', () => {
  // The component subscribes to the shared library list, so a mount left alive
  // between tests would keep refetching when later tests mutate that singleton.
  enableAutoUnmount(afterEach);

  beforeEach(() => {
    vi.mocked(fetchStats).mockReset();
    toastError.mockClear();
    // Reset the module-level shared library list between tests.
    useLibrary().libraries.value = [];
  });

  it('fetches global totals and renders them with a formats breakdown', async () => {
    vi.mocked(fetchStats).mockResolvedValue({
      total_books: 12403,
      total_size_bytes: 3 * 1024 * 1024 * 1024,
      authors: 3210,
      series: 142,
      libraries: 2,
      formats: { fb2: 840, epub: 1203 },
      languages: { en: 1500 },
    });
    const wrapper = mount(LibraryStats);
    await flushPromises();

    expect(fetchStats).toHaveBeenCalledWith(); // no library => global
    expect(wrapper.find('[data-testid="stat-books"]').text()).toContain('12,403');
    expect(wrapper.text()).toContain('GB');
    // Formats are listed highest-count first.
    expect(wrapper.find('[data-testid="stat-formats"]').text()).toMatch(/epub 1,203.*fb2 840/);
  });

  it('refetches totals when the library set changes (add or background sync)', async () => {
    vi.mocked(fetchStats)
      .mockResolvedValueOnce(makeStats({ total_books: 0 }))
      .mockResolvedValueOnce(makeStats({ total_books: 1500 }));

    const { libraries } = useLibrary();
    const wrapper = mount(LibraryStats);
    await flushPromises();

    expect(fetchStats).toHaveBeenCalledTimes(1);
    expect(wrapper.find('[data-testid="stat-books"]').text()).toContain('0');

    // A library is added and the background sync indexes books; SettingsPage's
    // poll updates the shared list, which must drive a fresh totals fetch so the
    // header stays in step with the table below it.
    libraries.value = [makeLibrary({ book_count: 1500 })];
    await flushPromises();

    expect(fetchStats).toHaveBeenCalledTimes(2);
    expect(wrapper.find('[data-testid="stat-books"]').text()).toContain('1,500');
  });

  it('toasts and renders no card body on failure', async () => {
    vi.mocked(fetchStats).mockRejectedValue(new Error('boom'));
    const wrapper = mount(LibraryStats);
    await flushPromises();
    expect(toastError).toHaveBeenCalledWith(expect.stringContaining('boom'));
    expect(wrapper.find('[data-testid="stat-books"]').exists()).toBe(false);
  });
});
