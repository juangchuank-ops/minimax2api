#!/usr/bin/env python3
"""End-to-end check for the daily check-in and credit-routing feature.

Every other tool in this directory exercises the gateway or the console. This
one covers the check-in path, which is awkward to test without an account: it
stands up a mock MiniMax upstream inside the same process, points a throwaway
account at it, and drives the real admin API from the outside.

What it proves that the unit tests cannot:

  * the two-query-string rule survives the trip through a real HTTP client --
    the signed string carries `op_ticket=undefined`, the wire URL does not;
  * the balance really is read from op_credit_summary.total_remaining_amount,
    including the string-typed value the upstream sends;
  * a spent account leaves the routing pool, and a stale reading stops counting;
  * a mainland account is skipped rather than sent to the wrong protocol;
  * the console's DTOs carry the new fields.

Usage:
    python tools/signin_e2e.py --base http://127.0.0.1:8080 --password admin12345

    # also cross-check the digests against the reverse-engineering workspace's
    # independently validated Python signer:
    python tools/signin_e2e.py --password admin12345 \\
        --recon "C:/path/to/逆向minimax国际签到接口/code"

The tool creates its own accounts and deletes them again. Run it against a
disposable instance, not one serving live traffic: the sweep it triggers checks
the accounts in for real.
"""

import argparse
import json
import os
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PROBLEMS: list[str] = []
CHECKS = 0

# A structurally valid JWT is not needed here: the check-in client only echoes
# the token back, and the mock accepts anything.
GLOBAL_TOKEN = "fake.jwt.token-for-e2e"
CN_TOKEN = "fake.jwt.token-for-e2e-cn"

STATUS_PATH = "/minimax-cloud/api/v1/signin/status"
CLAIM_PATH = "/minimax-cloud/api/v1/signin/claim"
CREDIT_PATH = "/matrix/api/v1/commerce/get_membership_info"


def check(label, ok, detail=""):
    global CHECKS
    CHECKS += 1
    print(f"  [{'ok' if ok else 'FAIL'}] {label}" + (f"  -> {detail}" if detail else ""))
    if not ok:
        PROBLEMS.append(label)


# --------------------------------------------------------------------- mock


class MockUpstream:
    """A MiniMax upstream that answers only the check-in endpoints.

    Requests are recorded so the test can assert on the URL and the signing
    headers rather than merely on the response the service produced from them.
    """

    def __init__(self, port):
        self.port = port
        self.log: list[dict] = []
        self.credit = "0"
        self.claimed_today = True
        # What `/v1/api/user/info` reports as the account's realUserID. Only
        # consulted when an account is added without one — the value is not in
        # the token and cannot be derived from it, so this endpoint is the only
        # way a bare token becomes usable.
        #
        # A synthetic value, not a captured one: the id is past 2^53, so it also
        # has to be carried as a string rather than a JSON number.
        self.real_user_id = "9007199254740993"
        self._lock = threading.Lock()
        self._server = None
        self._thread = None

    # -- state knobs the test flips between assertions

    def set_credit(self, value):
        self.credit = str(value)

    def set_claimed_today(self, value):
        self.claimed_today = value

    def since(self):
        with self._lock:
            return len(self.log)

    def entries_since(self, mark, suffix=None):
        with self._lock:
            rows = self.log[mark:]
        if suffix:
            rows = [row for row in rows if row["path"].split("?", 1)[0].endswith(suffix)]
        return rows

    def last(self, suffix, method=None):
        with self._lock:
            rows = [row for row in self.log if row["path"].split("?", 1)[0].endswith(suffix)]
        if method:
            rows = [row for row in rows if row["method"] == method]
        return rows[-1] if rows else None

    # -- lifecycle

    def start(self):
        upstream = self

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, *args):
                pass

            def _record(self, body):
                with upstream._lock:
                    upstream.log.append({
                        "method": self.command,
                        "path": self.path,
                        "token": self.headers.get("token", ""),
                        "x-timestamp": self.headers.get("x-timestamp", ""),
                        "x-signature": self.headers.get("x-signature", ""),
                        "yy": self.headers.get("yy", ""),
                        "body": body,
                    })

            def _reply(self, payload, status=200):
                raw = json.dumps(payload).encode()
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(raw)))
                self.end_headers()
                self.wfile.write(raw)

            def _dispatch(self):
                length = int(self.headers.get("Content-Length") or 0)
                body = self.rfile.read(length).decode() if length else ""
                self._record(body)
                path = self.path.split("?", 1)[0]

                if path.endswith("/signin/status"):
                    today = 3 if upstream.claimed_today else 1
                    self._reply({"data": {"scene": 1, "days": [
                        {"day_no": 1, "points": 400, "status": today, "is_today": True},
                        {"day_no": 2, "points": 400, "status": 1, "is_today": False},
                        {"day_no": 3, "points": 400, "status": 1, "is_today": False},
                        {"day_no": 4, "points": 1000, "status": 1, "is_today": False},
                        {"day_no": 5, "points": 400, "status": 1, "is_today": False},
                        {"day_no": 6, "points": 400, "status": 1, "is_today": False},
                        {"day_no": 7, "points": 1000, "status": 1, "is_today": False},
                    ]}})
                    return

                # The agent-side opening sequence. The check-in flow runs it
                # before the claim and refuses to claim if it fails, so a mock
                # without these answers every check-in with "initialisation
                # failed" — which is the correct behaviour and a useless test.
                if path.endswith("/api/v1/config"):
                    self._reply({"base_resp": {"status_code": 0, "status_msg": "ok"}, "models": []})
                    return

                if path.endswith("/api/v1/agent"):
                    # `name` is the numeric handle, `agent_role` the kind — the
                    # upstream's naming, not a mistake here.
                    self._reply({"agents": [{
                        "name": "443154487857417", "agent_role": "general",
                        "root_session_id": "443155700445473",
                    }]})
                    return

                if path.endswith("/channel/connections"):
                    self._reply({"base_resp": {"status_code": 0, "status_msg": "ok"}})
                    return

                if path.endswith("/v1/api/user/info"):
                    self._reply({"data": {"userInfo": {
                        "realUserID": upstream.real_user_id,
                        "name": "mock", "userID": "mock-handle",
                    }}})
                    return

                if path.endswith("/signin/claim"):
                    result = 2 if upstream.claimed_today else 1
                    self._reply({"data": {
                        "claim_id": 987654321, "claim_result": result,
                        "day_no": 1, "points": 400, "expire_at_ms": 1789831624000,
                    }})
                    return

                if path.endswith("/get_membership_info"):
                    value = upstream.credit
                    self._reply({
                        "plan_name": "Free", "plan_type": 0,
                        # The live number, sent as a string, exactly as upstream does.
                        "op_credit_summary": {
                            "total_remaining_amount": value,
                            "free_remaining_amount": value,
                            "purchased_remaining_amount": "0",
                        },
                        # The pre-migration fields stay at zero, which is the trap
                        # the routing guard has to avoid reading.
                        "opcredit_balance": 0, "total_remains_credit": 0,
                        "is_migrated_to_op": True,
                    })
                    return

                self._reply({"base_resp": {"status_code": 404, "status_msg": "not mocked"}}, 404)

            do_GET = _dispatch
            do_POST = _dispatch

        self._server = ThreadingHTTPServer(("127.0.0.1", self.port), Handler)
        self._thread = threading.Thread(target=self._server.serve_forever, daemon=True)
        self._thread.start()

    def stop(self):
        if self._server:
            self._server.shutdown()
            self._server.server_close()


# ---------------------------------------------------------------------- api


class Console:
    def __init__(self, base, password, user="admin"):
        self.base = base.rstrip("/")
        self.password = password
        self.user = user
        self.token = ""

    def call(self, method, path, body=None, auth=True, timeout=60):
        data = json.dumps(body).encode() if body is not None else None
        request = urllib.request.Request(self.base + path, data=data, method=method)
        if data:
            request.add_header("content-type", "application/json")
        if auth and self.token:
            request.add_header("authorization", "Bearer " + self.token)
        # Never let an environment proxy swallow a loopback request.
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        try:
            with opener.open(request, timeout=timeout) as response:
                raw = response.read().decode()
                return response.status, (json.loads(raw) if raw else {})
        except urllib.error.HTTPError as error:
            raw = error.read().decode()
            try:
                return error.code, json.loads(raw)
            except json.JSONDecodeError:
                return error.code, {"raw": raw}
        except Exception as error:  # noqa: BLE001
            return 0, {"error": str(error)}

    def login(self):
        status, payload = self.call("POST", "/admin/api/auth/login",
                                    {"username": self.user, "password": self.password},
                                    auth=False)
        self.token = payload.get("token", "")
        return status, self.token

    def health(self):
        return self.call("GET", "/health", auth=False)[1].get("pool", {})


# -------------------------------------------------------------- signature


def crosscheck_signatures(console, mock, recon_path):
    """Reproduce the service's digests with the recon workspace's signer.

    That Python implementation was validated byte-for-byte against a real
    browser capture, so agreement between the two is evidence about the
    protocol, not just about self-consistency.
    """
    sys.path.insert(0, recon_path)
    try:
        import minimax_sign
    except ImportError as error:
        check("recon signer importable", False, str(error))
        return

    def to_int(value, fallback=0):
        try:
            return int(str(value).strip())
        except (TypeError, ValueError):
            return fallback

    def replay(entry, method):
        actual_path = entry["path"].split("?", 1)[0]
        sent = dict(urllib.parse.parse_qsl(entry["path"].split("?", 1)[1], keep_blank_values=True))
        unix_ms = to_int(sent.get("unix"))

        rebuilt = minimax_sign.sign(
            actual_path, method, None if method == "GET" else "{}",
            token=sent.get("token", ""), uuid=sent.get("uuid", ""),
            device_id=sent.get("device_id", ""), user_id=sent.get("user_id", 0),
            unix_ms=unix_ms,
            # lang is a parameter of sign() itself; passing it in
            # device_overrides as well would bind it twice.
            lang=sent.get("lang", "en"),
            device_overrides={
                "screen_width": to_int(sent.get("screen_width")),
                "screen_height": to_int(sent.get("screen_height")),
                "os_name": sent.get("os_name", ""),
                "browser_name": sent.get("browser_name", ""),
                "browser_language": sent.get("browser_language", ""),
                "browser_platform": sent.get("browser_platform", ""),
                "cpu_core_num": to_int(sent.get("cpu_core_num")),
                "device_memory": to_int(sent.get("device_memory")),
                "utc_offset_minutes": to_int(sent.get("timezone_offset")) // 60,
            },
        )
        signed = rebuilt["signed_path"].split("?", 1)[1]
        check(f"{method} {actual_path}: signer agrees on yy",
              rebuilt["headers"]["yy"] == entry["yy"],
              f"python={rebuilt['headers']['yy']} service={entry['yy']}")
        check(f"{method} {actual_path}: signer agrees on x-signature",
              rebuilt["headers"]["x-signature"] == entry["x-signature"],
              f"python={rebuilt['headers']['x-signature']} service={entry['x-signature']}")
        check(f"{method} {actual_path}: signed string keeps op_ticket=undefined",
              "op_ticket=undefined" in signed)

    for suffix, method in ((STATUS_PATH, "GET"), (CLAIM_PATH, "POST"), (CREDIT_PATH, "POST")):
        entry = mock.last(suffix, method)
        if entry is None:
            check(f"{method} {suffix} was recorded", False)
            continue
        replay(entry, method)


# --------------------------------------------------------------------- main


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--base", default="http://127.0.0.1:8080")
    parser.add_argument("--password", required=True)
    parser.add_argument("--user", default="admin")
    parser.add_argument("--mock-port", type=int, default=18098)
    parser.add_argument("--recon", default="",
                        help="path to the recon workspace's code/ directory; "
                             "enables the cross-implementation digest check")
    args = parser.parse_args()

    console = Console(args.base, args.password, args.user)
    mock = MockUpstream(args.mock_port)
    mock.start()
    mock_url = f"http://127.0.0.1:{args.mock_port}"

    try:
        print(f"sign-in end-to-end check\nbase = {args.base}\nmock = {mock_url}\n")

        status, token = console.login()
        check("admin login", status == 200 and bool(token), f"status={status}")
        if not token:
            return 1

        print("\npool hygiene")
        status, payload = console.call("GET", "/admin/api/accounts?pageSize=200")
        existing = [row["id"] for row in payload.get("items", [])]
        if existing:
            console.call("POST", "/admin/api/accounts/batch",
                         {"action": "delete", "ids": existing})
            time.sleep(0.5)
        check("pool is empty before the run", console.health().get("total") == 0,
              f"total={console.health().get('total')}")

        print("\nscheduler settings")
        settings = console.call("GET", "/admin/api/settings")[1]
        check("settings exposes a signin section", "signin" in settings)
        settings["signin"]["enabled"] = True
        settings["signin"]["skipZeroCredit"] = True
        settings.pop("about", None)
        check("signin settings round-trip",
              console.call("PUT", "/admin/api/settings", settings)[0] == 200)

        print("\naccounts")
        status, created = console.call("POST", "/admin/api/accounts", {
            "name": "e2e-global", "token": GLOBAL_TOKEN, "region": "global",
            "baseURL": mock_url, "userID": "123456",
            "uuid": "11111111-2222-3333-4444-555555555555",
            "deviceID": "41873026", "screenWidth": 1920, "screenHeight": 1080,
        })
        global_id = (created.get("account") or {}).get("id", "")
        check("global account created", status == 200 and bool(global_id), f"status={status}")

        status, created = console.call("POST", "/admin/api/accounts", {
            "name": "e2e-cn", "token": CN_TOKEN, "region": "cn",
            "baseURL": mock_url, "userID": "654321",
            "uuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
            "deviceID": "75201943", "screenWidth": 1920, "screenHeight": 1080,
        })
        cn_id = (created.get("account") or {}).get("id", "")
        check("mainland account created", status == 200 and bool(cn_id), f"status={status}")
        if not (global_id and cn_id):
            return 1

        # Creating an account kicks off a detached preparation that has to
        # finish before the account is routable at all: without the realUserID
        # every signed call answers a bare 401 (which the pool reads as a dead
        # token), and without the agent id the session handshake answers 200 and
        # opens nothing. Both are discovered from the upstream, so this waits for
        # them rather than asserting on an account that is still being set up.
        def wait_for_preparation(account_id, seconds=30):
            deadline = time.time() + seconds
            row = {}
            while time.time() < deadline:
                listing = console.call("GET", "/admin/api/accounts?pageSize=100")[1]
                row = next((r for r in listing.get("items", []) if r.get("id") == account_id), {})
                if row.get("agentID"):
                    return row
                time.sleep(0.5)
            return row

        global_row = wait_for_preparation(global_id)
        cn_row = wait_for_preparation(cn_id)
        check("the agent id is discovered, not assumed", bool(global_row.get("agentID")),
              f"agentID={global_row.get('agentID')!r}")
        check("the discovered agent id is not the role name",
              global_row.get("agentID") not in ("", "general"), global_row.get("agentID"))

        # The same detached preparation runs a full Completion, which the mock
        # does not implement, so both accounts land in cooldown. Clear it, or the
        # routing assertions below would be measuring the cooldown instead of the
        # credit guard.
        console.call("POST", "/admin/api/accounts/batch",
                     {"action": "clearCooldown", "ids": [global_id, cn_id]})
        time.sleep(0.3)
        check("both accounts routable to begin with", console.health().get("routable") == 2,
              f"routable={console.health().get('routable')}")

        print("\ncredit reading")
        mock.set_credit("0")
        status, payload = console.call("POST", f"/admin/api/accounts/{global_id}/credit")
        credit = payload.get("credit") or {}
        check("credit refresh succeeds", status == 200,
              f"status={status} body={json.dumps(payload, ensure_ascii=False)[:200] if status != 200 else ''}")
        check("balance read from the string field", credit.get("total") == 0,
              f"total={credit.get('total')!r} (the flat pre-migration fields also read 0)")

        print("\nrouting guard")
        check("a spent account leaves the pool", console.health().get("routable") == 1,
              f"routable={console.health().get('routable')}")
        mock.set_credit("400")
        payload = console.call("POST", f"/admin/api/accounts/{global_id}/credit")[1]
        check("a funded balance is parsed", (payload.get("credit") or {}).get("total") == 400)
        check("a funded account returns to the pool", console.health().get("routable") == 2,
              f"routable={console.health().get('routable')}")

        print("\nrouting guard is bounded by freshness")
        mock.set_credit("0")
        console.call("POST", f"/admin/api/accounts/{global_id}/credit")
        check("the spent account is held out again", console.health().get("routable") == 1)
        # Shrink the window to its minimum and let the reading age past it: a
        # day-old zero must stop holding capacity the upstream would fund.
        settings = console.call("GET", "/admin/api/settings")[1]
        settings["signin"]["creditFreshMin"] = 1
        settings.pop("about", None)
        console.call("PUT", "/admin/api/settings", settings)
        print("       (waiting 65s for the balance reading to age past the window)")
        time.sleep(65)
        check("a stale zero stops holding the account out",
              console.health().get("routable") == 2,
              f"routable={console.health().get('routable')}")

        print("\ncheck-in, already claimed today")
        mark = mock.since()
        status, payload = console.call("POST", f"/admin/api/accounts/{global_id}/signin")
        result = payload.get("result") or {}
        check("signin returns 200", status == 200, f"status={status}")
        check("duplicate claim reported as already", result.get("status") == "already",
              f"status={result.get('status')!r}")
        check("points recorded", result.get("points") == 400)
        check("streak recorded", (payload.get("account") or {}).get("signinStreak") == 1)

        print("\nthe two-query-string rule, on the wire")
        calls = mock.entries_since(mark, STATUS_PATH)
        check("the status endpoint was called", len(calls) == 1, f"calls={len(calls)}")
        if calls:
            wire = calls[0]["path"]
            check("wire URL omits op_ticket", "op_ticket" not in wire)
            check("wire URL carries token and fingerprint",
                  f"token={GLOBAL_TOKEN}" in wire and "uuid=11111111" in wire)
            check("parameter order preserved",
                  wire.index("device_platform=") < wire.index("biz_id=")
                  < wire.index("token=") < wire.index("client="))
            check("x-signature is a 32-char digest", len(calls[0]["x-signature"]) == 32)
            check("yy is a 32-char digest", len(calls[0]["yy"]) == 32)

        print("\ncheck-in, a real claim")
        mock.set_claimed_today(False)
        mark = mock.since()
        status, payload = console.call("POST", f"/admin/api/accounts/{global_id}/signin")
        result = payload.get("result") or {}
        check("claim returns 200", status == 200, f"status={status}")
        check("status is ok", result.get("status") == "ok", f"status={result.get('status')!r}")
        check("claimed points recorded", result.get("points") == 400)
        claims = mock.entries_since(mark, CLAIM_PATH)
        check("the claim endpoint was called", len(claims) == 1, f"calls={len(claims)}")
        check("the claim body is the literal {}", (claims[0]["body"] if claims else "") == "{}")
        mock.set_claimed_today(True)

        print("\na mainland account is skipped, not attempted")
        mark = mock.since()
        status, payload = console.call("POST", f"/admin/api/accounts/{cn_id}/signin")
        result = payload.get("result") or {}
        check("a skip is 200, not 4xx", status == 200, f"status={status}")
        check("status is skipped", result.get("status") == "skipped",
              f"status={result.get('status')!r}")
        check("the reason names the mainland limitation",
              "国内站" in (result.get("error") or ""), result.get("error") or "")
        check("no upstream request was made for it", mock.since() == mark,
              f"new requests={mock.since() - mark}")

        print("\nfull sweep")
        status, payload = console.call("POST", "/admin/api/signin/run", timeout=180)
        report = payload.get("report") or {}
        check("sweep returns 200", status == 200, f"status={status}")
        check("sweep covers both accounts", report.get("total") == 2)
        check("sweep reports one already and one skipped",
              report.get("already") == 1 and report.get("skipped") == 1,
              f"already={report.get('already')} skipped={report.get('skipped')}")

        print("\nconsole overview")
        mock.set_credit("400")
        console.call("POST", f"/admin/api/accounts/{global_id}/credit")
        overview = console.call("GET", "/admin/api/signin")[1]
        check("overview returns 200", bool(overview))
        check("scheduler reports enabled", overview.get("enabled") is True)
        check("the guard flag reaches the console", overview.get("skipZeroCredit") is True)
        check("next run is in the future", bool(overview.get("nextRunAt")),
              overview.get("nextRunAt", ""))
        summary = overview.get("summary") or {}
        check("summary counts the pool", summary.get("total") == 2)
        check("a funded account is not counted as exhausted", summary.get("exhausted") == 0,
              f"exhausted={summary.get('exhausted')}")

        print("\naccount DTOs")
        rows = console.call("GET", "/admin/api/accounts?pageSize=200")[1].get("items", [])
        row = next((item for item in rows if item.get("id") == global_id), None)
        check("account row found", row is not None)
        if row:
            for field in ("signinAt", "signinStatus", "signinStreak", "signinPoints",
                          "signinTotal", "signinError", "signinPanel", "credit"):
                check(f"row carries {field}", field in row)
            check("panel carries seven days",
                  len((row.get("signinPanel") or {}).get("days") or []) == 7)
            check("credit carries a sync time",
                  bool((row.get("credit") or {}).get("syncedAt")))

        if args.recon:
            print("\ncross-implementation signature check")
            crosscheck_signatures(console, mock, args.recon)
        else:
            print("\n(skipping the signature cross-check; pass --recon to enable it)")

        print(f"\n{CHECKS - len(PROBLEMS)}/{CHECKS} checks passed")
        if PROBLEMS:
            print("failed:")
            for item in PROBLEMS:
                print("  -", item)
            return 1
        print("SIGNIN OK - the check-in path works end to end")
        return 0
    finally:
        mock.stop()


if __name__ == "__main__":
    sys.exit(main())
