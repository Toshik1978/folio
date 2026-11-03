package db

// Settings-table keys shared across packages (api writes them, opds reads them,
// main seeds them on first startup).
const (
	SettingOPDSUser     = "opds_user"
	SettingOPDSPassHash = "opds_pass_hash" //nolint:gosec // settings key name, not a secret

	SettingTelegramEnabled  = "telegram_enabled"
	SettingTelegramBotToken = "telegram_bot_token" //nolint:gosec // settings key name, not a secret
	SettingTelegramChatID   = "telegram_chat_id"
	SettingTelegramTopicID  = "telegram_topic_id"
	SettingTelegramSilent   = "telegram_silent"
)
