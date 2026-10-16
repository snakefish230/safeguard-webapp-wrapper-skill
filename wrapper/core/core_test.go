package core

import (
	"fmt"
	"testing"
	"time"
)

func TestExpand(t *testing.T) {
	in := &Input{Values: map[string]string{"asset": "sps.lab", "username": "bob", "Target.AccountDomainName": "corp.local"}}
	if g := in.Expand("https://{asset}/login"); g != "https://sps.lab/login" {
		t.Fatal(g)
	}
	if g := in.Expand("{username}@{target.accountdomainname}"); g != "bob@corp.local" {
		t.Fatal(g)
	}
	if g := in.Expand("no placeholders"); g != "no placeholders" {
		t.Fatal(g)
	}
}

func TestSplitArgs(t *testing.T) {
	got := SplitArgs(`-url "https://x/a b" -set account=1 -insecure`)
	want := []string{"-url", "https://x/a b", "-set", "account=1", "-insecure"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("%q", got)
	}
}

func TestPickTOTP(t *testing.T) {
	now := time.Now().Unix()
	js := fmt.Sprintf(`[{"Code":"111111","UnixTime":%d,"Period":30},{"Code":"222222","UnixTime":%d,"Period":30}]`, now-28, now+2)

	// 111111 has 2s left, below the 5s floor, so the next period's code wins.
	c, err := pickTOTP(js, 5, nil)
	if err != nil || c != "222222" {
		t.Fatal(c, err)
	}

	// A rejected code must not be handed back on retry.
	if c, err := pickTOTP(js, 5, map[string]bool{"222222": true}); err == nil {
		t.Fatalf("excluded code reused: %q", c)
	}

	// A code with plenty of life left is preferred over a later one.
	js2 := fmt.Sprintf(`[{"Code":"333333","UnixTime":%d,"Period":30},{"Code":"444444","UnixTime":%d,"Period":30}]`, now-5, now+25)
	if c, err := pickTOTP(js2, 10, nil); err != nil || c != "333333" {
		t.Fatal(c, err)
	}
}

func TestLastJSONLine(t *testing.T) {
	if g := lastJSONLine("noise\n{\"a\":1}\n"); g != `{"a":1}` {
		t.Fatal(g)
	}
}
