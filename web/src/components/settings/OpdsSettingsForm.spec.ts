import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { fetchSettings, updateSettings } from '@/api';
import OpdsSettingsForm from '@/components/settings/OpdsSettingsForm.vue';
import { useToast } from '@/composables/useToast';

vi.mock('@/api', () => ({
  fetchSettings: vi.fn(),
  updateSettings: vi.fn(),
}));

describe('OpdsSettingsForm', () => {
  beforeEach(() => {
    vi.mocked(fetchSettings).mockResolvedValue({ opds_user: 'reader', opds_pass_set: true });
    vi.mocked(updateSettings).mockResolvedValue({ opds_user: 'newuser', opds_pass_set: true });
  });
  afterEach(() => vi.clearAllMocks());

  it('loads settings on mount and shows the status', async () => {
    const wrapper = mount(OpdsSettingsForm);
    await flushPromises();
    expect((wrapper.find('input[type="text"]').element as HTMLInputElement).value).toBe('reader');
    expect(wrapper.text()).toContain('Status: Set');
  });

  it('saves, sending the password only when entered, and clears it', async () => {
    const wrapper = mount(OpdsSettingsForm);
    await flushPromises();
    await wrapper.find('input[type="text"]').setValue('newuser');
    await wrapper.find('input[type="password"]').setValue('s3cret');
    await wrapper.find('[data-testid="opds-save"]').trigger('click');
    await flushPromises();
    expect(updateSettings).toHaveBeenCalledWith({ opds_user: 'newuser', opds_pass: 's3cret' });
    expect((wrapper.find('input[type="password"]').element as HTMLInputElement).value).toBe('');
  });

  it('toasts when the load fails', async () => {
    vi.mocked(fetchSettings).mockRejectedValueOnce(new Error('api down'));
    mount(OpdsSettingsForm);
    await flushPromises();
    expect(
      useToast().toasts.value.some((t) => t.message === 'Failed to load settings: api down'),
    ).toBe(true);
  });
});
