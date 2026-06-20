import type { Ref } from 'vue';
import { nextTick, onUnmounted, watch } from 'vue';

// Open modals, in stacking order. Only the top-most one responds to Escape, so
// a confirm dialog layered above another modal doesn't close both at once.
const modalStack: symbol[] = [];

const FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), ' +
  'textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

// useModalFocus gives a class-toggled (non-native) modal the keyboard behavior
// dialog.showModal() would provide for free: focus moves into the dialog on
// open, Tab cycles inside it (focus trap), Escape closes the top-most modal
// only, and focus returns to the triggering element on close.
export function useModalFocus(
  open: Ref<boolean>,
  box: Ref<HTMLElement | null>,
  onClose: () => void,
): void {
  const id = Symbol('modal');
  let restoreTo: HTMLElement | null = null;

  function focusables(): HTMLElement[] {
    return Array.from(box.value?.querySelectorAll<HTMLElement>(FOCUSABLE) ?? []);
  }

  function onKeydown(e: KeyboardEvent): void {
    if (modalStack[modalStack.length - 1] !== id) return; // not the top modal
    if (e.key === 'Escape') {
      onClose();
      return;
    }
    if (e.key !== 'Tab') return;
    const els = focusables();
    if (els.length === 0) return;
    const first = els[0];
    const last = els[els.length - 1];
    const active = document.activeElement as HTMLElement | null;
    const inside = box.value?.contains(active) ?? false;
    if (e.shiftKey && (active === first || !inside)) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && (active === last || !inside)) {
      e.preventDefault();
      first.focus();
    }
  }

  function activate(): void {
    restoreTo = document.activeElement as HTMLElement | null;
    modalStack.push(id);
    window.addEventListener('keydown', onKeydown);
    void nextTick(() => focusables()[0]?.focus());
  }

  function deactivate(): void {
    const i = modalStack.indexOf(id);
    if (i === -1) return; // never activated (initial open=false fire)
    modalStack.splice(i, 1);
    window.removeEventListener('keydown', onKeydown);
    restoreTo?.focus();
    restoreTo = null;
  }

  watch(open, (isOpen) => (isOpen ? activate() : deactivate()), { immediate: true });
  onUnmounted(deactivate);
}
