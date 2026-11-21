package github

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

// A permission 403 must not masquerade as an exhausted quota: the UI would
// tell the user to wait for a reset that never helps, and the retry loop would
// keep hammering the same endpoint.
func TestForbiddenPermissionIsNotRateLimit(t *testing.T) {
	c, hits, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "4999")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"Resource not accessible by personal access token"}`))
	})
	_, err := c.Login(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("err is not *Error: %v", err)
	}
	if apiErr.Rate {
		t.Fatal("permission 403 reported as a rate limit")
	}
	if apiErr.Code != "E_FORBIDDEN" {
		t.Fatalf("code = %q, want E_FORBIDDEN", apiErr.Code)
	}
	if got := apiErr.Error(); got == "" || apiErr.Message == "" {
		t.Fatalf("missing message: %q", got)
	}
	if n := *hits; n > 1 {
		t.Fatalf("permission error was retried %d times", n-1)
	}
}

func TestForbiddenSecondaryRateLimit(t *testing.T) {
	c, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"You have exceeded a secondary rate limit. Please wait a few minutes before you try again."}`))
	})
	_, err := c.Login(context.Background())
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("err is not *Error: %v", err)
	}
	if !apiErr.Rate {
		t.Fatal("secondary rate limit not classified as a rate limit")
	}
	if apiErr.Code != "E_RATE_LIMIT" {
		t.Fatalf("code = %q, want E_RATE_LIMIT", apiErr.Code)
	}
}

// The header is the primary signal: a zero remaining count is a real limit even
// when the body carries a permission-flavoured message.
func TestForbiddenZeroRemainingIsRateLimit(t *testing.T) {
	c, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", "0")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
	})
	_, err := c.Login(context.Background())
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("err is not *Error: %v", err)
	}
	if !apiErr.Rate {
		t.Fatal("zero remaining was not classified as a rate limit")
	}
}

func TestLooksRateLimited(t *testing.T) {
	cases := map[string]bool{
		"You have exceeded a secondary rate limit.":        true,
		"API rate limit exceeded for 1.2.3.4.":             true,
		"Too Many Requests":                                true,
		"Abuse detection mechanism triggered.":             true,
		"Resource not accessible by personal access token": false,
		"Repository access blocked":                        false,
		"":                                                 false,
	}
	for message, want := range cases {
		if got := looksRateLimited(message); got != want {
			t.Errorf("looksRateLimited(%q) = %v, want %v", message, got, want)
		}
	}
}
