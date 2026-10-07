package retry

import (
	"errors"
	"testing"
)

func TestIsErrorMatch(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{errors.New("connection dead"), true},
		{errors.New("rpc error: Timedout"), true},
		{errors.New("No workers running"), true},
		{errors.New("wrap: connection dead: extra"), true},
		{errors.New("some unrelated error"), false},
		{nil, false},
	}
	for _, c := range cases {
		if got := isErrorMatch(c.err); got != c.want {
			t.Errorf("isErrorMatch(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}
