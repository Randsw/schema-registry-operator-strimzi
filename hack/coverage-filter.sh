#!/bin/sh
# Strip non-project code from a Go coverage profile.
#
# Two entries are excluded from coverage; neither is hand-written project
# logic and neither may be tested directly:
#   * api/v1alpha1/zz_generated.deepcopy.go — controller-gen output
#     (`make generate`); its content is regenerated, never hand-edited.
#   * the main() function of cmd/main.go — process wiring (flags, manager
#     construction, signal handling) that only executes inside the operator
#     binary. The rest of cmd/main.go (watchNamespace, managerCacheOptions)
#     stays in the profile and is covered by cmd/main_test.go.
#
# Usage: hack/coverage-filter.sh [coverprofile]   (rewrites it in place)
#
# Written for POSIX sh and depends only on grep/awk (no new tools).

set -eu

profile=${1:-cover.out}
main_src=cmd/main.go

if [ ! -f "$profile" ]; then
	echo "coverage-filter: profile not found: $profile" >&2
	exit 1
fi
if [ ! -f "$main_src" ]; then
	echo "coverage-filter: source not found: $main_src" >&2
	exit 1
fi

# Locate main(): its opening line and the line of the brace that closes it.
# Braces are counted so nested blocks and function literals are handled.
main_range=$(awk '
	!inside && /^func main\(/ { inside = 1; first = NR; depth = 0 }
	inside {
		braces = $0
		gsub(/[^{}]/, "", braces)
		for (i = 1; i <= length(braces); i++) {
			if (substr(braces, i, 1) == "{") {
				depth++
			} else {
				depth--
			}
		}
		if (NR > first && depth == 0) { print first, NR; exit }
	}
' "$main_src")

if [ -z "$main_range" ]; then
	echo "coverage-filter: cannot find main() in $main_src" >&2
	exit 1
fi

main_first=${main_range% *}
main_last=${main_range#* }

tmp=${profile}.filtered.$$
trap 'rm -f "$tmp"' EXIT HUP INT TERM

awk -v main_first="$main_first" -v main_last="$main_last" '
	/^mode:/ { print; next }
	{
		colon = index($0, ":")
		if (colon == 0) { next }
		file = substr($0, 1, colon - 1)
		block = substr($0, colon + 1)
		sub(/\..*/, "", block)     # leading source line of the block

		# Generated deepcopy code: not project logic.
		if (file ~ /(^|\/)api\/v1alpha1\/zz_generated\.deepcopy\.go$/) { next }

		# Only main() of cmd/main.go is excluded, not the whole file.
		if (file ~ /(^|\/)cmd\/main\.go$/) {
			line = block + 0
			if (line >= main_first + 0 && line <= main_last + 0) { next }
		}
		print
	}
' "$profile" > "$tmp"

mv "$tmp" "$profile"
trap - EXIT HUP INT TERM

echo "coverage-filter: excluded api/v1alpha1/zz_generated.deepcopy.go and cmd/main.go main() (lines $main_first-$main_last)"
