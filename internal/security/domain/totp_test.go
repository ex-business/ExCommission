package domain

import (
	"testing"
	"time"
)

func TestRFC6238SHA1VectorsAndWindow(t *testing.T) {
	secret := []byte("12345678901234567890")
	for _, v := range []struct {
		at   int64
		want string
	}{{59, "94287082"}, {1111111109, "07081804"}, {1111111111, "14050471"}, {1234567890, "89005924"}, {2000000000, "69279037"}, {20000000000, "65353130"}} {
		got, e := Code(secret, uint64(v.at/30), 8)
		if e != nil || got != v.want {
			t.Fatal(v, got, e)
		}
	}
	at := time.Unix(1234567890, 0)
	code, _ := Code(secret, uint64(at.Unix()/30), 6)
	for _, shift := range []time.Duration{-30 * time.Second, 0, 30 * time.Second} {
		if _, e := Match(secret, code, at.Add(shift)); e != nil {
			t.Fatal(shift, e)
		}
	}
	if _, e := Match(secret, code, at.Add(90*time.Second)); e == nil {
		t.Fatal("expired code")
	}
	for _, bad := range []string{"12345", "1234567", "12x456", " 12345"} {
		if _, e := Match(secret, bad, at); e == nil {
			t.Fatal(bad)
		}
	}
}
