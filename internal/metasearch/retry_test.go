package metasearch

import (
	"context"
	"errors"
	"fmt"
	"time"
)

func (s *coreSuite) TestRetryCoversFirstSuccessCalledOnce() {
	calls := 0
	covers := []CoverCandidate{{Source: SourceAmazon, FullURL: "http://example.com/a.jpg"}}
	fn := func(_ context.Context) ([]CoverCandidate, error) {
		calls++

		return covers, nil
	}

	got, err := RetryCovers(context.Background(), 3, time.Millisecond, fn)
	s.Require().NoError(err)
	s.Require().Equal(covers, got)
	s.Require().Equal(1, calls, "fn should be called exactly once on first success")
}

func (s *coreSuite) TestRetryCoversRetriesPastError() {
	calls := 0
	covers := []CoverCandidate{{Source: SourceAmazon, FullURL: "http://example.com/a.jpg"}}
	fn := func(_ context.Context) ([]CoverCandidate, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("transient error")
		}

		return covers, nil
	}

	got, err := RetryCovers(context.Background(), 3, time.Millisecond, fn)
	s.Require().NoError(err)
	s.Require().Equal(covers, got)
	s.Require().Equal(2, calls, "fn should be called twice: once error, once success")
}

func (s *coreSuite) TestRetryCoversRetriesPastEmptyResult() {
	calls := 0
	covers := []CoverCandidate{{Source: SourceAmazon, FullURL: "http://example.com/a.jpg"}}
	fn := func(_ context.Context) ([]CoverCandidate, error) {
		calls++
		if calls == 1 {
			return nil, nil // empty, no error (anti-bot interstitial)
		}

		return covers, nil
	}

	got, err := RetryCovers(context.Background(), 3, time.Millisecond, fn)
	s.Require().NoError(err)
	s.Require().Equal(covers, got)
	s.Require().Equal(2, calls, "fn should be called twice: once empty, once success")
}

func (s *coreSuite) TestRetryCoversAllEmptyNoError() {
	calls := 0
	fn := func(_ context.Context) ([]CoverCandidate, error) {
		calls++

		return nil, nil
	}

	got, err := RetryCovers(context.Background(), 3, time.Millisecond, fn)
	s.Require().NoError(err)
	s.Require().Nil(got)
	s.Require().Equal(3, calls)
}

func (s *coreSuite) TestRetryCoversAllError() {
	sentinel := errors.New("persistent error")
	calls := 0
	fn := func(_ context.Context) ([]CoverCandidate, error) {
		calls++

		return nil, sentinel
	}

	got, err := RetryCovers(context.Background(), 3, time.Millisecond, fn)
	s.Require().ErrorIs(err, sentinel)
	s.Require().Nil(got)
	s.Require().Equal(3, calls)
}

func (s *coreSuite) TestRetryCoversRespectsContextCancellation() {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	calls := 0
	fn := func(_ context.Context) ([]CoverCandidate, error) {
		calls++

		return nil, nil // empty result so it would retry
	}

	// First attempt runs (i==0, no wait), then on i==1 select hits ctx.Done()
	got, err := RetryCovers(ctx, 3, time.Millisecond, fn)
	s.Require().ErrorIs(err, context.Canceled)
	s.Require().Nil(got)
	// fn was called once (attempt 0 has no wait); attempt 1 hits ctx.Done before fn
	s.Require().Equal(1, calls)
}

func (s *coreSuite) TestRetryCoversStopsOnNoRetry() {
	var calls int
	terminal := fmt.Errorf("blocked hard: %w", errors.Join(ErrBlocked, ErrNoRetry))

	out, err := RetryCovers(context.Background(), 3, time.Millisecond,
		func(context.Context) ([]CoverCandidate, error) {
			calls++
			return nil, terminal
		},
	)

	s.Require().Nil(out)
	s.Require().ErrorIs(err, ErrNoRetry)
	s.Require().ErrorIs(err, ErrBlocked)
	s.Require().Equal(1, calls, "a terminal error must not be retried")
}
