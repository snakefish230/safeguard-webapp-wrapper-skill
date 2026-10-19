#!/usr/bin/env python3
"""Mock Entra sign-in reproducing the two OTP faults seen on portal.azure.com.

The verification-code page deliberately carries BOTH the OTP box and the
"#signInAnotherWay" link, as the real page does. A wrapper that clicks the link
unconditionally takes a detour through the method picker; this records it.

The first code submitted is always rejected, so the retry path is exercised too.
Results are appended as JSON to argv[2].
"""
import json
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer

PORT = int(sys.argv[1])
OUT = sys.argv[2]
state = {"detour": False, "codes": [], "accepted": False, "user": "", "password": "", "submits": 0}

PAGE = """<!doctype html><html><body>
<div id="v_user"><input name="loginfmt" data-id="i0116">
 <button data-id="idSIButton9" onclick="u=val('loginfmt');show('pass')">Next</button></div>

<div id="v_pass" hidden><input type="password" name="passwd">
 <button data-id="idBtn_Back" onclick="show('user')">Back</button>
 <button data-id="idSIButton9" onclick="signin()">Sign in</button>
 <div id="passwordError" hidden>Your account or password is incorrect.</div></div>

<div id="v_push" hidden><div>Approve a sign in request</div>
 <a data-id="signInAnotherWay" href="#" onclick="detour();show('pick')">I can&#39;t use my app right now</a></div>

<div id="v_pick" hidden>
 <div data-value="PhoneAppNotification">Approve a request</div>
 <div data-value="PhoneAppOTP" onclick="show('otp')">Use a verification code</div></div>

<div id="v_otp" hidden>
 <input data-id="idTxtBx_SAOTCC_OTC" name="otc" oninput="clearErr()">
 <a data-id="signInAnotherWay" href="#" onclick="detour();show('pick')">I can&#39;t use my app right now</a>
 <button data-id="idBtn_Back" onclick="show('pass')">Back</button>
 <button data-id="idSubmit_SAOTCC_Continue" onclick="verify()">Verify</button>
 <div id="idSpan_SAOTCC_Error_OTC" hidden>The code you entered isn&#39;t valid.</div></div>

<div id="v_kmsi" hidden><button data-id="idBtn_Back" onclick="show('done')">No</button>
 <button data-id="idSIButton9" onclick="show('done')">Yes</button></div>

<div id="v_done" hidden><h1 id="welcome">Portal</h1></div>
<script>
var u="",p="";
// ?push=1 makes the tenant default to push approval, so the wrapper must take
// the "sign in another way" detour instead of skipping it.
var PUSH = location.search.indexOf('push=1') >= 0;
// ?stubborn=1 makes the Sign in button ignore programmatic clicks, the way a
// page that re-renders between hit-test and dispatch swallows them. Only a real
// Enter in the password field gets through, which is the user-reported symptom.
var STUBBORN = location.search.indexOf('stubborn=1') >= 0;
// ?badpass=1 rejects the password, so the Enter fallback must NOT fire again.
var BADPASS = location.search.indexOf('badpass=1') >= 0;
function signin(ev){
  if (STUBBORN && !window.__viaEnter) return;   // click swallowed
  p = val('passwd');
  fetch('/rec?submits=1');
  if (BADPASS) { document.getElementById('passwordError').hidden = false; return; }
  show(PUSH?'push':'otp');
}
document.addEventListener('keydown', function(e){
  if (e.key !== 'Enter') return;
  var a = document.activeElement;
  if (!a || a.name !== 'passwd') return;
  window.__viaEnter = true; signin(); window.__viaEnter = false;
});
function val(n){return document.getElementsByName(n)[0].value;}
// The real page swaps views out of the DOM, so ids like #idSIButton9 and
// #idBtn_Back are unique at any moment. Only the active view carries its real
// id here; the rest are parked under a suffix.
function show(v){
  ['user','pass','push','pick','otp','kmsi','done'].forEach(function(n){
    document.getElementById('v_'+n).hidden = (n!==v);
  });
  document.querySelectorAll('[data-id]').forEach(function(el){
    el.id = el.dataset.id + '_parked';
  });
  document.querySelectorAll('#v_'+v+' [data-id]').forEach(function(el){
    el.id = el.dataset.id;
  });
}
function detour(){ fetch('/rec?detour=1'); }
// Real sign-in pages drop the "invalid code" message as soon as you retype, so
// a stale error must not linger into the next attempt.
function clearErr(){ document.getElementById('idSpan_SAOTCC_Error_OTC').hidden=true; }
function verify(){
  var box=document.getElementById('idTxtBx_SAOTCC_OTC'), c=box.value;
  fetch('/rec?code='+encodeURIComponent(c)).then(r=>r.text()).then(function(t){
    if(t==='ok'){ fetch('/rec?u='+encodeURIComponent(u)+'&p='+encodeURIComponent(p))
                    .then(function(){show('kmsi');}); }
    else { document.getElementById('idSpan_SAOTCC_Error_OTC').hidden=false; box.value=''; }
  });
}
show('user');
</script></body></html>"""


class H(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def do_GET(self):
        # Each scenario needs a clean slate: the "reject the first code" rule is
        # counted per run, not per process.
        if self.path.startswith("/reset"):
            state.update(detour=False, codes=[], accepted=False, user="", password="", submits=0)
            self.send_response(200)
            self.send_header("Content-Type", "text/plain")
            self.end_headers()
            self.wfile.write(b"reset")
            return
        if self.path.startswith("/rec"):
            from urllib.parse import parse_qs, urlparse
            q = parse_qs(urlparse(self.path).query)
            body = "ok"
            if "detour" in q:
                state["detour"] = True
            if "submits" in q:
                state["submits"] += 1
            if "u" in q:
                state["user"] = q["u"][0]
                state["password"] = q.get("p", [""])[0]
            if "code" in q:
                state["codes"].append(q["code"][0])
                # Reject the first code, accept any later one.
                if len(state["codes"]) == 1:
                    body = "no"
                else:
                    state["accepted"] = True
            with open(OUT, "w") as f:
                json.dump(state, f)
            self.send_response(200)
            self.send_header("Content-Type", "text/plain")
            self.end_headers()
            self.wfile.write(body.encode())
            return
        self.send_response(200)
        self.send_header("Content-Type", "text/html")
        self.end_headers()
        self.wfile.write(PAGE.encode())


HTTPServer(("127.0.0.1", PORT), H).serve_forever()
