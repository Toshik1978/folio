package ebook

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
)

type errorsSuite struct {
	baseSuite
}

func (s *errorsSuite) TestReasonOf() {
	cases := []struct {
		name   string
		err    error
		reason Reason
		ok     bool
	}{
		{"too large", ErrTooLarge, ReasonTooLarge, true},
		{"malformed", ErrMalformed, ReasonCorrupt, true},
		{"panic", &PanicError{Value: "boom"}, ReasonCrashed, true},
		{"source io", ErrSourceIO, "", false},
		{"canceled", context.Canceled, "", false},
		{"deadline", context.DeadlineExceeded, "", false},
		{"not exist", fs.ErrNotExist, "", false},
		{"permission", fs.ErrPermission, "", false},
		{"unknown", errors.New("something else"), "", false},
		{"nil", nil, "", false},
		{"source io beats malformed", errors.Join(ErrSourceIO, ErrMalformed), "", false},
		{"source io beats too large", errors.Join(ErrTooLarge, ErrSourceIO), "", false},
		{"source io beats panic", errors.Join(&PanicError{Value: 1}, ErrSourceIO), "", false},
	}
	for _, tc := range cases {
		for _, wrapped := range []bool{false, true} {
			err := tc.err
			if wrapped && err != nil {
				err = fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", err))
			}
			reason, ok := ReasonOf(err)
			s.Equal(tc.ok, ok, "%s wrapped=%v", tc.name, wrapped)
			s.Equal(tc.reason, reason, "%s wrapped=%v", tc.name, wrapped)
		}
	}
}

func (s *errorsSuite) TestPanicErrorMessage() {
	s.Contains((&PanicError{Value: "kaboom"}).Error(), "kaboom")
}
