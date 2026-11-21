#!/bin/sh
# Enforce a minimum statement-coverage threshold on a filtered Go coverage
# profile.
#
# The profile must already have non-project code removed by
# hack/coverage-filter.sh (zz_generated.deepcopy.go and the main() function of
# cmd/main.go), so the percentage reflects hand-written project logic only.
#
# The percentage is computed directly from the profile's statement counts, not
# scraped from `go tool cover -func` text, and the pass/fail comparison uses
# exact integer arithmetic in basis points (hundredths of a percent). This
# keeps the gate deterministic and immune to locale-specific decimal
# separators or output-format changes.
#
# Usage: hack/coverage-gate.sh [coverprofile] [threshold-percent]
#   e.g. hack/coverage-gate.sh cover.out 84.0
#
# Exit status: 0 when coverage meets or exceeds the threshold; 1 when it is
# below the threshold or the input is invalid.

set -eu

profile=${1:-cover.out}
threshold=${2:-84.0}

if [ ! -f "$profile" ]; then
	echo "coverage-gate: profile not found: $profile" >&2
	exit 1
fi

# Parse the threshold ("84", "84.0", "84.00") into basis points using integer
# arithmetic only. Reject anything that is not a plain non-negative decimal.
threshold_bp=$(awk -v t="$threshold" 'BEGIN {
	if (t !~ /^[0-9]+([.][0-9]+)?$/) {
		exit 1
	}
	n = split(t, p, ".")
	whole = p[1] + 0
	frac = 0
	if (n > 1) {
		f = p[2] "00"
		frac = substr(f, 1, 2) + 0
	}
	printf "%d\n", whole * 100 + frac
}') || {
	echo "coverage-gate: invalid threshold: $threshold" >&2
	exit 1
}

# Sum statement counts from the profile. Field 2 is the statement count and
# field 3 the execution count; a zero execution count means the block was
# never covered. Emit:
#   <total-statements> <covered-statements> <coverage-basis-points>
stats=$(awk '
	NR == 1 {
		if ($1 != "mode:" || ($2 != "set" && $2 != "count" && $2 != "atomic")) {
			print "coverage-gate: invalid profile header: " $0 > "/dev/stderr"
			exit 1
		}
		next
	}
	{
		if (NF < 3) {
			next
		}
		stmts = $2 + 0
		total += stmts
		if ($3 + 0 > 0) {
			covered += stmts
		}
	}
	END {
		if (total == 0) {
			print "coverage-gate: profile contains no statements" > "/dev/stderr"
			exit 1
		}
		# Rounded basis points, for display only.
		printf "%d %d %d\n", total, covered, int((covered * 10000 + total / 2) / total)
	}
' "$profile")

total=${stats%% *}
rest=${stats#* }
covered=${rest%% *}
actual_bp=${rest#* }

fmt_pct() {
	# Render basis points ($1) as a percentage without using floating point.
	printf '%d.%02d' $(( $1 / 100 )) $(( $1 % 100 ))
}

# Exact rational comparison: covered/total >= threshold_bp/10000.
if [ $((covered * 10000)) -ge $((threshold_bp * total)) ]; then
	echo "coverage-gate: filtered project coverage $(fmt_pct "$actual_bp")% ($covered/$total statements) meets the $(fmt_pct "$threshold_bp")% threshold"
	exit 0
fi

echo "coverage-gate: filtered project coverage $(fmt_pct "$actual_bp")% ($covered/$total statements) is BELOW the $(fmt_pct "$threshold_bp")% threshold" >&2
echo "coverage-gate: add tests or lower COVERAGE_THRESHOLD deliberately (see Phase 5 of plans/test-suite-audit-and-optimization-plan.md)" >&2
exit 1
