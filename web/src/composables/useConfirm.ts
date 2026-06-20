import { ref } from 'vue';

export interface ConfirmOptions {
  title: string;
  body: string;
  confirmLabel?: string;
  cancelLabel?: string;
  danger?: boolean;
}

interface ConfirmState extends ConfirmOptions {
  open: boolean;
}

const state = ref<ConfirmState>({ open: false, title: '', body: '' });
let resolver: ((value: boolean) => void) | null = null;

export function useConfirm() {
  function confirm(options: ConfirmOptions): Promise<boolean> {
    // Resolve any still-pending confirm as false before replacing it, so an
    // awaiter from a previous modal doesn't hang forever when a second opens.
    resolver?.(false);
    state.value = { ...options, open: true };
    return new Promise<boolean>((resolve) => {
      resolver = resolve;
    });
  }

  function respond(value: boolean): void {
    state.value = { ...state.value, open: false };
    resolver?.(value);
    resolver = null;
  }

  return { state, confirm, respond };
}
