import { mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('@/api', () => ({ fetchLibraries: vi.fn().mockResolvedValue([]) }));

import { useLibrary } from '@/composables/useLibrary';
import { makeLibrary } from '@/test/factories';

import LibrarySelect from './LibrarySelect.vue';

describe('LibrarySelect', () => {
  beforeEach(() => {
    localStorage.clear();
    const { setLibrary, libraries } = useLibrary();
    setLibrary(null);
    libraries.value = [];
  });

  it('renders All plus each library and updates selection on change', async () => {
    const { libraries, libraryId } = useLibrary();
    libraries.value = [makeLibrary({ id: 1, name: 'Calibre' })];

    const wrapper = mount(LibrarySelect);
    const options = wrapper.findAll('option');
    expect(options[0].text()).toBe('All Libraries');
    expect(options[1].text()).toBe('Calibre');

    await wrapper.find('select').setValue('1');
    expect(libraryId.value).toBe(1);

    await wrapper.find('select').setValue('');
    expect(libraryId.value).toBeNull();
  });
});
