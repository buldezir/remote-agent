package netinfo

import (
	"net/url"
	"testing"
)

func TestPairingLinkName(t *testing.T) {
	link := PairingLink("Studio Mac + iPad", "abc", []string{"http://127.0.0.1:7421"})
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	if u.RawQuery != "code=abc&name=Studio%20Mac%20%2B%20iPad&url=http%3A%2F%2F127.0.0.1%3A7421&v=1" {
		t.Errorf("query = %s", u.RawQuery)
	}
	if got := u.Query().Get("name"); got != "Studio Mac + iPad" {
		t.Errorf("name = %q", got)
	}
}
