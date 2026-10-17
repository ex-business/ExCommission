package domain

import (
	"net/url"
	"testing"
)

func TestInvitationURLUsesRegistrationParameter(t *testing.T) {
	value, e := InvitationURL("https://members.example/register?channel=agent", "123456789")
	if e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(value)
	if u.Query().Get("ic") != "123456789" || u.Query().Get("channel") != "agent" {
		t.Fatal(value)
	}
	for _, base := range []string{"http://members.example/", "javascript:alert(1)", "https://user:password@members.example/", "https://members.example/#fragment"} {
		if _, e = InvitationURL(base, "123456789"); e == nil {
			t.Fatal(base)
		}
	}
	if _, e = InvitationURL("http://127.0.0.1:18286/", "123456789"); e != nil {
		t.Fatal(e)
	}
}
