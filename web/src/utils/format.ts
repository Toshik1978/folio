// formatSize renders a byte count as a human-readable string using decimal
// units (1 KB = 1000 B), matching what mainstream OS file managers display.
export function formatSize(bytes: number): string {
  if (bytes < 1000) return `${bytes} B`;
  if (bytes < 1000 * 1000) return `${(bytes / 1000).toFixed(0)} KB`;
  if (bytes < 1000 * 1000 * 1000) return `${(bytes / (1000 * 1000)).toFixed(1)} MB`;
  return `${(bytes / (1000 * 1000 * 1000)).toFixed(1)} GB`;
}

// formatProgress renders an indexing count: "1,200 / 5,000" when a total is known,
// or "1,200 books" for an indeterminate run.
export function formatProgress(processed: number, total?: number): string {
  return total
    ? `${processed.toLocaleString()} / ${total.toLocaleString()}`
    : `${processed.toLocaleString()} books`;
}

// formatTime renders a unix-seconds timestamp in the viewer's locale.
export function formatTime(timestamp: number): string {
  return new Date(timestamp * 1000).toLocaleString();
}
