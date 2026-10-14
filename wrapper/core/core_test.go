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
	c, err := pickTOTP(js, 5)
	if err != nil || c != "222222" {
		t.Fatal(c, err)
	}
}

func TestLastJSONLine(t *testing.T) {
	if g := lastJSONLine("noise\n{\"a\":1}\n"); g != `{"a":1}` {
		t.Fatal(g)
	}
}
