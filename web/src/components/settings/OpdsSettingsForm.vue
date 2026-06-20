<template>
  <div class="card bg-base-200 max-w-xl gap-4 p-5">
    <div>
      <label for="opds-user" class="mb-1 block text-sm font-semibold">OPDS Username</label>
      <input
        id="opds-user"
        v-model="opdsUser"
        type="text"
        class="input w-full"
        placeholder="reader"
      />
    </div>
    <div>
      <label for="opds-pass" class="mb-1 block text-sm font-semibold">OPDS Password</label>
      <input
        id="opds-pass"
        v-model="opdsPass"
        type="password"
        class="input w-full"
        placeholder="Enter new password"
      />
      <span class="text-base-content/60 mt-1 block text-xs">
        Status: {{ opdsPassSet ? 'Set' : 'Not set' }}
      </span>
    </div>
    <div>
      <button type="button" data-testid="opds-save" class="btn btn-primary" @click="save">
        Save
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue';

import { fetchSettings, updateSettings } from '@/api';
import { useToast } from '@/composables/useToast';

const toast = useToast();

const opdsUser = ref('');
const opdsPass = ref('');
const opdsPassSet = ref(false);

async function load(): Promise<void> {
  const settings = await fetchSettings();
  opdsUser.value = settings.opds_user;
  opdsPassSet.value = settings.opds_pass_set;
}

async function save(): Promise<void> {
  try {
    const update: { opds_user: string; opds_pass?: string } = { opds_user: opdsUser.value };
    if (opdsPass.value) update.opds_pass = opdsPass.value;
    const settings = await updateSettings(update);
    opdsUser.value = settings.opds_user;
    opdsPassSet.value = settings.opds_pass_set;
    opdsPass.value = '';
  } catch (err) {
    toast.error(`Failed to save settings: ${(err as Error).message}`);
  }
}

// Self-load on mount. The toast message matches the page's previous combined
// load-error string, so existing coverage stays valid.
onMounted(async () => {
  try {
    await load();
  } catch (err) {
    toast.error(`Failed to load settings: ${(err as Error).message}`);
  }
});
</script>
