#!/bin/bash
# benchmark.sh — 論文 Table 1〜4, Fig. 25, Fig. 29 の再現実験用スクリプト
#
# 使い方:
#   chmod +x benchmark.sh
#   ./benchmark.sh              # 全テーブル (Table 1〜4) を実行
#   ./benchmark.sh table1       # Table 1 のみ
#   ./benchmark.sh table2       # Table 2 のみ
#   ./benchmark.sh table3       # Table 3 のみ
#   ./benchmark.sh table4       # Table 4 のみ
#   ./benchmark.sh trace         # Fig. 25, Fig. 29 用トレース生成
#
# 出力:
#   results/ ディレクトリに生ログ・トレースファイルを保存
#   表は標準出力に表示

set -euo pipefail
cd "$(dirname "$0")"

TRIALS=10
TIMEOUT=180  # 3分（論文と同じ）
TIMEOUT_SIEVE1000=600  # sieve1000 は10分
RESULTS_DIR="results"
RUNTIME="./cjanus-runtime"
mkdir -p "$RESULTS_DIR"

# ビルド済みバイナリがなければビルド
if [ ! -f "$RUNTIME" ]; then
    echo "ランタイムをビルド中..."
    go build -o "$RUNTIME" ./runtime
fi

# ============================================================
# 1回の計測を実行し、Forward/Backward/Replay の時間(ms)を出力
#    引数: $1=プログラム(.crl), $2=プロセッサ数, $3=タイムアウト秒(省略時$TIMEOUT)
#    出力: "forward_ms backward_ms replay_ms" or "TIMEOUT" on timeout
# ============================================================
run_once() {
    local prog="$1"
    local procs="$2"
    local tmout="${3:-$TIMEOUT}"

    local output
    output=$(timeout "$tmout" bash -c \
        "cat /dev/zero | $RUNTIME -silent -v=false -auto -t -procs $procs '$prog' 2>&1" \
    ) || {
        echo "TIMEOUT"
        return 0
    }

    # Elapsed 行を抽出（3行あるはず: Forward, Backward, Replay）
    local times
    times=$(echo "$output" | grep -oP 'Elapsed: \K[0-9.]+(?:ms|s|µs|ns)' | head -3)
    local count
    count=$(echo "$times" | wc -l)

    if [ "$count" -lt 3 ]; then
        echo "TIMEOUT"
        return 0
    fi

    # 全てを ms に変換して出力
    echo "$times" | python3 -c "
import sys

def to_ms(s):
    s = s.strip()
    if s.endswith('ms'):
        return float(s[:-2])
    elif s.endswith('µs'):
        return float(s[:-2]) / 1000.0
    elif s.endswith('ns'):
        return float(s[:-2]) / 1000000.0
    elif s.endswith('s'):
        return float(s[:-1]) * 1000.0
    else:
        return float(s)

vals = [to_ms(line) for line in sys.stdin]
print(f'{vals[0]:.3f} {vals[1]:.3f} {vals[2]:.3f}')
"
}

# ============================================================
# ベンチマーク実行: 指定プログラム・プロセッサ数で TRIALS 回計測
#    引数: $1=名前, $2=.crlパス, $3=プロセッサ数, $4=タイムアウト秒(省略可)
# ============================================================
run_benchmark() {
    local name="$1"
    local prog="$2"
    local procs="$3"
    local tmout="${4:-$TIMEOUT}"
    local logfile="$RESULTS_DIR/${name}_procs${procs}.log"

    echo -n "  計測中: $name (procs=$procs) "

    > "$logfile"
    for i in $(seq 1 $TRIALS); do
        local result
        result=$(run_once "$prog" "$procs" "$tmout")
        echo "$result" >> "$logfile"
        echo -n "."
    done
    echo " done"
}

# ============================================================
# 集計: ログファイルから中央値とTO回数を算出
#    引数: $1=ログファイル
#    出力: "fwd_median\tbwd_median\trep_median\tto_count"
# ============================================================
summarize() {
    local logfile="$1"
    python3 -c "
import statistics

fwd_times, bwd_times, rep_times = [], [], []
to_count = 0

with open('$logfile') as f:
    for line in f:
        line = line.strip()
        if line == 'TIMEOUT':
            to_count += 1
            continue
        parts = line.split()
        if len(parts) == 3:
            fwd_times.append(float(parts[0]))
            bwd_times.append(float(parts[1]))
            rep_times.append(float(parts[2]))

def fmt(lst):
    if not lst:
        return 'N/A'
    v = statistics.median(lst)
    if v >= 1000:
        return f'{v/1000:.3f}s'
    return f'{v:.3f}ms'

print(f'{fmt(fwd_times)}\t{fmt(bwd_times)}\t{fmt(rep_times)}\t{to_count}')
"
}

# ============================================================
# 表の出力ヘルパー
# ============================================================
print_table_header() {
    printf "%-18s  %-14s  %-14s  %-14s\n" "" "Forward" "Backward" "Replay"
}

print_table_row() {
    local label="$1"
    local logfile="$2"
    local result
    result=$(summarize "$logfile")
    local fwd bwd rep to to_str
    fwd=$(echo "$result" | cut -f1)
    bwd=$(echo "$result" | cut -f2)
    rep=$(echo "$result" | cut -f3)
    to=$(echo "$result" | cut -f4)
    to_str=""
    if [ "$to" -gt 0 ]; then
        to_str=" ($to TO)"
    fi
    printf "%-18s  %-14s  %-14s  %-14s\n" \
        "$label" "$fwd" "$bwd" "${rep}${to_str}"
}

# ============================================================
# Table 1: fib(10) / fib(15) × プロセッサ数
# ============================================================
run_table1() {
    echo ""
    echo "========================================"
    echo " Table 1: fib(10) / fib(15) 性能計測"
    echo "========================================"

    for prog_name in fib10 fib15; do
        if [ "$prog_name" = "fib10" ]; then
            crl="progs/fib.crl"
        else
            crl="progs/fib15.crl"
        fi
        for procs in 1 2 10 20; do
            run_benchmark "$prog_name" "$crl" "$procs"
        done
    done

    echo ""
    echo "Table 1: Execution time for fib(10) and fib(15)"
    echo ""

    for prog_name in fib10 fib15; do
        echo "$prog_name calculation:"
        print_table_header
        for procs in 1 2 10 20; do
            print_table_row "$procs processor(s)" \
                "$RESULTS_DIR/${prog_name}_procs${procs}.log"
        done
        echo ""
    done
}

# ============================================================
# Table 2: sfib (逐次call版, 20 processors)
# ============================================================
run_table2() {
    echo ""
    echo "========================================"
    echo " Table 2: sfib(10) / sfib(15) 性能計測 (20 processors)"
    echo "========================================"

    for prog_name in sfib10 sfib15; do
        if [ "$prog_name" = "sfib10" ]; then
            crl="progs/sfib.crl"
        else
            crl="progs/sfib15.crl"
        fi
        run_benchmark "$prog_name" "$crl" 20
    done

    echo ""
    echo "Table 2: The single call Fibonacci program (20 processors)"
    echo ""
    print_table_header
    for prog_name in sfib10 sfib15; do
        print_table_row "$prog_name" \
            "$RESULTS_DIR/${prog_name}_procs20.log"
    done
    echo ""
}

# ============================================================
# Table 3: Sieve100 / Sieve1000 × プロセッサ数
# ============================================================
run_table3() {
    echo ""
    echo "========================================"
    echo " Table 3: Sieve100 / Sieve1000 性能計測"
    echo "========================================"

    for prog_name in sieve100 sieve1000; do
        if [ "$prog_name" = "sieve100" ]; then
            crl="progs/sieve.crl"
            tmout="$TIMEOUT"
        else
            crl="progs/sieve1000.crl"
            tmout="$TIMEOUT_SIEVE1000"
        fi
        for procs in 1 2 10 20; do
            run_benchmark "$prog_name" "$crl" "$procs" "$tmout"
        done
    done

    echo ""
    echo "Table 3: Execution time for the prime number enumeration"
    echo ""

    for prog_name in sieve100 sieve1000; do
        if [ "$prog_name" = "sieve100" ]; then
            echo "Enumerating primes up to 100 (Sieve100):"
        else
            echo "Enumerating primes up to 1000 (Sieve1000):"
        fi
        print_table_header
        for procs in 1 2 10 20; do
            print_table_row "$procs processor(s)" \
                "$RESULTS_DIR/${prog_name}_procs${procs}.log"
        done
        echo ""
    done
}

# ============================================================
# Table 4: Sieve100 plock除去版 × プロセッサ数
# ============================================================
run_table4() {
    echo ""
    echo "========================================"
    echo " Table 4: Sieve100 (plock除去版) 性能計測"
    echo "========================================"

    for procs in 1 2 10 20; do
        run_benchmark "sieve_noplock" "progs/sieve_noplock.crl" "$procs"
    done

    echo ""
    echo "Table 4: Execution time without the semaphore of plock"
    echo ""

    echo "Enumerating primes up to 100 (Sieve100, no plock):"
    print_table_header
    for procs in 1 2 10 20; do
        print_table_row "$procs processor(s)" \
            "$RESULTS_DIR/sieve_noplock_procs${procs}.log"
    done
    echo ""
}

# ============================================================
# Fig. 25 / Fig. 29: 実行トレースの生成
# ============================================================
run_traces() {
    echo ""
    echo "========================================"
    echo " Fig. 25 / Fig. 29: 実行トレース生成"
    echo "========================================"
    echo ""

    TRACE_DIR="$RESULTS_DIR/traces"
    mkdir -p "$TRACE_DIR"

    # Fig. 25: fib(10) with 1 proc and 10 procs
    for procs in 1 10; do
        echo "  トレース生成: fib(10) (procs=$procs) ..."
        local tracefile="$TRACE_DIR/trace_fib10_procs${procs}.out"
        cat /dev/zero | timeout 30 \
            $RUNTIME -silent -v=false -trace -procs "$procs" progs/fib.crl \
            >/dev/null 2>&1 || true
        if [ -s trace.out ]; then
            mv trace.out "$tracefile"
            echo "    → $tracefile ($(du -h "$tracefile" | cut -f1))"
        else
            echo "    → トレース生成失敗"
        fi
    done

    # Fig. 29: sieve100 with 1 proc and 10 procs
    for procs in 1 10; do
        echo "  トレース生成: sieve100 (procs=$procs) ..."
        local tracefile="$TRACE_DIR/trace_sieve100_procs${procs}.out"
        cat /dev/zero | timeout 60 \
            $RUNTIME -silent -v=false -trace -procs "$procs" progs/sieve.crl \
            >/dev/null 2>&1 || true
        if [ -s trace.out ]; then
            mv trace.out "$tracefile"
            echo "    → $tracefile ($(du -h "$tracefile" | cut -f1))"
        else
            echo "    → トレース生成失敗"
        fi
    done

    echo ""
    echo "トレースの可視化方法:"
    echo ""
    echo "  # ブラウザで実行トレースを表示 (Fig. 25 相当)"
    echo "  go tool trace $TRACE_DIR/trace_fib10_procs1.out"
    echo "  go tool trace $TRACE_DIR/trace_fib10_procs10.out"
    echo ""
    echo "  # ブラウザで実行トレースを表示 (Fig. 29 相当)"
    echo "  go tool trace $TRACE_DIR/trace_sieve100_procs1.out"
    echo "  go tool trace $TRACE_DIR/trace_sieve100_procs10.out"
    echo ""
    echo "  # スケジューラプロファイルをSVGに変換"
    echo "  go tool trace -pprof=sched TRACE_FILE > sched.prof"
    echo "  go tool pprof -svg $RUNTIME sched.prof > sched.svg"
    echo ""
}

# ============================================================
# メイン
# ============================================================

echo "========================================"
echo " Concurrent Janus 再現実験ベンチマーク"
echo "========================================"
echo ""
echo "実行環境:"
echo "  OS:     $(uname -srm)"
echo "  CPU:    $(lscpu | grep 'Model name' | sed 's/Model name:\s*//')"
echo "  コア数: $(nproc)"
echo "  メモリ: $(free -h | awk '/Mem:/{print $2}')"
echo "  Go:     $(go version | awk '{print $3}')"
echo "  日時:   $(date '+%Y-%m-%d %H:%M:%S')"
echo ""
echo "設定: ${TRIALS}回計測, タイムアウト${TIMEOUT}秒 (sieve1000: ${TIMEOUT_SIEVE1000}秒)"

case "${1:-all}" in
    table1)
        run_table1
        ;;
    table2)
        run_table2
        ;;
    table3)
        run_table3
        ;;
    table4)
        run_table4
        ;;
    trace)
        run_traces
        ;;
    all)
        run_table1
        run_table2
        run_table3
        run_table4
        ;;
    full)
        run_table1
        run_table2
        run_table3
        run_table4
        run_traces
        ;;
    *)
        echo ""
        echo "Usage: $0 [table1|table2|table3|table4|trace|all|full]"
        echo ""
        echo "  table1  Table 1: fib(10)/fib(15) × procs 1,2,10,20"
        echo "  table2  Table 2: sfib(10)/sfib(15) × 20 procs"
        echo "  table3  Table 3: sieve100/sieve1000 × procs 1,2,10,20"
        echo "  table4  Table 4: sieve100 (no plock) × procs 1,2,10,20"
        echo "  trace   Fig.25/29: 実行トレース生成"
        echo "  all     Table 1〜4 を全て実行"
        echo "  full    Table 1〜4 + トレースを全て実行"
        exit 1
        ;;
esac

echo ""
echo "========================================"
echo " 計測完了"
echo " 生ログ: $RESULTS_DIR/ ディレクトリ"
echo "========================================"
