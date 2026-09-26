package source

import (
	"net/http"
	"testing"
)

func TestTheSessionJoinsThePagesCookiesAndLosesOnlyToThem(t *testing.T) {
	got := WithSession(http.Header{"Cookie": {"consent=yes; token=page"}}, http.Header{"Cookie": {"token=jar; hdntl=exp=1"}})
	if c := got.Get("Cookie"); c != "consent=yes; token=page; hdntl=exp=1" {
		t.Errorf("Cookie = %q, want the page's cookies, then the session's it does not name", c)
	}
}
