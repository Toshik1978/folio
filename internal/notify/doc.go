// Package notify delivers opt-in Telegram notifications about failures.
//
// Invariants that every file in the package upholds:
//
//   - Producers never block and never do I/O: reporting an outcome only
//     enqueues a message, and a full queue drops the message instead of waiting.
//   - The notifier never logs through the tapped logger, so a notification
//     failure can never trigger another notification (no loops).
//   - The bot token never leaves Store and Client: it is not returned by any
//     API, logged, or included in an error string.
//   - All state except the five Telegram settings rows is held in memory and is
//     lost on restart, by design.
package notify
