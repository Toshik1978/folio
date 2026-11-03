package notify

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/Toshik1978/folio/internal/db"
	"github.com/Toshik1978/folio/internal/db/dbq"
)

type storeSuite struct {
	suite.Suite

	database *sql.DB
	guard    *db.WriteGuard
	store    *Store
}

func (s *storeSuite) SetupTest() {
	database, err := db.Open(slog.New(slog.DiscardHandler), s.T().TempDir())
	s.Require().NoError(err)
	s.T().Cleanup(func() { _ = database.Close() })
	s.database = database
	s.guard = db.NewWriteGuard()
	s.store = NewStore(database, s.guard)
}

func (s *storeSuite) TestEmptyLoadsZeroConfig() {
	cfg, err := s.store.Load(context.Background())
	s.Require().NoError(err)
	s.Equal(Config{}, cfg)
}

func (s *storeSuite) TestRoundTrip() {
	ctx := context.Background()
	for _, want := range []Config{
		{Enabled: true, BotToken: "123456:TEST-token_SENTINEL", ChatID: "@test_channel", TopicID: 42, Silent: true},
		{Enabled: false, BotToken: "123456:TEST-token_SENTINEL", ChatID: "-1001234", Silent: false},
	} {
		s.Require().NoError(s.store.Save(ctx, want))
		got, err := s.store.Load(ctx)
		s.Require().NoError(err)
		s.Equal(want, got)
	}
}

func (s *storeSuite) TestSecondSaveOverwritesEveryKey() {
	ctx := context.Background()
	s.Require().NoError(s.store.Save(ctx, Config{
		Enabled: true, BotToken: "1:a", ChatID: "@first_chan", TopicID: 7, Silent: true,
	}))
	want := Config{BotToken: "2:b", ChatID: "-5"}
	s.Require().NoError(s.store.Save(ctx, want))
	got, err := s.store.Load(ctx)
	s.Require().NoError(err)
	s.Equal(want, got)
}

func (s *storeSuite) TestSaveGuardTimeout() {
	ctx := context.Background()
	s.Require().NoError(s.guard.Lock(ctx))
	s.store.acquireBudget = 5 * time.Millisecond

	err := s.store.Save(ctx, Config{Enabled: true, BotToken: "1:a", ChatID: "-5"})
	s.Require().ErrorIs(err, context.DeadlineExceeded)
	s.guard.Unlock()

	_, gerr := dbq.New(s.database).GetSetting(ctx, db.SettingTelegramChatID)
	s.Require().ErrorIs(gerr, sql.ErrNoRows)
}
