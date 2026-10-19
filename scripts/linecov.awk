# Converts a Go cover profile to LINE coverage, the same unit Coveralls reports.
# A line is relevant if any block spans it; covered if any spanning block ran.
# Output (tab-separated):  TOTAL<TAB>covered<TAB>total<TAB>pct
#                          DIR<TAB>path<TAB>covered<TAB>total<TAB>pct
/^mode:/ { next }
NF == 3 {
  split($1, a, ":"); file = a[1]; split(a[2], r, ",")
  split(r[1], s, "."); split(r[2], e, ".")
  for (l = s[1]; l <= e[1]; l++) {
    k = file SUBSEP l; seen[k] = 1
    if ($3 + 0 > 0) hit[k] = 1
  }
}
END {
  for (k in seen) {
    split(k, p, SUBSEP); d = p[1]; sub(/\/[^\/]*$/, "", d); sub(/^github.com\/[^\/]+\/[^\/]+\//, "", d)
    t[d]++; T++
    if (k in hit) { c[d]++; C++ }
  }
  printf "TOTAL\t%d\t%d\t%.2f\n", C, T, (T ? 100 * C / T : 0)
  for (d in t) printf "DIR\t%s\t%d\t%d\t%.2f\n", d, c[d], t[d], 100 * c[d] / t[d]
}
