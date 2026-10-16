#!/usr/bin/env python3
"""Run PowerShell on a Windows host over WinRM using a credential checked out of Safeguard.

The password is read straight from the Safeguard API into memory and handed to the WinRM
transport. It is never printed, logged, or written to disk, and the access request is checked
in again on exit. Nothing here echoes the credential, so it stays out of any transcript.

  python sg_winrm.py --asset 10.0.7.103 --account da-shared1 --account-domain safeguardlab.net \
      --command 'whoami'
  python sg_winrm.py ... --script local.ps1 --upload file.exe:C:\\dir\\file.exe

Bearer token comes from $SAFEGUARD_TOKEN_FILE or ~/sg-token.txt (see ~/bin/refresh-safeguard-token).
"""
import argparse
import base64
import hashlib
import os
import subprocess
import sys
import tempfile
import urllib3

import requests
import winrm

urllib3.disable_warnings(urllib3.exceptions.InsecureRequestWarning)


class Sg:
    def __init__(self, appliance, token, verify=False):
        self.base = f"https://{appliance}/service/core/v4"
        self.s = requests.Session()
        self.s.verify = verify
        self.s.headers["Authorization"] = f"Bearer {token}"
        self.request_id = None
        self.created = False

    def _j(self, method, path, **kw):
        r = self.s.request(method, self.base + path, timeout=60, **kw)
        if not r.ok:
            raise SystemExit(f"Safeguard {method} {path} -> HTTP {r.status_code}: {r.text[:300]}")
        return r.json() if r.text.strip() else None

    def find_account(self, account, domain, asset):
        """Resolve the account to (assetId, accountId, policy, activeRequests) via own entitlements."""
        ents = self._j("GET", "/Me/RequestEntitlements", params={"accessRequestType": "Password"})
        for e in ents:
            a = e.get("Account") or {}
            if a.get("Name", "").lower() != account.lower():
                continue
            if domain and (a.get("DomainName") or "").lower() != domain.lower():
                continue
            return (a["AssetId"], a["Id"], (e.get("Policy") or {}).get("Name"),
                    e.get("ActiveRequests") or [])
        raise SystemExit(
            f"'{account}' is not requestable for Password access by you. "
            f"Grant an access policy in SPP, or pick another account."
        )

    def open_request(self, system_id, account_id, reason, create, active):
        """Reuse an open Password request for this account, else create one."""
        for req in active:
            if req.get("State") in ("RequestAvailable", "PasswordCheckedOut", "Approved"):
                self.request_id = req["Id"]
                return req["State"], False
        if not create:
            raise SystemExit("No open Password request for that account; pass --create-request.")
        body = {"SystemId": system_id, "AccountId": account_id,
                "AccessRequestType": "Password", "ReasonComment": reason}
        req = self._j("POST", "/AccessRequests", json=body)
        self.request_id, self.created = req["Id"], True
        state = req.get("State")
        if state == "PendingApproval":
            self.cleanup()
            raise SystemExit("Access request requires approval; re-run once it is approved.")
        return state, True

    def checkout(self):
        """Return the password. Callers must never print the result."""
        pw = self._j("POST", f"/AccessRequests/{self.request_id}/CheckOutPassword")
        if not isinstance(pw, str) or not pw:
            raise SystemExit("Check-out did not return a password.")
        return pw

    def cleanup(self):
        if not self.request_id:
            return
        for action in ("CheckIn", "Cancel"):
            try:
                self._j("POST", f"/AccessRequests/{self.request_id}/{action}")
                return
            except SystemExit:
                continue


def ps(session, script):
    r = session.run_ps(script)
    out = r.std_out.decode(errors="replace")
    err = r.std_err.decode(errors="replace")
    return r.status_code, out, err


def smb_put(host, domain, user, password, local, remote_unc_dir, remote_name):
    """Copy a file to an admin share with smbclient, password via a 0600 auth file."""
    share, _, subdir = remote_unc_dir.partition("\\")
    fd, authfile = tempfile.mkstemp()
    try:
        with os.fdopen(fd, "w") as f:
            f.write(f"username={user}\npassword={password}\ndomain={domain}\n")
        os.chmod(authfile, 0o600)
        cmds = []
        if subdir:
            parts, acc = subdir.split("\\"), ""
            for p in parts:
                acc = f"{acc}\\{p}" if acc else p
                cmds.append(f'mkdir "{acc}"')
            cmds.append(f'put "{local}" "{subdir}\\{remote_name}"')
        else:
            cmds.append(f'put "{local}" "{remote_name}"')
        r = subprocess.run(
            ["smbclient", f"//{host}/{share}", "-A", authfile, "-c", "; ".join(cmds)],
            capture_output=True, text=True, timeout=600)
        # NT_STATUS_OBJECT_NAME_COLLISION just means the directory already exists.
        bad = [l for l in (r.stdout + r.stderr).splitlines()
               if "NT_STATUS" in l and "COLLISION" not in l]
        if r.returncode != 0 and bad:
            raise SystemExit("smbclient failed: " + "\n".join(bad[:5]))
        return "\n".join(l for l in r.stdout.splitlines() if "blocks/sec" in l or "putting file" in l)
    finally:
        os.unlink(authfile)


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--appliance", default="spp.safeguardlab.net")
    p.add_argument("--host", required=True, help="WinRM target (IP or name)")
    p.add_argument("--account", required=True)
    p.add_argument("--account-domain", default="")
    p.add_argument("--login-name", default="", help="override WinRM login, e.g. DOMAIN\\user")
    p.add_argument("--create-request", action="store_true")
    p.add_argument("--keep-request", action="store_true")
    p.add_argument("--reason", default="Publish Safeguard RemoteApp wrapper")
    p.add_argument("--command", help="PowerShell to run on the host")
    p.add_argument("--script", help="local .ps1 whose contents are run on the host")
    p.add_argument("--arg", action="append", default=[],
                   help="NAME=VALUE passed to --script as $env:NAME on the host")
    p.add_argument("--upload", action="append", default=[],
                   help="local:REMOTE_ABS_PATH copied via SMB (repeatable)")
    p.add_argument("--token-file", default=os.environ.get(
        "SAFEGUARD_TOKEN_FILE", os.path.expanduser("~/sg-token.txt")))
    a = p.parse_args()

    with open(a.token_file) as f:
        token = f.read().strip()

    sg = Sg(a.appliance, token)
    system_id, account_id, policy, active = sg.find_account(a.account, a.account_domain, a.host)
    print(f"[sg] account '{a.account}' requestable via policy '{policy}'", file=sys.stderr)
    state, created = sg.open_request(system_id, account_id, a.reason, a.create_request, active)
    print(f"[sg] request {sg.request_id} state={state} ({'created' if created else 'reused'})",
          file=sys.stderr)

    rc = 1
    try:
        password = sg.checkout()
        print("[sg] password checked out (not displayed)", file=sys.stderr)
        user = a.login_name or (f"{a.account_domain}\\{a.account}" if a.account_domain else a.account)

        for spec in a.upload:
            local, _, remote = spec.partition(":")
            if not remote:
                raise SystemExit(f"--upload needs local:REMOTE_ABS_PATH, got {spec!r}")
            drive, _, rest = remote.partition(":\\")
            share = f"{drive}$"
            rdir, _, rname = rest.rpartition("\\")
            sha = hashlib.sha256(open(local, "rb").read()).hexdigest()
            smb_put(a.host, a.account_domain, a.account, password,
                    local, f"{share}\\{rdir}" if rdir else share, rname)
            print(f"[smb] uploaded {local} -> {remote} (local sha256 {sha})", file=sys.stderr)

        session = winrm.Session(f"http://{a.host}:5985/wsman",
                                auth=(user, password), transport="ntlm",
                                operation_timeout_sec=300, read_timeout_sec=360)
        script = a.command
        if a.script:
            prelude = ""
            for s in a.arg:
                k, _, v = s.partition("=")
                prelude += "$env:{} = '{}'\n".format(k, v.replace("'", "''"))
            script = prelude + open(a.script).read()
        if not script:
            script = "whoami; hostname"
        code, out, err = ps(session, script)
        if out:
            print(out)
        if err.strip():
            print(err, file=sys.stderr)
        rc = code
    finally:
        if a.keep_request:
            print(f"[sg] leaving request {sg.request_id} open", file=sys.stderr)
        else:
            sg.cleanup()
            print("[sg] access request checked in", file=sys.stderr)
    sys.exit(rc)


if __name__ == "__main__":
    main()
