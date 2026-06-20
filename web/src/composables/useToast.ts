import { ref } from 'vue';

export type ToastType = 'success' | 'error';

export interface Toast {
  id: number;
  type: ToastType;
  message: string;
}

const TOAST_TTL = 4000;
const toasts = ref<Toast[]>([]);
let nextId = 1;

function dismiss(id: number): void {
  toasts.value = toasts.value.filter((t) => t.id !== id);
}

function push(type: ToastType, message: string): void {
  const id = nextId++;
  toasts.value.push({ id, type, message });
  setTimeout(() => dismiss(id), TOAST_TTL);
}

export function useToast() {
  return {
    toasts,
    dismiss,
    success: (message: string) => push('success', message),
    error: (message: string) => push('error', message),
  };
}
