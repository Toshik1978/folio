<template>
  <fieldset class="card bg-base-200 max-w-xl gap-4 p-5" :disabled="locked">
    <label class="flex cursor-pointer items-center gap-3">
      <input
        data-testid="tg-enabled"
        type="checkbox"
        class="toggle toggle-primary"
        :checked="enabled"
        @change="onToggle"
      />
      <span class="text-sm font-semibold">Send notifications to Telegram</span>
    </label>
    <p v-if="error" data-testid="tg-error" class="text-error text-sm" role="alert">{{ error }}</p>
    <div>
      <label for="tg-token" class="mb-1 block text-sm font-semibold">Bot token</label>
      <input
        id="tg-token"
        v-model="botToken"
        data-testid="tg-token"
        type="password"
        autocomplete="new-password"
        data-1p-ignore
        data-lpignore="true"
        class="input w-full"
        :placeholder="tokenPlaceholder"
      />
    </div>
    <div>
      <label for="tg-chat" class="mb-1 block text-sm font-semibold">Channel ID</label>
      <input
        id="tg-chat"
        v-model="chatId"
        data-testid="tg-chat"
        type="text"
        class="input w-full"
        placeholder="@channel or -100…"
      />
    </div>
    <div>
      <label for="tg-topic" class="mb-1 block text-sm font-semibold">Topic ID (optional)</label>
      <input
        id="tg-topic"
        ref="topicEl"
        v-model="topicId"
        data-testid="tg-topic"
        type="number"
        min="1"
        step="1"
        class="input w-full"
      />
    </div>
    <label class="flex cursor-pointer items-center gap-2">
      <input v-model="silent" data-testid="tg-silent" type="checkbox" class="checkbox" />
      <span class="text-sm">Send without sound</span>
    </label>
    <div class="flex flex-wrap gap-2">
      <button
        type="button"
        data-testid="tg-save"
        class="btn btn-primary"
        :disabled="locked"
        @click="save"
      >
        Save
      </button>
      <button
        type="button"
        data-testid="tg-test"
        class="btn"
        :disabled="locked || !savedComplete || dirty"
        :title="testTitle"
        @click="sendTest"
      >
        Send test message
      </button>
    </div>
    <p
      v-if="loaded"
      data-testid="tg-status"
      class="text-xs"
      :class="failureIsLatest ? 'text-warning' : 'text-base-content/60'"
    >
      {{ statusText }}
    </p>
  </fieldset>
  <!-- outside the fieldset: it stays disabled until the settings load -->
  <div v-if="loadError" data-testid="tg-load-error" class="mt-3 flex max-w-xl items-center gap-2">
    <span class="text-error text-sm" role="alert">Couldn't load Telegram settings.</span>
    <button type="button" data-testid="tg-retry" class="btn btn-sm" @click="load">Retry</button>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, useTemplateRef } from 'vue';

import {
  fetchTelegramSettings,
  fetchTelegramStatus,
  sendTelegramTest,
  updateTelegramSettings,
} from '@/api';
import { useToast } from '@/composables/useToast';
import type { TelegramDeliveryStatus, TelegramSettings, TelegramSettingsUpdate } from '@/types';

const toast = useToast();

const enabled = ref(false);
const botToken = ref('');
const chatId = ref('');
const topicId = ref<number | string | null>(null);
const silent = ref(false);
const tokenSet = ref(false);
const tokenLast4 = ref('');
// Completeness of the settings as last saved on the server; gates the test button.
const savedComplete = ref(false);
const status = ref<TelegramDeliveryStatus>({
  last_success_at: null,
  last_failure_at: null,
  last_failure_error: '',
});
const error = ref('');
const busy = ref(false);
// loaded flips once the stored settings arrive; until then the fields hold
// defaults, and saving them would overwrite the stored chat and topic.
const loaded = ref(false);
const loadError = ref(false);
const locked = computed(() => busy.value || !loaded.value);
const topicEl = useTemplateRef<HTMLInputElement>('topicEl');

// The fields as last loaded or saved; the test message is sent with the
// stored settings, so it is held back while the form has unsaved edits.
const saved = ref({ enabled: false, chatId: '', topic: null as number | null, silent: false });
const dirty = computed(
  () =>
    botToken.value.trim() !== '' ||
    enabled.value !== saved.value.enabled ||
    chatId.value.trim() !== saved.value.chatId ||
    parseTopic() !== saved.value.topic ||
    silent.value !== saved.value.silent,
);
const testTitle = computed(() => {
  if (!savedComplete.value) return 'Save a bot token and a channel ID first';
  return dirty.value ? 'Save your changes first' : '';
});

const tokenPlaceholder = computed(() => {
  if (!tokenSet.value) return 'Bot token from @BotFather';
  return tokenLast4.value ? `Stored (ends with ${tokenLast4.value})` : 'Stored';
});

function formatTime(iso: string): string {
  return new Date(iso).toLocaleString();
}

const failureIsLatest = computed(() => {
  const { last_success_at: ok, last_failure_at: fail } = status.value;
  if (!fail) return false;
  return !ok || new Date(fail).getTime() > new Date(ok).getTime();
});

const statusText = computed(() => {
  const { last_success_at: ok, last_failure_at: fail, last_failure_error: err } = status.value;
  if (!ok && !fail) return 'No messages sent since the server started.';
  const delivered = ok ? `Last delivered ${formatTime(ok)}.` : '';
  if (failureIsLatest.value && fail) {
    const failed = `Last delivery failed ${formatTime(fail)}: ${err}`;
    return delivered ? `${failed} ${delivered}` : failed;
  }
  return delivered;
});

function apply(s: TelegramSettings): void {
  enabled.value = s.enabled;
  tokenSet.value = s.token_set;
  tokenLast4.value = s.token_last4;
  chatId.value = s.chat_id;
  topicId.value = s.topic_id;
  silent.value = s.silent;
  status.value = s.status;
  savedComplete.value = s.token_set && s.chat_id !== '';
  botToken.value = '';
  saved.value = { enabled: s.enabled, chatId: s.chat_id, topic: s.topic_id, silent: s.silent };
}

function onToggle(e: Event): void {
  const box = e.target as HTMLInputElement;
  if (
    (box.checked && !(tokenSet.value || botToken.value)) ||
    (box.checked && !chatId.value.trim())
  ) {
    error.value = 'A bot token and a channel ID are required.';
    box.checked = false;
    enabled.value = false;
    return;
  }
  error.value = '';
  enabled.value = box.checked;
}

// parseTopic returns undefined when the field holds something other than a
// positive integer, so the form can say so instead of the server rejecting
// the body as invalid JSON (topic_id is an int64).
function parseTopic(): number | null | undefined {
  // An unparseable number input (e.g. "12-") reads back as "", which would
  // otherwise save as "no topic"; badInput tells the two apart.
  if (topicEl.value?.validity.badInput) return undefined;
  const raw = String(topicId.value ?? '').trim();
  if (raw === '') return null;
  const n = Number(raw);
  return Number.isInteger(n) && n > 0 ? n : undefined;
}

async function save(): Promise<void> {
  error.value = '';
  const topic = parseTopic();
  if (topic === undefined) {
    error.value = 'Topic ID must be a positive integer.';
    return;
  }
  busy.value = true;
  try {
    const update: TelegramSettingsUpdate = {
      enabled: enabled.value,
      chat_id: chatId.value.trim(),
      topic_id: topic,
      silent: silent.value,
    };
    const token = botToken.value.trim();
    if (token) update.bot_token = token;
    apply(await updateTelegramSettings(update));
    toast.success('Telegram settings saved');
  } catch (err) {
    if ((err as { status?: number }).status === 400) {
      error.value = (err as Error).message;
    } else {
      toast.error(`Failed to save Telegram settings: ${(err as Error).message}`);
    }
  } finally {
    busy.value = false;
  }
}

async function sendTest(): Promise<void> {
  busy.value = true;
  try {
    status.value = (await sendTelegramTest()).status;
    toast.success('Test message sent');
  } catch (err) {
    toast.error(`Test message failed: ${(err as Error).message}`);
    // A failed send rejects without its body, so re-read the status line.
    try {
      status.value = await fetchTelegramStatus();
    } catch {
      // the toast above already reported the outcome; keep the previous status line
    }
  } finally {
    busy.value = false;
  }
}

async function load(): Promise<void> {
  loadError.value = false;
  try {
    apply(await fetchTelegramSettings());
    loaded.value = true;
  } catch (err) {
    loadError.value = true;
    toast.error(`Failed to load Telegram settings: ${(err as Error).message}`);
  }
}

onMounted(load);
</script>
