package notify

import (
	"errors"
	"regexp"
	"strconv"
)

// ErrNotConfigured is returned when an operation needs a bot token and a chat
// that have not both been set.
var ErrNotConfigured = errors.New("notify: telegram is not configured")

// ValidationError describes a rejected settings update. Msg never contains the
// rejected value, so the bot token cannot leak through it.
type ValidationError struct {
	Field string
	Msg   string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Msg }

// Config is the active Telegram configuration.
type Config struct {
	Enabled  bool
	BotToken string // secret
	ChatID   string
	TopicID  int64 // 0 = none
	Silent   bool
}

// Ready reports whether a message can be addressed: a token and a chat are set.
func (c Config) Ready() bool { return c.BotToken != "" && c.ChatID != "" }

// Update is a requested settings change.
type Update struct {
	Enabled  bool
	BotToken *string // nil or "" = keep stored
	ChatID   string
	TopicID  *int64 // nil = none
	Silent   bool
}

// Settings is the redacted view of Config. It has no token field.
type Settings struct {
	Enabled    bool
	TokenSet   bool
	TokenLast4 string // "" when unset or shorter than 8 characters
	ChatID     string
	TopicID    *int64
	Silent     bool
	Status     DeliveryStatus
}

const (
	minLast4TokenLen = 8
	last4            = 4
)

var (
	tokenRe      = regexp.MustCompile(`^\d+:[A-Za-z0-9_-]+$`)
	chatHandleRe = regexp.MustCompile(`^@[A-Za-z0-9_]{5,32}$`)
	chatNumberRe = regexp.MustCompile(`^-?\d+$`)
)

// view builds the redacted Settings for c.
func view(c Config, st DeliveryStatus) Settings {
	s := Settings{
		Enabled:  c.Enabled,
		TokenSet: c.BotToken != "",
		ChatID:   c.ChatID,
		Silent:   c.Silent,
		Status:   st,
	}
	if len(c.BotToken) >= minLast4TokenLen {
		s.TokenLast4 = c.BotToken[len(c.BotToken)-last4:]
	}
	if c.TopicID != 0 {
		id := c.TopicID
		s.TopicID = &id
	}

	return s
}

func validChatID(id string) bool {
	if chatHandleRe.MatchString(id) {
		return true
	}
	if !chatNumberRe.MatchString(id) {
		return false
	}
	_, err := strconv.ParseInt(id, 10, 64)

	return err == nil
}

// validate checks u against stored and returns the Config to persist. An absent
// or empty token keeps the stored one. Nothing is returned on error.
func validate(u Update, stored Config) (Config, error) {
	cfg := Config{
		Enabled:  u.Enabled,
		BotToken: stored.BotToken,
		ChatID:   u.ChatID,
		Silent:   u.Silent,
	}
	if u.BotToken != nil && *u.BotToken != "" {
		if !tokenRe.MatchString(*u.BotToken) {
			return Config{}, &ValidationError{Field: "bot_token", Msg: "invalid bot token format"}
		}
		cfg.BotToken = *u.BotToken
	}
	if u.ChatID != "" && !validChatID(u.ChatID) {
		return Config{}, &ValidationError{
			Field: "chat_id",
			Msg:   "must be @channelname (5-32 letters, digits or underscores) or a numeric chat ID",
		}
	}
	if u.TopicID != nil {
		if *u.TopicID <= 0 {
			return Config{}, &ValidationError{Field: "topic_id", Msg: "must be a positive integer"}
		}
		cfg.TopicID = *u.TopicID
	}
	if cfg.Enabled && !cfg.Ready() {
		return Config{}, &ValidationError{Field: "enabled", Msg: "notifications need a bot token and a channel ID"}
	}

	return cfg, nil
}
