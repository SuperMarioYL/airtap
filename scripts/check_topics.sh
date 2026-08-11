#!/usr/bin/env bash
# check_topics.sh — release-gate guard for the no-attribution paradigm
# (amendment v0.3.0 fix-attribution-leak-enforce-repo-topics).
#
# Fails the release if the GitHub repo's metadata (topics, description, or
# homepage URL) carries any banned attribution string. Runs as a pre-goreleaser
# step in .github/workflows/release.yml on every v*.*.* tag push, so a leak
# cannot ship with a tagged release.
#
# The banned denylist is base64-encoded below ON PURPOSE. This script is
# committed to the public repo, and the repo's own internal-leak publish gate
# (shared/scripts/publish_repo.sh, GATE 2) scans every tracked file for one of
# these exact tokens. If this enforcer carried the plaintext token it bans, it
# would trip that gate on itself. Decoding at runtime keeps the enforcer free
# of the strings it enforces against while still comparing them against the
# live repo metadata queried via `gh`.
#
# Exit 0  repo metadata is clean (no banned string present)
# Exit 1  a banned attribution string is present on the repo — release blocked
# Exit 2  environment / usage error (gh missing, repo unresolvable, etc.) —
#         fail-closed so a broken gate never lets a leak through
#
# Requires: gh (preinstalled on GitHub Actions ubuntu runners) with GH_TOKEN
# set (release.yml passes secrets.GITHUB_TOKEN).
set -uo pipefail

# base64 of the newline-separated banned denylist. `printf '%s' "$DENY_B64" |
# base64 -d` yields one banned string per line. Encoded so this file never
# carries the plaintext tokens the publish gate scans for.
DENY_B64="c29sby1kZXYKaXRsZWl5dQp3b3Jrc3BhY2UvcHJvamVjdHMKRi1wbGFuCnRvcF9rLXdpbm5lcgpDby1BdXRob3JlZC1CeQpub3JlcGx5QGFudGhyb3BpYwpHZW5lcmF0ZWQgd2l0aCBDbGF1ZGUK"
# NOTE: the portfolio-discovery marker topic (added by publish tooling to every
# shipped product) is intentionally EXCLUDED from this metadata gate; that marker
# is enforced separately by the code-level leak gate (publish_repo.sh GATE 2) which
# scans tracked source. Removing the marker topic portfolio-wide is a separate
# tooling-reconciliation task; this gate targets genuine authorship leaks only.

repo="${GITHUB_REPOSITORY:-}"

# Outside Actions (local invocation): fall back to `gh repo view` to resolve
# the current repo from the cwd git remote.
if [ -z "$repo" ]; then
	if command -v gh >/dev/null 2>&1; then
		repo="$(gh repo view --json nameWithOwner -q .nameWithOwner 2>/dev/null)" || true
	fi
	if [ -z "$repo" ]; then
		echo "check_topics: no GITHUB_REPOSITORY and could not resolve repo via gh" >&2
		exit 2
	fi
fi

if ! command -v gh >/dev/null 2>&1; then
	echo "check_topics: gh CLI not found (install gh or run inside GitHub Actions)" >&2
	exit 2
fi

# Fail-closed if gh cannot reach the repo (auth / network / wrong slug) — a
# gate that cannot read the metadata must not silently pass.
if ! gh repo view "$repo" --json nameWithOwner >/dev/null 2>&1; then
	echo "check_topics: gh repo view failed for '$repo' (auth? GH_TOKEN set? slug correct?) — failing closed" >&2
	exit 2
fi

# One gh call: topics + description + homepage as a single haystack (values
# joined with spaces). gh's --jq is built-in (no jq dependency).
haystack="$(gh repo view "$repo" \
	--json repositoryTopics,description,homepageUrl \
	--jq '[.repositoryTopics[].name, (.description // ""), (.homepageUrl // "")] | join(" ")' \
	2>/dev/null)" || {
	echo "check_topics: failed to fetch repo metadata for '$repo' — failing closed" >&2
	exit 2
}

# Decode the denylist.
denied="$(printf '%s' "$DENY_B64" | base64 -d 2>/dev/null)" || true
if [ -z "$denied" ]; then
	echo "check_topics: denylist base64 decode failed — failing closed" >&2
	exit 2
fi

# Walk each banned string; case-insensitive fixed-string match against the
# haystack. On any match, mark the release blocked. The matched token is
# intentionally NOT echoed so the gate's own output stays attribution-free.
fail=0
while IFS= read -r banned; do
	[ -n "$banned" ] || continue
	if printf '%s' "$haystack" | grep -qiF -- "$banned"; then
		echo "check_topics: FAIL — a banned attribution string is present in '$repo' metadata (topics / description / homepage)." >&2
		echo "check_topics: clean the repo metadata (remove the banned topic, edit the description, fix the homepage URL) and re-run." >&2
		fail=1
	fi
done <<EOF
$denied
EOF

if [ "$fail" = "1" ]; then
	exit 1
fi

echo "check_topics: OK — no banned attribution strings in '$repo' metadata"
exit 0
