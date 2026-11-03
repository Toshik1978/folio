package notify

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/Toshik1978/folio/internal/db"
	"github.com/Toshik1978/folio/internal/db/dbq"
)

// saveAcquireBudget bounds how long Save waits for the write guard, so a
// settings write behind a long indexing run fails fast instead of hanging.
const saveAcquireBudget = 2 * time.Second

// Store persists the Telegram Config in the settings table.
type Store struct {
	db            *sql.DB
	guard         *db.WriteGuard
	acquireBudget time.Duration
}

// NewStore returns a Store over database; writes are serialized by guard.
func NewStore(database *sql.DB, guard *db.WriteGuard) *Store {
	return &Store{db: database, guard: guard, acquireBudget: saveAcquireBudget}
}

// get reads one setting; a missing row reads as "".
func get(ctx context.Context, q *dbq.Queries, key string) (string, error) {
	v, err := q.GetSetting(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read setting %s: %w", key, err)
	}

	return v, nil
}

// Load returns the stored Config; absent keys read as zero values.
func (s *Store) Load(ctx context.Context) (Config, error) {
	q := dbq.New(s.db)
	var cfg Config

	enabled, err := get(ctx, q, db.SettingTelegramEnabled)
	if err != nil {
		return Config{}, err
	}
	cfg.Enabled = enabled == "1"

	if cfg.BotToken, err = get(ctx, q, db.SettingTelegramBotToken); err != nil {
		return Config{}, err
	}
	if cfg.ChatID, err = get(ctx, q, db.SettingTelegramChatID); err != nil {
		return Config{}, err
	}

	topic, err := get(ctx, q, db.SettingTelegramTopicID)
	if err != nil {
		return Config{}, err
	}
	if topic != "" {
		if cfg.TopicID, err = strconv.ParseInt(topic, 10, 64); err != nil {
			return Config{}, errors.New("stored telegram topic id is not a number")
		}
	}

	silent, err := get(ctx, q, db.SettingTelegramSilent)
	if err != nil {
		return Config{}, err
	}
	cfg.Silent = silent == "1"

	return cfg, nil
}

// Save writes all five keys in one transaction. If the write guard cannot be
// taken within the acquire budget the error matches context.DeadlineExceeded.
func (s *Store) Save(ctx context.Context, cfg Config) error {
	ctx, cancel := context.WithTimeout(ctx, s.acquireBudget)
	defer cancel()

	topic := ""
	if cfg.TopicID != 0 {
		topic = strconv.FormatInt(cfg.TopicID, 10)
	}
	rows := []dbq.UpsertSettingParams{
		{Key: db.SettingTelegramEnabled, Value: flag(cfg.Enabled)},
		{Key: db.SettingTelegramBotToken, Value: cfg.BotToken},
		{Key: db.SettingTelegramChatID, Value: cfg.ChatID},
		{Key: db.SettingTelegramTopicID, Value: topic},
		{Key: db.SettingTelegramSilent, Value: flag(cfg.Silent)},
	}

	err := s.guard.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		for _, r := range rows {
			if err := q.UpsertSetting(ctx, r); err != nil {
				return fmt.Errorf("write setting %s: %w", r.Key, err)
			}
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("save telegram settings: %w", err)
	}

	return nil
}

func flag(b bool) string {
	if b {
		return "1"
	}

	return "0"
}
