import type { Library } from '@/types';

// statusClass maps a library status to its DaisyUI badge class.
export function statusClass(status: string): string {
  switch (status) {
    case 'active':
      return 'badge-success';
    case 'syncing':
      return 'badge-info';
    case 'error':
      return 'badge-error';
    case 'pending_purge':
      return 'badge-warning';
    case 'queued':
      return 'badge-ghost';
    default:
      return 'badge-ghost';
  }
}

// statusLabel maps a library status to its human-readable label.
export function statusLabel(status: string): string {
  switch (status) {
    case 'active':
      return 'Active';
    case 'syncing':
      return 'Syncing';
    case 'error':
      return 'Error';
    case 'pending_purge':
      return 'Pending Purge';
    case 'queued':
      return 'Queued';
    default:
      return status;
  }
}

const typeLabels: Record<Library['type'], string> = {
  calibre: 'Calibre',
  inpx: 'INPX',
  folder: 'Folder',
};

// typeLabel renders a library source type for display.
export function typeLabel(type: Library['type']): string {
  return typeLabels[type] ?? type;
}
