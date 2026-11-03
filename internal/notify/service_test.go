package notify

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/stretchr/testify/suite"

	"github.com/Toshik1978/folio/internal/db"
)

type serviceSuite struct {
	suite.Suite

	store *Store
	fake  *fakeTelegram
}

func (s *serviceSuite) SetupTest() {
	database, err := db.Open(slog.New(slog.DiscardHandler), s.T().TempDir())
	s.Require().NoError(err)
	s.T().Cleanup(func() { _ = database.Close() })
	s.store = NewStore(database, db.NewWriteGuard())
	s.fake = newFakeTelegram(reply(200, okBody))
	s.T().Cleanup(s.fake.srv.Close)
}

func (s *serviceSuite) newService() *Service {
	svc, err := New(slog.New(slog.DiscardHandler), s.store, NewClient(s.fake.srv.URL, s.fake.srv.Client()))
	s.Require().NoError(err)
	s.T().Cleanup(svc.Stop)

	return svc
}

func (s *serviceSuite) save(cfg Config) {
	s.Require().NoError(s.store.Save(context.Background(), cfg))
}

func (s *serviceSuite) TestEmptyDBIsOff() {
	got, err := s.newService().TelegramSettings(context.Background())
	s.Require().NoError(err)
	s.False(got.Enabled)
	s.False(got.TokenSet)
}

func (s *serviceSuite) TestUpdatePersistsAndReloads() {
	svc := s.newService()
	got, err := svc.UpdateTelegram(context.Background(),
		Update{Enabled: true, BotToken: new(testToken), ChatID: testChat, Silent: true})
	s.Require().NoError(err)
	s.True(got.Enabled)
	s.True(got.TokenSet)

	again, err := s.newService().TelegramSettings(context.Background())
	s.Require().NoError(err)
	s.Equal(got, again)
	stored, err := s.store.Load(context.Background())
	s.Require().NoError(err)
	s.Equal(Config{Enabled: true, BotToken: testToken, ChatID: testChat, Silent: true}, stored)
}

func (s *serviceSuite) TestUpdateWithoutTokenKeepsStored() {
	s.save(Config{BotToken: testToken, ChatID: testChat})
	svc := s.newService()
	_, err := svc.UpdateTelegram(context.Background(), Update{Enabled: true, ChatID: "@other_channel"})
	s.Require().NoError(err)

	stored, err := s.store.Load(context.Background())
	s.Require().NoError(err)
	s.Equal(Config{Enabled: true, BotToken: testToken, ChatID: "@other_channel"}, stored)
}

func (s *serviceSuite) TestInvalidUpdateChangesNothing() {
	before := Config{BotToken: testToken, ChatID: testChat}
	s.save(before)
	svc := s.newService()
	_, err := svc.UpdateTelegram(context.Background(), Update{Enabled: true, ChatID: "bad chat"})

	var ve *ValidationError
	s.Require().ErrorAs(err, &ve)
	stored, err := s.store.Load(context.Background())
	s.Require().NoError(err)
	s.Equal(before, stored)
	s.Equal(before, *svc.cfg.Load())
}

func (s *serviceSuite) TestSendTestNotConfigured() {
	err := s.newService().SendTest(context.Background())
	s.Require().ErrorIs(err, ErrNotConfigured)
	s.Zero(s.fake.count())
}

func (s *serviceSuite) TestSendTestWhileOff() {
	s.save(Config{BotToken: testToken, ChatID: testChat})
	svc := s.newService()
	s.Require().NoError(svc.SendTest(context.Background()))
	s.Equal(1, s.fake.count())
	s.NotNil(svc.DeliveryStatus().LastSuccessAt)
}

func (s *serviceSuite) TestSendTestRejected() {
	s.fake.reply = reply(400, `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`)
	s.save(Config{BotToken: testToken, ChatID: testChat})
	svc := s.newService()
	err := svc.SendTest(context.Background())

	var api *APIError
	s.Require().ErrorAs(err, &api)
	st := svc.DeliveryStatus()
	s.NotNil(st.LastFailureAt)
	s.NotEmpty(st.LastFailureError)
	s.NotContains(st.LastFailureError, testToken)
	s.NotContains(err.Error(), testToken)
}

func (s *serviceSuite) TestNoTokenInSettings() {
	svc := s.newService()
	got, err := svc.UpdateTelegram(context.Background(),
		Update{Enabled: true, BotToken: new(testToken), ChatID: testChat})
	s.Require().NoError(err)
	s.NotContains(fmt.Sprintf("%+v", got), testToken)

	_, err = svc.UpdateTelegram(context.Background(), Update{BotToken: new("bad"), ChatID: testChat})
	s.Require().Error(err)
	s.NotContains(err.Error(), testToken)
}
