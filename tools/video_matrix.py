#!/usr/bin/env python3
"""Drive every advertised video parameter combination through a live gateway.

The gateway's own Go tests already cover the handler; this exists to run the
same matrix against a real process, a real state file and a real HTTP stack,
because that is where the differences live — a config frozen by an older
release, a model entry that never had its ranges written, a proxy that rewrites
a body.

It never talks to the real upstream. Point it at an instance whose
`upstream.baseURL` is `tools/stub_upstream.py` and no credit is spent.

Usage:
    python tools/video_matrix.py --base http://127.0.0.1:18099 --password admin12345
"""

import argparse
import json
import sys
import urllib.error
import urllib.request

RATIOS = ["21:9", "16:9", "4:3", "1:1", "3:4", "9:16"]
DURATIONS = list(range(5, 16))
MODELS = [
    {"id": "minimax-h3-max", "upstream": "MiniMax-H3-Max", "resolutions": ["480P", "768P"]},
    {"id": "minimax-h3", "upstream": "MiniMax-H3", "resolutions": ["768P", "2K"]},
]

FAILURES: list[str] = []

# An environment that sets `http_proxy` would send a request to 127.0.0.1 out to
# the network, and the answer comes back as a 502 that reads exactly like the
# gateway refusing the call. Loopback is never proxied, here or in the gateway's
# own client.
OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))


def call(base, path, method="GET", body=None, token=None, timeout=60):
    data = json.dumps(body).encode() if body is not None else None
    request = urllib.request.Request(base + path, data=data, method=method)
    if data:
        request.add_header("content-type", "application/json")
    if token:
        request.add_header("authorization", "Bearer " + token)
    try:
        with OPENER.open(request, timeout=timeout) as response:
            raw = response.read()
            return response.status, (json.loads(raw) if raw else {})
    except urllib.error.HTTPError as error:
        raw = error.read()
        try:
            return error.code, json.loads(raw)
        except ValueError:
            return error.code, {"raw": raw.decode(errors="replace")}


def check(label, ok, detail=""):
    if ok:
        print(f"  ok   {label}")
    else:
        print(f"  FAIL {label}  {detail}")
        FAILURES.append(f"{label} {detail}")


def login(base, password):
    status, payload = call(base, "/admin/api/auth/login", "POST",
                           {"username": "admin", "password": password})
    if status != 200:
        sys.exit(f"login failed: {status} {payload}")
    return payload["token"]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--base", default="http://127.0.0.1:18099")
    parser.add_argument("--password", default="admin12345")
    args = parser.parse_args()
    base = args.base.rstrip("/")

    token = login(base, args.password)

    status, payload = call(base, "/admin/api/client-keys", "POST",
                           {"name": "matrix-key", "rpmLimit": 0, "maxConcurrent": 8}, token)
    if status != 200:
        sys.exit(f"client key failed: {status} {payload}")
    key = payload["key"]["key"]
    print(f"client key {key[:12]}…")

    print("\n1. the catalogue's own ranges")
    status, payload = call(base, "/admin/api/models", token=token)
    check("GET /admin/api/models returns 200", status == 200, f"status={status}")
    by_id = {item["id"]: item for item in payload.get("items", [])}
    for model in MODELS:
        entry = by_id.get(model["id"])
        if entry is None:
            check(f"{model['id']} is in the catalogue", False, "missing")
            continue
        check(f"{model['id']} resolutions = {model['resolutions']}",
              entry.get("resolutions") == model["resolutions"], str(entry.get("resolutions")))
        check(f"{model['id']} ratios = the panel's six",
              entry.get("ratios") == RATIOS, str(entry.get("ratios")))
        check(f"{model['id']} durations = 5-15",
              entry.get("durations") == DURATIONS, str(entry.get("durations")))
    hailuo = by_id.get("minimax-hailuo-2-3")
    if hailuo is not None:
        check("hailuo carries no ranges",
              not hailuo.get("ratios") and not hailuo.get("resolutions") and not hailuo.get("durations"),
              json.dumps({k: hailuo.get(k) for k in ("ratios", "resolutions", "durations")}))

    print("\n2. every advertised combination")
    turns = 0
    for model in MODELS:
        for ratio in RATIOS:
            for resolution in model["resolutions"]:
                for duration in DURATIONS:
                    turns += 1
                    status, payload = call(base, "/v1/videos/generations", "POST", {
                        "model": model["id"], "prompt": "一只猫在弹钢琴",
                        "ratio": ratio, "resolution": resolution, "duration": duration,
                    }, key)
                    label = f"{model['id']} {ratio} {resolution} {duration}s"
                    if status != 200:
                        check(label, False, f"status={status} {payload}")
                        continue
                    params = payload.get("params") or {}
                    expected = {"model": model["upstream"], "ratio": ratio,
                                "resolution": resolution, "duration": duration}
                    if params != expected:
                        check(label, False, f"params={params}")
                    elif "adjusted" in payload:
                        check(label, False, f"repaired {payload['adjusted']}")
    check(f"all {turns} combinations answered 200 with the values as sent", not FAILURES)

    print("\n3. values outside the range are repaired, not rejected")
    # Only the fields that actually changed are reported, which is the point of
    # listing them per case: a field left alone must not appear in `adjusted`.
    cases = [
        ("minimax-h3-max", "2K", 30, "768P", 15,
         {"resolution": ("2K", "768P"), "duration": ("30", "15")}),
        ("minimax-h3-max", "1080P", 3, "768P", 5,
         {"resolution": ("1080P", "768P"), "duration": ("3", "5")}),
        ("minimax-h3", "480P", 20, "768P", 15,
         {"resolution": ("480P", "768P"), "duration": ("20", "15")}),
        ("minimax-h3", "720P", 5, "768P", 5,
         {"resolution": ("720P", "768P")}),
    ]
    for model_id, resolution, duration, want_resolution, want_duration, want_adjusted in cases:
        status, payload = call(base, "/v1/videos/generations", "POST", {
            "model": model_id, "prompt": "一只猫在弹钢琴",
            "ratio": "16:9", "resolution": resolution, "duration": duration,
        }, key)
        label = f"{model_id} {resolution} {duration}s -> {want_resolution} {want_duration}s"
        if status != 200:
            check(label, False, f"status={status} {payload}")
            continue
        params = payload.get("params") or {}
        adjusted = {item["field"]: (item["requested"], item["used"]) for item in payload.get("adjusted", [])}
        ok = (params.get("resolution") == want_resolution
              and params.get("duration") == want_duration
              and adjusted == want_adjusted)
        check(label, ok, f"params={params} adjusted={adjusted} want {want_adjusted}")

    print("\n4. a ratio outside the panel")
    status, payload = call(base, "/v1/videos/generations", "POST", {
        "model": "minimax-h3", "prompt": "一只猫在弹钢琴",
        "ratio": "1.85:1", "resolution": "768P", "duration": 8,
    }, key)
    params = payload.get("params") or {}
    check("1.85:1 becomes 16:9 and says so",
          status == 200 and params.get("ratio") == "16:9"
          and {"field": "ratio", "requested": "1.85:1", "used": "16:9"} in (payload.get("adjusted") or []),
          f"params={params} adjusted={payload.get('adjusted')}")

    print("\n5. the unread model is left alone")
    status, payload = call(base, "/v1/videos/generations", "POST", {
        "model": "minimax-hailuo-2-3", "prompt": "一只猫在弹钢琴",
        "ratio": "5:4", "resolution": "1080P", "duration": 25,
    }, key)
    params = payload.get("params") or {}
    check("hailuo keeps what it was given",
          status == 200 and params.get("ratio") == "5:4" and params.get("resolution") == "1080P"
          and params.get("duration") == 25 and "adjusted" not in payload,
          f"status={status} params={params} adjusted={payload.get('adjusted')}")

    print()
    if FAILURES:
        print(f"{len(FAILURES)} failure(s)")
        for failure in FAILURES[:20]:
            print("  -", failure)
        sys.exit(1)
    print("all checks passed")


if __name__ == "__main__":
    main()
