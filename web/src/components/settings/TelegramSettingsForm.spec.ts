import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import {
  fetchTelegramSettings,
  fetchTelegramStatus,
  sendTelegramTest,
  updateTelegramSettings,
} from '@/api';
import TelegramSettingsForm from '@/components/settings/TelegramSettingsForm.vue';
import { useToast } from '@/composables/useToast';
import type { TelegramSettings } from '@/types';

vi.mock('@/api', () => ({
  fetchTelegramSettings: vi.fn(),
  updateTelegramSettings: vi.fn(),
  sendTelegramTest: vi.fn(),
  fetchTelegramStatus: vi.fn(),
}));

const SENTINEL = '123456:TEST-token_SENTINEL';

function settings(over: Partial<TelegramSettings> = {}): TelegramSettings {
  return {
    enabled: true,
    token_set: true,
    token_last4: 'ABCD',
    chat_id: '@test_channel',
    topic_id: null,
    silent: false,
    status: { last_success_at: null, last_failure_at: null, last_failure_error: '' },
    ...over,
  };
}

async function mountForm() {
  const wrapper = mount(TelegramSettingsForm);
  await flushPromises();
  return wrapper;
}

const input = (w: Awaited<ReturnType<typeof mountForm>>, id: string) =>
  w.find(`[data-testid="${id}"]`);
const toastMessages = () => useToast().toasts.value.map((t) => t.message);

describe('TelegramSettingsForm', () => {
  beforeEach(() => {
    vi.mocked(fetchTelegramSettings).mockResolvedValue(settings());
    vi.mocked(updateTelegramSettings).mockResolvedValue(settings());
  });
  afterEach(() => {
    vi.clearAllMocks();
    useToast().toasts.value = [];
  });

  it('renders the masked token placeholder and never a token value', async () => {
    const w = await mountForm();
    const token = input(w, 'tg-token');
    expect(token.attributes('placeholder')).toBe('Stored (ends with ABCD)');
    expect(token.attributes('type')).toBe('password');
    expect(token.attributes('autocomplete')).toBe('new-password');
    expect((token.element as HTMLInputElement).value).toBe('');
    expect(w.html()).not.toContain(SENTINEL);
    expect((input(w, 'tg-chat').element as HTMLInputElement).value).toBe('@test_channel');
  });

  it('uses the BotFather placeholder when no token is stored', async () => {
    vi.mocked(fetchTelegramSettings).mockResolvedValue(
      settings({ token_set: false, token_last4: '' }),
    );
    const w = await mountForm();
    expect(input(w, 'tg-token').attributes('placeholder')).toBe('Bot token from @BotFather');
  });

  it('shows Stored without the suffix when a token is stored but its last4 is empty', async () => {
    vi.mocked(fetchTelegramSettings).mockResolvedValue(settings({ token_last4: '' }));
    const w = await mountForm();
    expect(input(w, 'tg-token').attributes('placeholder')).toBe('Stored');
  });

  it('saves without bot_token when the field is empty', async () => {
    const w = await mountForm();
    await input(w, 'tg-topic').setValue('7');
    await input(w, 'tg-silent').setValue(true);
    await input(w, 'tg-save').trigger('click');
    await flushPromises();
    expect(updateTelegramSettings).toHaveBeenCalledWith({
      enabled: true,
      chat_id: '@test_channel',
      topic_id: 7,
      silent: true,
    });
    expect(toastMessages()).toContain('Telegram settings saved');
  });

  it('sends an empty topic as null', async () => {
    const w = await mountForm();
    await input(w, 'tg-save').trigger('click');
    await flushPromises();
    expect(vi.mocked(updateTelegramSettings).mock.calls[0][0].topic_id).toBeNull();
  });

  it('trims the channel ID and token before saving', async () => {
    const w = await mountForm();
    await input(w, 'tg-chat').setValue('  @test_channel ');
    await input(w, 'tg-token').setValue(` ${SENTINEL} `);
    await input(w, 'tg-save').trigger('click');
    await flushPromises();
    const sent = vi.mocked(updateTelegramSettings).mock.calls[0][0];
    expect(sent.chat_id).toBe('@test_channel');
    expect(sent.bot_token).toBe(SENTINEL);
  });

  it('saves with bot_token when typed and clears the field afterwards', async () => {
    const w = await mountForm();
    await input(w, 'tg-token').setValue(SENTINEL);
    await input(w, 'tg-save').trigger('click');
    await flushPromises();
    expect(updateTelegramSettings).toHaveBeenCalledWith(
      expect.objectContaining({ bot_token: SENTINEL }),
    );
    expect((input(w, 'tg-token').element as HTMLInputElement).value).toBe('');
    expect(w.html()).not.toContain(SENTINEL);
  });

  it('refuses to switch on without a token or channel, without a request', async () => {
    vi.mocked(fetchTelegramSettings).mockResolvedValue(
      settings({ enabled: false, token_set: false, token_last4: '', chat_id: '' }),
    );
    const w = await mountForm();
    await input(w, 'tg-enabled').setValue(true);
    await flushPromises();
    expect(input(w, 'tg-error').text()).toBe('A bot token and a channel ID are required.');
    expect((input(w, 'tg-enabled').element as HTMLInputElement).checked).toBe(false);
    expect(updateTelegramSettings).not.toHaveBeenCalled();
  });

  it('allows switching on once a token is typed and a channel is set', async () => {
    vi.mocked(fetchTelegramSettings).mockResolvedValue(
      settings({ enabled: false, token_set: false, token_last4: '' }),
    );
    const w = await mountForm();
    await input(w, 'tg-token').setValue(SENTINEL);
    await input(w, 'tg-enabled').setValue(true);
    await flushPromises();
    expect(w.find('[data-testid="tg-error"]').exists()).toBe(false);
    expect((input(w, 'tg-enabled').element as HTMLInputElement).checked).toBe(true);
  });

  it('shows a server 400 inline', async () => {
    vi.mocked(updateTelegramSettings).mockRejectedValueOnce(
      Object.assign(new Error('chat_id is invalid'), { status: 400 }),
    );
    const w = await mountForm();
    await input(w, 'tg-save').trigger('click');
    await flushPromises();
    expect(input(w, 'tg-error').text()).toBe('chat_id is invalid');
    expect(toastMessages()).not.toContain('Telegram settings saved');
  });

  it('toasts other save errors', async () => {
    vi.mocked(updateTelegramSettings).mockRejectedValueOnce(new Error('boom'));
    const w = await mountForm();
    await input(w, 'tg-save').trigger('click');
    await flushPromises();
    expect(toastMessages()).toContain('Failed to save Telegram settings: boom');
    expect(w.find('[data-testid="tg-error"]').exists()).toBe(false);
  });

  it('toasts when the load fails', async () => {
    vi.mocked(fetchTelegramSettings).mockRejectedValueOnce(new Error('api down'));
    await mountForm();
    expect(toastMessages()).toContain('Failed to load Telegram settings: api down');
  });

  it('keeps the form locked when the load fails, so defaults cannot overwrite the stored settings', async () => {
    vi.mocked(fetchTelegramSettings).mockRejectedValueOnce(new Error('api down'));
    const w = await mountForm();
    expect(input(w, 'tg-save').attributes('disabled')).toBeDefined();
    await input(w, 'tg-save').trigger('click');
    await flushPromises();
    expect(updateTelegramSettings).not.toHaveBeenCalled();
  });

  it('keeps the form locked until the settings have loaded', async () => {
    let resolve!: (s: TelegramSettings) => void;
    vi.mocked(fetchTelegramSettings).mockReturnValueOnce(new Promise((r) => (resolve = r)));
    const w = mount(TelegramSettingsForm);
    expect(input(w, 'tg-save').attributes('disabled')).toBeDefined();
    resolve(settings());
    await flushPromises();
    expect(input(w, 'tg-save').attributes('disabled')).toBeUndefined();
  });

  it('rejects a non-integer topic id before sending', async () => {
    const w = await mountForm();
    await input(w, 'tg-topic').setValue('12.5');
    await input(w, 'tg-save').trigger('click');
    await flushPromises();
    expect(updateTelegramSettings).not.toHaveBeenCalled();
    expect(input(w, 'tg-error').text()).toBe('Topic ID must be a positive integer.');
  });

  it('disables the test button when the saved settings are incomplete', async () => {
    vi.mocked(fetchTelegramSettings).mockResolvedValue(settings({ token_set: false }));
    const w = await mountForm();
    const btn = input(w, 'tg-test');
    expect(btn.attributes('disabled')).toBeDefined();
    expect(btn.attributes('title')).toMatch(/save/i);
  });

  it('offers a retry after a failed load and unlocks once it succeeds', async () => {
    vi.mocked(fetchTelegramSettings).mockRejectedValueOnce(new Error('api down'));
    const w = await mountForm();
    expect(input(w, 'tg-status').exists()).toBe(false);
    const retry = input(w, 'tg-retry');
    expect(retry.attributes('disabled')).toBeUndefined();
    await retry.trigger('click');
    await flushPromises();
    expect(input(w, 'tg-load-error').exists()).toBe(false);
    expect(input(w, 'tg-save').attributes('disabled')).toBeUndefined();
    expect(input(w, 'tg-status').exists()).toBe(true);
  });

  it('rejects a topic id the browser could not parse instead of clearing it', async () => {
    const w = await mountForm();
    const el = input(w, 'tg-topic').element as HTMLInputElement;
    Object.defineProperty(el, 'validity', { value: { badInput: true } });
    await input(w, 'tg-save').trigger('click');
    await flushPromises();
    expect(updateTelegramSettings).not.toHaveBeenCalled();
    expect(input(w, 'tg-error').text()).toBe('Topic ID must be a positive integer.');
  });

  it('holds the test button back while the form has unsaved edits', async () => {
    const w = await mountForm();
    await input(w, 'tg-chat').setValue('@other_channel');
    const btn = input(w, 'tg-test');
    expect(btn.attributes('disabled')).toBeDefined();
    expect(btn.attributes('title')).toBe('Save your changes first');
    await input(w, 'tg-chat').setValue('@test_channel');
    expect(input(w, 'tg-test').attributes('disabled')).toBeUndefined();
  });

  it('enables the test button for complete saved settings', async () => {
    const w = await mountForm();
    expect(input(w, 'tg-test').attributes('disabled')).toBeUndefined();
  });

  it('updates the status line from the response after a successful test', async () => {
    vi.mocked(sendTelegramTest).mockResolvedValue({
      ok: true,
      status: {
        last_success_at: '2026-01-02T03:04:05Z',
        last_failure_at: null,
        last_failure_error: '',
      },
    });
    const w = await mountForm();
    await input(w, 'tg-test').trigger('click');
    await flushPromises();
    expect(toastMessages()).toContain('Test message sent');
    expect(fetchTelegramStatus).not.toHaveBeenCalled();
    expect(input(w, 'tg-status').text()).toMatch(/^Last delivered .+\.$/);
  });

  it('updates the status line and toasts after a failed test', async () => {
    vi.mocked(sendTelegramTest).mockRejectedValueOnce(new Error('chat not found'));
    vi.mocked(fetchTelegramStatus).mockResolvedValue({
      last_success_at: null,
      last_failure_at: '2026-01-02T03:04:05Z',
      last_failure_error: 'chat not found',
    });
    const w = await mountForm();
    await input(w, 'tg-test').trigger('click');
    await flushPromises();
    expect(toastMessages()).toContain('Test message failed: chat not found');
    expect(input(w, 'tg-status').text()).toMatch(/^Last delivery failed .+: chat not found$/);
  });

  describe('status line', () => {
    const base = { last_success_at: null, last_failure_at: null, last_failure_error: '' };
    async function withStatus(status: TelegramSettings['status']) {
      vi.mocked(fetchTelegramSettings).mockResolvedValue(settings({ status }));
      return mountForm();
    }

    it('shows the empty state', async () => {
      const w = await withStatus(base);
      expect(input(w, 'tg-status').text()).toBe('No messages sent since the server started.');
    });

    it('shows success only', async () => {
      const w = await withStatus({ ...base, last_success_at: '2026-01-02T03:04:05Z' });
      const text = input(w, 'tg-status').text();
      expect(text).toMatch(/^Last delivered .+\.$/);
      expect(text).not.toContain('failed');
    });

    it('shows success when it is later than the failure', async () => {
      const w = await withStatus({
        last_success_at: '2026-01-03T00:00:00Z',
        last_failure_at: '2026-01-02T00:00:00Z',
        last_failure_error: 'old',
      });
      const text = input(w, 'tg-status').text();
      expect(text).toMatch(/^Last delivered /);
      expect(text).not.toContain('old');
    });

    it('shows failure only in the warning colour', async () => {
      const w = await withStatus({
        ...base,
        last_failure_at: '2026-01-02T03:04:05Z',
        last_failure_error: 'chat not found',
      });
      const el = input(w, 'tg-status');
      expect(el.text()).toMatch(/^Last delivery failed .+: chat not found$/);
      expect(el.classes()).toContain('text-warning');
    });

    it('shows a newer failure plus the earlier success', async () => {
      const w = await withStatus({
        last_success_at: '2026-01-01T00:00:00Z',
        last_failure_at: '2026-01-02T00:00:00Z',
        last_failure_error: 'chat not found',
      });
      const text = input(w, 'tg-status').text();
      expect(text).toMatch(/Last delivery failed .+: chat not found/);
      expect(text).toMatch(/Last delivered .+\./);
    });
  });
});
