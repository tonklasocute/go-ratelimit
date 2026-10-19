#!/usr/bin/env bash
# Prints allowed (200) and rejected (429) requests per second and per
# instance from the k6 CSV written by docker-compose.yml.
set -euo pipefail
csv=${1:-"$(dirname "$0")/results/k6.csv"}
awk -F, '
  NR == 1 { for (i = 1; i <= NF; i++) col[$i] = i; next }
  $col["metric_name"] != "http_reqs" || $col["scenario"] != "load" { next }
  {
    t = $col["timestamp"]; if (start == "" || t < start) start = t
    host = ($col["url"] ~ /app1/) ? "app1" : "app2"
    seen[t] = 1
    if ($col["status"] == 200) ok[t, host]++; else if ($col["status"] == 429) no[t]++
  }
  END {
    for (t in seen) printf "%d %d %d %d\n", t - start, ok[t, "app1"], ok[t, "app2"], no[t]
  }' "$csv" | sort -n | awk '
  BEGIN { printf "%-6s %6s %6s %8s %9s\n", "second", "app1", "app2", "allowed", "rejected" }
  { printf "%-6d %6d %6d %8d %9d\n", $1, $2, $3, $2 + $3, $4; total += $2 + $3; rej += $4 }
  END { printf "total  %22d %9d\n", total, rej }'
