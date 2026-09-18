#!/usr/bin/env python3
"""Scan the working tree and the entire git history for committed credentials.

Why history, not just the tree: this repository once shipped a `config.json`
holding a live MiniMax JWT. Removing the file and rewriting history was only
half the job -- `git filter-repo` rewrites the commits it is told about, and a
secret sitting in a *second* file survives the rewrite untouched. The only way
to know is to walk every blob that is still reachable.

What it does NOT do: decide whether a hit is real. A hand-built test token and
a live credential look identical to a regex, so the tool prints every hit
(redacted) with its path and blob, and exits non-zero. Read the output.

Usage:
    python tools/secret_scan.py                 # tree + history
    python tools/secret_scan.py --tree-only      # fast, pre-commit
    python tools/secret_scan.py --quiet          # only print hits

Exit code is 1 when anything matched, so it can gate a push.
"""

import argparse
import os
import re
import subprocess
import sys

# Every pattern is a *shape*, never a literal secret. A tool that embeds the
# value it hunts for is itself a leak.
#
# Fields: (name, compiled pattern, value group index or None, apply the
# placeholder filter?). The group index lets the filter inspect the credential
# itself; without it the filter falls back to the whole match.
PATTERNS = [
    # A JWT has three dot-separated base64url segments and always starts with
    # the base64 of `{"alg"`, i.e. "eyJ".
    #
    # The signature segment must be at least 16 characters: a real HS256 token
    # signs with 32 bytes -> 43 base64url chars, while hand-written test stubs
    # put a literal word there (`….test`, `….signature`). Requiring a plausible
    # length drops the stubs without hiding any credential that could actually
    # be used.
    #
    # Never placeholder-filtered: base64 routinely contains substrings like
    # "test", so filtering it would hide real tokens to suppress noise.
    (
        "jwt",
        re.compile(rb"eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{16,}"),
        None,
        False,
    ),
    # Provider-prefixed keys. The prefix is the whole point: it is what secret
    # scanners and the providers themselves key off. These need the filter
    # because documentation writes them as `sk-your-...-key`.
    ("sk-key", re.compile(rb"\bsk-[A-Za-z0-9_-]{16,}"), None, True),
    ("github-token", re.compile(rb"\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{30,}"), None, True),
    ("slack-token", re.compile(rb"\bxox[abprs]-[A-Za-z0-9-]{10,}"), None, True),
    ("aws-access-key", re.compile(rb"\b(?:AKIA|ASIA)[0-9A-Z]{16}\b"), None, True),
    # PEM blocks: a private key is a credential regardless of how it got here.
    ("private-key", re.compile(rb"-----BEGIN [A-Z ]*PRIVATE KEY-----"), None, False),
    # An assignment of a literal to a secret-looking key. The value is captured
    # so the placeholder filter can reject documentation examples -- without
    # that filter this pattern alone drowns the report in i18n strings and
    # `"password": "your-password"`.
    (
        "secret-assignment",
        re.compile(
            rb"""(?i)["']?(?:password|passwd|secret|api[_-]?key|apikey|access[_-]?token|"""
            rb"""refresh[_-]?token|auth[_-]?token|private[_-]?key|client[_-]?secret|"""
            rb"""webui_password)["']?\s*[:=]\s*["']([^"'\s$]{6,})["']"""
        ),
        1,
        True,
    ),
]

# Values that are obviously not credentials, so the report stays readable
# enough that a reader actually reads it.
#
# `admin12345` is this repository's own documented test password (every tool
# defaults to it), and the fixture words cover the obvious cases in test code.
# Suppressing these is not hiding anything: none of them is a credential, and
# flagging them on every run would train the reader to ignore the output --
# which is worse than not scanning at all.
PLACEHOLDER_ANYWHERE = re.compile(
    r"(?i)your|example|placeholder|changeme|change-me|redacted|xxxx|\*\*\*|\.\.\.|^<|>$|admin12345"
)

# Fixture-looking *prefixes*. Anchored at the start because these words are
# common inside real values (`contested-key`), where a substring match would
# silently drop a hit.
PLACEHOLDER_PREFIX = re.compile(
    r"(?i)^(?:wrong|whatever|dummy|fake|sample|test|invalid|none|nope|notreal|definitely)"
)

# A value that just restates its own field name. i18n files are full of these
# (`password: "Password"`, `token: "Token"`), and they are labels, not secrets.
PLACEHOLDER_EXACT = re.compile(
    r"(?i)^(?:password|passwd|secret|token|api[_-]?key|apikey|username|email|user)$"
)

# A value carrying non-ASCII is a UI string, not a credential.
NON_ASCII = re.compile(r"[^\x00-\x7f]")


def is_placeholder(value: bytes) -> bool:
    text = value.decode("utf-8", "replace")
    return bool(
        NON_ASCII.search(text)
        or PLACEHOLDER_ANYWHERE.search(text)
        or PLACEHOLDER_PREFIX.match(text)
        or PLACEHOLDER_EXACT.match(text)
    )


def run(args, **kwargs):
    return subprocess.run(args, capture_output=True, **kwargs)


def redact(raw: bytes, keep: int = 8) -> str:
    """Show enough to recognise the value, never enough to use it.

    The first bytes of a prefixed key (the provider and a hint of the body) are
    what a human needs to tell "that is my production key" from "that is the
    example in the README".
    """
    text = raw.decode("utf-8", "replace")
    if len(text) <= keep * 2:
        return text
    return f"{text[:keep]}…{text[-4:]} ({len(text)} chars)"


def scan_blob(blob: bytes, label: str):
    for name, pattern, value_group, filtered in PATTERNS:
        for match in pattern.finditer(blob):
            if filtered:
                subject = match.group(value_group) if value_group else match.group(0)
                if is_placeholder(subject):
                    continue
            yield name, label, redact(match.group(0))


def worktree_files():
    """Every file git would actually consider: tracked, plus untracked and not
    ignored.

    Walking the directory instead would scan `.workbuddy-ai/` and other
    ignored scratch -- including the notes that document the leak, which would
    then be reported as if they were committed.
    """
    listing = run(["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"])
    if listing.returncode == 0:
        return [p for p in listing.stdout.decode("utf-8", "replace").split("\0") if p]
    # Not a git repository: fall back to a walk with the usual exclusions.
    ignore = {".git", "node_modules", "dist", ".venv", "__pycache__"}
    found = []
    for root, dirs, files in os.walk("."):
        dirs[:] = [d for d in dirs if d not in ignore]
        found.extend(os.path.join(root, name) for name in files)
    return found


def scan_tree():
    for path in worktree_files():
        try:
            if os.path.getsize(path) > 8 * 1024 * 1024:
                continue
            with open(path, "rb") as handle:
                blob = handle.read()
        except OSError:
            continue
        yield from scan_blob(blob, f"worktree:{os.path.relpath(path, '.')}")


def scan_history():
    """Walk every blob reachable from any ref, with its last known path.

    `--objects` prints `<sha> <path>`, but a blob can be listed once with no
    path (it was reached through a tree that the listing had already covered),
    so the path map is filled in as we go and missing ones are reported as
    unknown rather than dropped.
    """
    listing = run(["git", "rev-list", "--objects", "--all"])
    if listing.returncode != 0:
        raise SystemExit("git rev-list failed: not a git repository?")

    paths = {}
    for line in listing.stdout.decode("utf-8", "replace").splitlines():
        sha, _, path = line.partition(" ")
        paths[sha] = path

    scanned = 0
    for sha, path in paths.items():
        kind = run(["git", "cat-file", "-t", sha]).stdout.strip()
        if kind != b"blob":
            continue
        blob = run(["git", "cat-file", "blob", sha]).stdout
        scanned += 1
        yield from scan_blob(blob, f"history:{path or '<unknown>'}@{sha[:12]}")
    print(f"scanned {scanned} historical blob(s)")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tree-only", action="store_true",
                        help="skip history; fast enough for a pre-commit hook")
    parser.add_argument("--quiet", action="store_true",
                        help="suppress progress lines")
    args = parser.parse_args()

    hits = []
    if not args.quiet:
        print("scanning the working tree")
    hits.extend(scan_tree())
    if not args.tree_only:
        if not args.quiet:
            print("scanning every blob reachable from any ref")
        hits.extend(scan_history())

    # The same blob shows up under several commits; one line per distinct
    # finding is what a human can actually read.
    unique = sorted(set(hits))

    if not unique:
        print("\nCLEAN - no credential-shaped strings found")
        return 0

    print(f"\n{len(unique)} match(es) - redacted, review each one:\n")
    for name, where, sample in unique:
        print(f"  [{name}] {where}\n      {sample}")

    print(
        "\nA match is not proof. Hand-built test tokens and documentation\n"
        "examples look exactly like live credentials. What matters:\n"
        "  * if it is live, revoke it first - that is what actually closes the\n"
        "    hole. Rewriting history is secondary, and force-pushed objects\n"
        "    stay reachable by SHA until GitHub Support runs a GC.\n"
        "  * `git filter-repo` only rewrites what you point it at. Grep every\n"
        "    file, not just the one you found the secret in.\n"
    )
    return 1


if __name__ == "__main__":
    sys.exit(main())
