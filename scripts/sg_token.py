#!/usr/bin/env python3
"""Log in to Safeguard and cache an API bearer token for the Linux-side helpers.

Most people do not need this. Publishing a wrapper from Windows with
scripts/publish-remoteapp.ps1 authenticates through safeguard-ps and checks the
jump-host password out internally, so no token ever touches the disk. This
script exists for the Linux path, where scripts/sg_winrm.py talks to the
Safeguard REST API directly and needs a bearer token to do it.

Logging in is two steps, which is why a plain "get a token" call does not exist:

  1. POST https://<appliance>/RSTS/oauth2/token
         {grant_type, username, password, scope: rsts:sts:primaryproviderid:<provider>}
     returns an STS access_token.
  2. POST https://<appliance>/service/core/v4/Token/LoginResponse
         {StsAccessToken: <that token>}
     exchanges it for the Safeguard UserToken the API actually accepts.

Usage:
  scripts/sg_token.py --appliance spp.example.com --username admin
  scripts/sg_token.py --appliance spp.example.com --check     # is the cached token still good?

The password is read with getpass and held only in memory. The token is written
to the token file with mode 0600 and is never printed; --check reports only who
the token belongs to. Tokens are short-lived, so expect to re-run this
occasionally -- sg_winrm.py will tell you when.

If your login requires secondary authentication, this script cannot complete it.
Use the safeguard-mcp login (https://github.com/OneIdentity/safeguard-mcp) or
safeguard-ps instead; both land a token in the same file if you point them at it.
"""
import argparse
import getpass
import os
import stat
import sys

import requests
import urllib3

urllib3.disable_warnings(urllib3.exceptions.InsecureRequestWarning)

# Searched in order. ~/sg-token.txt is supported because earlier versions of
# these scripts used it; new installs get ~/.safeguard/token.
TOKEN_FILE_CANDIDATES = ("~/.safeguard/token", "~/sg-token.txt")


def resolve_token_file(explicit=""):
    """Return the token file to use: explicit, then $SAFEGUARD_TOKEN_FILE, then
    the first candidate that already exists, else the preferred default."""
    if explicit:
        return os.path.expanduser(explicit)
    if os.environ.get("SAFEGUARD_TOKEN_FILE"):
        return os.path.expanduser(os.environ["SAFEGUARD_TOKEN_FILE"])
    for c in TOKEN_FILE_CANDIDATES:
        p = os.path.expanduser(c)
        if os.path.exists(p):
            return p
    return os.path.expanduser(TOKEN_FILE_CANDIDATES[0])


def load_token(explicit=""):
    """Read the cached token, or exit with advice on how to create one."""
    path = resolve_token_file(explicit)
    try:
        with open(path) as f:
            token = f.read().strip()
    except FileNotFoundError:
        sys.exit(
            f"No Safeguard token at {path}.\n"
            f"Create one with:  {sys.argv[0].rsplit('/', 1)[0]}/sg_token.py "
            f"--appliance <appliance> --username <you>"
        )
    if not token:
        sys.exit(f"Token file {path} is empty; re-run sg_token.py to refresh it.")
    return token


def write_token(path, token):
    """Write the token 0600, creating it with those permissions from the start
    so it is never briefly world-readable."""
    d = os.path.dirname(path)
    if d:
        os.makedirs(d, mode=0o700, exist_ok=True)
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w") as f:
        f.write(token + "\n")
    os.chmod(path, stat.S_IRUSR | stat.S_IWUSR)


def login(appliance, username, password, provider, verify):
    sts = requests.post(
        f"https://{appliance}/RSTS/oauth2/token",
        json={
            "grant_type": "password",
            "username": username,
            "password": password,
            "scope": f"rsts:sts:primaryproviderid:{provider}",
        },
        timeout=60,
        verify=verify,
    )
    if not sts.ok:
        # The RSTS error body is small and carries no secret, so showing it is
        # the quickest route to "wrong provider" vs "wrong password".
        sys.exit(f"Safeguard login failed (HTTP {sts.status_code}): {sts.text.strip()[:300]}")
    access = sts.json().get("access_token")
    if not access:
        sys.exit("Safeguard login returned no access_token.")

    resp = requests.post(
        f"https://{appliance}/service/core/v4/Token/LoginResponse",
        json={"StsAccessToken": access},
        timeout=60,
        verify=verify,
    )
    if not resp.ok:
        sys.exit(f"Token exchange failed (HTTP {resp.status_code}): {resp.text.strip()[:300]}")
    token = resp.json().get("UserToken")
    if not token:
        sys.exit("Token exchange returned no UserToken.")
    return token


def whoami(appliance, token, verify):
    r = requests.get(
        f"https://{appliance}/service/core/v4/Me",
        headers={"Authorization": f"Bearer {token}"},
        timeout=60,
        verify=verify,
    )
    return r


def main():
    p = argparse.ArgumentParser(
        description="Cache a Safeguard API bearer token for the Linux helper scripts."
    )
    p.add_argument("--appliance", default=os.environ.get("SAFEGUARD_APPLIANCE", ""),
                   help="SPP appliance hostname (or $SAFEGUARD_APPLIANCE)")
    p.add_argument("--username", default="", help="Safeguard user (prompted if omitted)")
    p.add_argument("--provider", default="local",
                   help="primary authentication provider id; 'local' for local users, "
                        "otherwise the external provider's id as shown in SPP")
    p.add_argument("--token-file", default="",
                   help="where to cache the token (or $SAFEGUARD_TOKEN_FILE)")
    p.add_argument("--password-stdin", action="store_true",
                   help="read the password from stdin instead of prompting; the only "
                        "non-interactive route. Pipe from a secret store, never from a file "
                        "in the repo")
    p.add_argument("--check", action="store_true",
                   help="report whether the cached token still works, and do not log in")
    p.add_argument("--ca-bundle", default="", help="CA bundle for appliance TLS verification")
    p.add_argument("--insecure", action="store_true",
                   help="skip appliance TLS verification (lab appliances with self-signed certs)")
    a = p.parse_args()

    if not a.appliance:
        p.error("--appliance is required (or set $SAFEGUARD_APPLIANCE)")
    verify = a.ca_bundle if a.ca_bundle else (not a.insecure)

    path = resolve_token_file(a.token_file)

    if a.check:
        token = load_token(a.token_file)
        r = whoami(a.appliance, token, verify)
        if r.ok:
            me = r.json()
            print(f"token at {path} is valid for "
                  f"{me.get('Name', '?')} on {a.appliance}")
            return 0
        print(f"token at {path} is not accepted (HTTP {r.status_code}); re-run without --check")
        return 1

    username = a.username or input("Safeguard user: ").strip()
    if a.password_stdin:
        password = sys.stdin.readline().rstrip("\n")
    else:
        password = getpass.getpass(f"Password for {username}@{a.appliance}: ")
    if not password:
        sys.exit("No password supplied.")

    token = login(a.appliance, username, password, a.provider, verify)
    del password

    r = whoami(a.appliance, token, verify)
    if not r.ok:
        sys.exit(f"Logged in but the token was rejected (HTTP {r.status_code}).")

    write_token(path, token)
    print(f"Token cached at {path} (mode 0600) for "
          f"{r.json().get('Name', username)} on {a.appliance}. It was not printed.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
