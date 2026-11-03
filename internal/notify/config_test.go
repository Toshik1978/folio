package notify

import (
	"fmt"
	"strings"

	"github.com/stretchr/testify/suite"
)

type configSuite struct {
	suite.Suite
}

const (
	testToken = "123456:TEST-token_SENTINEL"
	testChat  = "@test_channel"
)

func (s *configSuite) fieldOf(err error) string {
	var ve *ValidationError
	s.Require().ErrorAs(err, &ve)
	return ve.Field
}

func (s *configSuite) TestToken() {
	cases := []struct {
		token string
		ok    bool
	}{
		{"123:abc_-X", true},
		{"12/3:abc", false},
		{"123:ab?c", false},
		{"123:ab#c", false},
		{"123:ab c", false},
		{"123abc", false},
		{"x23:abc", false},
		{"123:", false},
	}
	for _, c := range cases {
		got, err := validate(Update{BotToken: new(c.token), ChatID: testChat}, Config{})
		if c.ok {
			s.Require().NoError(err, c.token)
			s.Equal(c.token, got.BotToken)
			continue
		}
		s.Require().Error(err, c.token)
		s.Equal("bot_token", s.fieldOf(err))
		s.NotContains(err.Error(), c.token)
	}
}

func (s *configSuite) TestChatID() {
	cases := []struct {
		chat string
		ok   bool
	}{
		{"@abcde", true},
		{"@" + strings.Repeat("a", 32), true},
		{"@abcd", false},
		{"@" + strings.Repeat("a", 33), false},
		{"@a-b", false},
		{"-1001234567890", true},
		{"42", true},
		{"-", false},
		{"9223372036854775808", false},
		{"+42", false},
	}
	for _, c := range cases {
		got, err := validate(Update{BotToken: new(testToken), ChatID: c.chat}, Config{})
		if c.ok {
			s.Require().NoError(err, c.chat)
			s.Equal(c.chat, got.ChatID)
			continue
		}
		s.Require().Error(err, c.chat)
		s.Equal("chat_id", s.fieldOf(err))
	}
}

func (s *configSuite) TestTopic() {
	for _, bad := range []int64{0, -1} {
		_, err := validate(Update{BotToken: new(testToken), ChatID: testChat, TopicID: new(bad)}, Config{})
		s.Require().Error(err)
		s.Equal("topic_id", s.fieldOf(err))
	}
	got, err := validate(Update{BotToken: new(testToken), ChatID: testChat, TopicID: new(int64(7))}, Config{})
	s.Require().NoError(err)
	s.Equal(int64(7), got.TopicID)

	got, err = validate(Update{BotToken: new(testToken), ChatID: testChat}, Config{TopicID: 9})
	s.Require().NoError(err)
	s.Zero(got.TopicID)
}

func (s *configSuite) TestKeepOrReplaceToken() {
	stored := Config{BotToken: testToken}
	for _, u := range []Update{{ChatID: testChat}, {ChatID: testChat, BotToken: new("")}} {
		got, err := validate(u, stored)
		s.Require().NoError(err)
		s.Equal(testToken, got.BotToken)
	}
	got, err := validate(Update{ChatID: testChat, BotToken: new("999:new_TOKEN")}, stored)
	s.Require().NoError(err)
	s.Equal("999:new_TOKEN", got.BotToken)
}

func (s *configSuite) TestEnabledNeedsTokenAndChat() {
	const msg = "notifications need a bot token and a channel ID"
	_, err := validate(Update{Enabled: true, ChatID: testChat}, Config{})
	s.Require().Error(err)
	s.Equal("enabled", s.fieldOf(err))
	s.Contains(err.Error(), msg)

	_, err = validate(Update{Enabled: true, BotToken: new(testToken)}, Config{})
	s.Require().Error(err)
	s.Contains(err.Error(), msg)

	got, err := validate(Update{Enabled: true, ChatID: testChat, Silent: true}, Config{BotToken: testToken})
	s.Require().NoError(err)
	s.True(got.Enabled)
	s.True(got.Silent)
	s.True(got.Ready())
}

func (s *configSuite) TestDisabledKeepsValues() {
	got, err := validate(Update{
		BotToken: new(testToken), ChatID: testChat, TopicID: new(int64(3)), Silent: true,
	}, Config{})
	s.Require().NoError(err)
	s.Equal(Config{BotToken: testToken, ChatID: testChat, TopicID: 3, Silent: true}, got)

	got, err = validate(Update{}, Config{BotToken: testToken})
	s.Require().NoError(err)
	s.Equal(testToken, got.BotToken)
	s.Empty(got.ChatID)
	s.False(got.Ready())
}

func (s *configSuite) TestView() {
	st := DeliveryStatus{LastFailureError: "boom"}
	v := view(Config{BotToken: testToken, ChatID: testChat}, st)
	s.Equal(st, v.Status)
	s.True(v.TokenSet)
	s.Equal(testToken[len(testToken)-4:], v.TokenLast4)
	s.Nil(v.TopicID)

	v = view(Config{BotToken: "1:abcde", TopicID: 5}, DeliveryStatus{})
	s.True(v.TokenSet)
	s.Empty(v.TokenLast4)
	s.Require().NotNil(v.TopicID)
	s.Equal(int64(5), *v.TopicID)

	v = view(Config{}, DeliveryStatus{})
	s.False(v.TokenSet)
	s.Empty(v.TokenLast4)

	full := view(Config{BotToken: testToken}, DeliveryStatus{})
	s.NotContains(fmt.Sprintf("%+v", full), testToken)
	s.NotContains(fmt.Sprintf("%+v", full), "SENTINEL")
}
