#!/usr/bin/env python3
"""benchmark.sh の結果ログを読み込み、論文 Table 1 / Table 2 形式の表を出力する"""

import os
import statistics
import sys

RESULTS_DIR = os.path.join(os.path.dirname(__file__), "results")


def load_log(filename):
    """ログファイルを読み込み、(fwd, bwd, replay) のリストと TO 回数を返す"""
    path = os.path.join(RESULTS_DIR, filename)
    if not os.path.exists(path):
        return [], 0
    fwd, bwd, rep = [], [], []
    to_count = 0
    with open(path) as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            if line == "TIMEOUT":
                to_count += 1
                continue
            parts = line.split()
            if len(parts) == 3:
                fwd.append(float(parts[0]))
                bwd.append(float(parts[1]))
                rep.append(float(parts[2]))
    return list(zip(fwd, bwd, rep)), to_count


def fmt_ms(values, to_count=0):
    """中央値を 'XXX.XXXms' 形式にフォーマット。TO があれば付記"""
    if not values:
        return "N/A"
    med = statistics.median(values)
    s = f"{med:.3f}ms"
    if to_count > 0:
        s += f" ({to_count} TO)"
    return s


def print_table1():
    procs_list = [1, 2, 10, 20]

    for prog, label in [("fib10", "fib(10)"), ("fib15", "fib(15)")]:
        print(f"{label} calculation:")
        print(f"{'':>20s} {'Forward':>16s} {'Backward':>16s} {'Replay':>20s}")
        for p in procs_list:
            data, to = load_log(f"{prog}_procs{p}.log")
            if not data:
                print(f"  {p:>2d} processor(s)     {'N/A':>16s} {'N/A':>16s} {'N/A':>20s}")
                continue
            fwd_vals = [d[0] for d in data]
            bwd_vals = [d[1] for d in data]
            rep_vals = [d[2] for d in data]
            print(
                f"  {p:>2d} processor(s)  "
                f"   {fmt_ms(fwd_vals):>16s}"
                f" {fmt_ms(bwd_vals):>16s}"
                f" {fmt_ms(rep_vals, to):>20s}"
            )
        print()


def print_table2():
    print(f"{'':>20s} {'Forward':>16s} {'Backward':>16s} {'Replay':>16s}")
    for prog, label in [("sfib10", "sfib(10)"), ("sfib15", "sfib(15)")]:
        data, to = load_log(f"{prog}_procs20.log")
        if not data:
            print(f"  {label:<16s}   {'N/A':>16s} {'N/A':>16s} {'N/A':>16s}")
            continue
        fwd_vals = [d[0] for d in data]
        bwd_vals = [d[1] for d in data]
        rep_vals = [d[2] for d in data]
        print(
            f"  {label:<16s}"
            f"   {fmt_ms(fwd_vals):>16s}"
            f" {fmt_ms(bwd_vals):>16s}"
            f" {fmt_ms(rep_vals, to):>16s}"
        )
    print()


def print_paper_table1():
    """論文の Table 1 の値（比較用）"""
    print("fib(10) calculation:")
    print(f"{'':>20s} {'Forward':>16s} {'Backward':>16s} {'Replay':>20s}")
    paper = [
        (1, "94.074ms", "88.149ms", "98.149ms"),
        (2, "50.955ms", "101.833ms", "104.176ms"),
        (10, "26.119ms", "28.825ms", "42.646ms"),
        (20, "26.655ms", "28.053ms", "37.457ms (1 TO)"),
    ]
    for p, f, b, r in paper:
        print(f"  {p:>2d} processor(s)     {f:>16s} {b:>16s} {r:>20s}")
    print()
    print("fib(15) calculation:")
    print(f"{'':>20s} {'Forward':>16s} {'Backward':>16s} {'Replay':>20s}")
    paper = [
        (1, "926.531ms", "854.810ms", "920.486ms"),
        (2, "655.447ms", "586.187ms", "640.634ms"),
        (10, "288.658ms", "211.488ms", "215.120ms (3 TO)"),
        (20, "277.781ms", "140.793ms", "139.661ms (2 TO)"),
    ]
    for p, f, b, r in paper:
        print(f"  {p:>2d} processor(s)     {f:>16s} {b:>16s} {r:>20s}")
    print()


def print_paper_table2():
    print(f"{'':>20s} {'Forward':>16s} {'Backward':>16s} {'Replay':>16s}")
    print(f"  {'sfib(10)':<16s}      {'640.178ms':>16s} {'590.278ms':>16s} {'683.542ms':>16s}")
    print(f"  {'sfib(15)':<16s}       {'12.301s':>16s}  {'10.164s':>16s}  {'11.699s':>16s}")
    print()


if __name__ == "__main__":
    print("=" * 72)
    print(" Table 1: Execution time for fib(10) and fib(15)")
    print("=" * 72)
    print()
    print_table1()

    print("=" * 72)
    print(" Table 2: The single call Fibonacci program (20 processors)")
    print("=" * 72)
    print()
    print_table2()

    print("=" * 72)
    print(" 論文の Table 1（参考値）")
    print(" Xeon E5-2690 v2 (20cores, 3.00GHz), 128GB, Go 1.25.1")
    print("=" * 72)
    print()
    print_paper_table1()

    print("=" * 72)
    print(" 論文の Table 2（参考値）")
    print("=" * 72)
    print()
    print_paper_table2()
