package main

import "testing"

func TestIdentity(t *testing.T) {
	cases := []struct{ user, acctDom, override, wantU, wantD string }{
		{"svc-admin", "corp.example.com", "", "svc-admin", "corp.example.com"},
		{"svc-admin", "corp.example.com", "CORP", "svc-admin", "CORP"},
		{`CORP\svc-admin`, "other.example.com", "", "svc-admin", "CORP"},
		{"svc-admin@corp.example.com", "corp.example.com", "", "svc-admin@corp.example.com", ""},
	}
	for _, c := range cases {
		u, d, err := identity(c.user, c.acctDom, c.override)
		if err != nil || u != c.wantU || d != c.wantD {
			t.Errorf("identity(%q,%q,%q) = %q,%q,%v; want %q,%q", c.user, c.acctDom, c.override, u, d, err, c.wantU, c.wantD)
		}
	}
	if _, _, err := identity("localadmin", "", ""); err == nil {
		t.Error("account without a domain must fail: a netonly logon with no domain authenticates as nobody")
	}
}

func TestCommandLine(t *testing.T) {
	got := commandLine(`C:\Windows\System32\mmc.exe`, `C:\Windows\System32\dsa.msc`, "dc01.corp.example.com", "")
	want := `C:\Windows\System32\mmc.exe C:\Windows\System32\dsa.msc /server=dc01.corp.example.com`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	got = commandLine(`C:\Program Files\x\mmc.exe`, `D:\My Consoles\ad.msc`, "", "corp.example.com")
	want = `"C:\Program Files\x\mmc.exe" "D:\My Consoles\ad.msc" /domain=corp.example.com`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}
