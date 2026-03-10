#!/usr/bin/env bash
set -euo pipefail

BENCH_DIR="$(cd "$(dirname "$0")/.." && pwd)/benchmarks"
BASELINE="$BENCH_DIR/baseline.txt"
BENCH_PACKAGES="./internal/sql/"
COUNT=6
BENCHTIME="1s"

mkdir -p "$BENCH_DIR"

usage() {
    cat <<EOF
Usage: $0 <command> [options]

Commands:
  run         Run benchmarks, save timestamped results to benchmarks/
  baseline    Run benchmarks and save as the baseline for future comparisons
  compare     Compare the latest run against the saved baseline (requires benchstat)
  quick       Run benchmarks once (count=1) for a fast sanity check

Options:
  -count N      Number of iterations per benchmark (default: $COUNT)
  -benchtime D  Duration per benchmark (default: $BENCHTIME)
  -pkg PKG      Package to benchmark (default: $BENCH_PACKAGES)

Examples:
  $0 run
  $0 run -count 10
  $0 baseline
  $0 compare
  $0 quick
EOF
    exit 1
}

[[ $# -lt 1 ]] && usage
CMD="$1"; shift

while [[ $# -gt 0 ]]; do
    case "$1" in
        -count)    COUNT="$2"; shift 2 ;;
        -benchtime) BENCHTIME="$2"; shift 2 ;;
        -pkg)      BENCH_PACKAGES="$2"; shift 2 ;;
        *)         echo "Unknown option: $1"; usage ;;
    esac
done

run_benchmarks() {
    local outfile="$1"
    echo "Running benchmarks (count=$COUNT, benchtime=$BENCHTIME)..."
    go test "$BENCH_PACKAGES" \
        -bench=. \
        -benchmem \
        -benchtime="$BENCHTIME" \
        -count="$COUNT" \
        -run='^$' \
        -timeout=10m \
        | tee "$outfile"
    echo ""
    echo "Results saved to: $outfile"
}

case "$CMD" in
    run)
        OUTFILE="$BENCH_DIR/bench_$(date +%Y%m%d_%H%M%S).txt"
        run_benchmarks "$OUTFILE"
        # Symlink latest for easy comparison
        ln -sf "$(basename "$OUTFILE")" "$BENCH_DIR/latest.txt"
        ;;

    baseline)
        run_benchmarks "$BASELINE"
        echo "Baseline saved. Future 'compare' will use this."
        ;;

    compare)
        LATEST="$BENCH_DIR/latest.txt"
        if [[ ! -f "$BASELINE" ]]; then
            echo "No baseline found. Run '$0 baseline' first."
            exit 1
        fi
        if [[ ! -f "$LATEST" ]]; then
            echo "No latest run found. Run '$0 run' first."
            exit 1
        fi
        if ! command -v benchstat &>/dev/null; then
            echo "benchstat not found. Install with:"
            echo "  go install golang.org/x/perf/cmd/benchstat@latest"
            exit 1
        fi
        echo "Comparing baseline vs latest..."
        echo ""
        benchstat "$BASELINE" "$LATEST"
        ;;

    quick)
        OUTFILE="$BENCH_DIR/bench_quick_$(date +%Y%m%d_%H%M%S).txt"
        COUNT=1
        run_benchmarks "$OUTFILE"
        ;;

    *)
        usage
        ;;
esac
