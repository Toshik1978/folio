package db

// Settings-table keys shared across packages (api writes them, opds reads them,
// main seeds them on first startup).
const (
	SettingOPDSUser     = "opds_user"
	SettingOPDSPassHash = "opds_pass_hash" //nolint:gosec // settings key name, not a secret
)
