"""Figures for docs/gonka-decode-poc-whitepaper.md.

Usage: python make_figures.py <path to a checkout of kaitakuai/experiments @ cdf6d28>
Figure 3 is computed from the committed counters; the other figures use the published
numbers quoted next to each array.
"""

import glob
import json
import os
import sys

import matplotlib

matplotlib.use("Agg")
import matplotlib.pyplot as plt
import numpy as np
from matplotlib.ticker import FixedLocator, FuncFormatter, NullLocator

KX = sys.argv[1]
OUT = os.path.dirname(os.path.abspath(__file__))
PREFILL, DECODE, SERVING = "#E69F00", "#0072B2", "#8C8C8C"
HONEST, FRAUD = "#0072B2", "#D55E00"
plt.rcParams.update({"font.size": 8, "axes.spines.top": False, "axes.spines.right": False})


def ratio_bars(ax, groups, title, poc_color):
    x = np.arange(len(groups))
    w = 0.36
    for i, (label, poc, serving, prior) in enumerate(groups):
        b = ax.bar(i - w / 2, poc, w, color=poc_color, hatch="///" if prior else None,
                   edgecolor="white" if prior else None, linewidth=0)
        ax.bar(i + w / 2, serving, w, color=SERVING)
        for xx, v in ((i - w / 2, poc), (i + w / 2, serving)):
            ax.text(xx, v * (1.08 if v >= 1 else 0.93), f"×{v:.2f}", ha="center",
                    va="bottom" if v >= 1 else "top", fontsize=6.5)
        del b
    ax.axhline(1, color="black", linewidth=0.8)
    ax.set_yscale("log", base=2)
    ax.set_ylim(0.35, 16)
    ax.yaxis.set_major_locator(FixedLocator([0.5, 1, 2, 4, 8]))
    ax.yaxis.set_minor_locator(NullLocator())
    ax.yaxis.set_major_formatter(FuncFormatter(lambda v, _: f"×{v:g}"))
    ax.set_xticks(x, [g[0] for g in groups], fontsize=6.5)
    ax.set_title(title, fontsize=8.5)


# Figure 1. Ratio after/before for one engine setting, PoC and serving measured on the same box.
prefill_groups = [
    # 2026-07/deepseek-v4-flash-2xb200: PoC b32 1344 -> 2304; compressa s2 165.54 -> 932.26 tok/s
    ("DeepSeek-V4-Flash\n2×B200, TP 2\nCUDA graphs on", 2304 / 1344, 932.26 / 165.54, False),
    # 2026-07/deepseek-v4-flash-2xh200: PoC 1216 -> 1216; s2 152 -> 371
    ("DeepSeek-V4-Flash\n2×H200, TP 2\nCUDA graphs on", 1216 / 1216, 371 / 152, False),
    # 2026-07/deepseek-v4-flash-4xh100: PoC 1536 -> 1536; s2 133.8 -> 1293.8
    ("DeepSeek-V4-Flash\n4×H100, TP 4\nCUDA graphs on", 1536 / 1536, 1293.8 / 133.8, False),
    # 2026-06/glm-5.2-deepgemm-8xb200: PoC 1517 -> 1054; bench conc 16: 196.17 -> 440.77
    ("GLM-5.2\n8×B200, TP 8\nCUDA graphs on", 1054 / 1517, 440.77 / 196.17, False),
    # 2026-06/decode-poc-goodhart-deepgemm-vs-flashinfer: prefill "+40 % (prior)";
    # serving compiled FlashInfer 3584.3 -> DeepGEMM 1818.9
    ("MiniMax-M2.7\n1×B300, TP 1\nDeepGEMM MoE", 1.40, 1818.9 / 3584.3, True),
]
decode_groups = [
    # goodhart repeat logs: decode PoC steps/s and serving tok/s, means of 3
    ("compiled vs eager\nFlashInfer MoE", 1087.1 / 807.6, 3584.3 / 1443.4, False),
    ("compiled vs eager\nDeepGEMM MoE", 992.4 / 748.9, 1818.9 / 1504.5, False),
    ("DeepGEMM vs\nFlashInfer MoE,\ncompiled", 992.4 / 1087.1, 1818.9 / 3584.3, False),
    ("DeepGEMM vs\nFlashInfer MoE,\neager", 748.9 / 807.6, 1504.5 / 1443.4, False),
]
fig, (a1, a2) = plt.subplots(1, 2, figsize=(10, 3.5), sharey=True,
                             gridspec_kw={"width_ratios": [5, 4]})
ratio_bars(a1, prefill_groups, "Prefill scheme: PoC vs serving", PREFILL)
ratio_bars(a2, decode_groups, "Decode scheme: PoC vs serving, MiniMax-M2.7 on 1×B300", DECODE)
a1.set_ylabel("after ÷ before")
handles = [plt.Rectangle((0, 0), 1, 1, color=c) for c in (PREFILL, DECODE, SERVING)]
handles.append(plt.Rectangle((0, 0), 1, 1, facecolor=PREFILL, hatch="///", edgecolor="white"))
fig.legend(handles, ["prefill PoC, nonces/min", "decode PoC, steps/s", "serving, output tok/s",
                     "prior measurement, raw data not published"],
           loc="lower center", ncol=4, frameon=False, fontsize=7)
fig.tight_layout(rect=(0, 0.07, 1, 1))
fig.savefig(os.path.join(OUT, "fig1_poc_vs_serving.png"), dpi=200)
plt.close(fig)

# Figure 2. 2026-06/decode-poc-separability-compiled-vs-eager, N = 64, mean ± 95 % CI.
fig, ax = plt.subplots(figsize=(4.6, 2.8))
modes = ["compiled prefill + decode", "eager prefill + decode"]
honest, honest_ci = [0.199, 0.441], [0.020, 0.021]
fraud, fraud_ci = [0.569, 0.465], [0.017, 0.020]
gaps = ["gap +0.369 ± 0.027", "gap +0.024 ± 0.029"]
x = np.arange(2)
ax.bar(x - 0.18, honest, 0.36, yerr=honest_ci, color=HONEST, capsize=3, label="honest FP8")
ax.bar(x + 0.18, fraud, 0.36, yerr=fraud_ci, color=FRAUD, capsize=3, label="fraud AWQ")
for i, g in enumerate(gaps):
    ax.text(i, 0.66, g, ha="center", fontsize=7.5)
ax.set_xticks(x, modes)
ax.set_ylim(0, 0.72)
ax.set_ylabel("share of decode steps\nwith a different index")
ax.legend(frameon=False, fontsize=7, loc="upper right", bbox_to_anchor=(1.0, 0.9))
fig.tight_layout()
fig.savefig(os.path.join(OUT, "fig2_compiled_vs_eager_separability.png"), dpi=200)
plt.close(fig)

# Figure 3. Share of nonces flagged against tau, computed from the committed counters.
tau_mm = json.load(open(os.path.join(
    KX, "2026-09/decode-poc-0251-freeze/artifacts/fig_data_tau.json")))
taus_mm = np.array(tau_mm["taus"])
vals = ("B300", "H200", "H100")
mm = {
    "fraud (AWQ QuantTrio)": np.mean([tau_mm[v]["fraud"] for v in vals], axis=0),
    "honest, other card": np.mean([tau_mm[v]["honest_cross"] for v in vals], axis=0),
    "honest, same card, other boot": np.array(tau_mm["B300"]["honest_other_boot"]),
}
cells = {}
glm_dir = os.path.join(KX, "2026-09/decode-poc-0281-glm-freeze/artifacts/validations/glm")
for fn in glob.glob(os.path.join(glm_dir, "validator_*", "prover_*", "npz_*_h*_v200.npz")):
    v = fn.split("validator_")[1].split(os.sep)[0]
    p = fn.split("prover_")[1].split(os.sep)[0]
    role = os.path.basename(fn).split("_")[1]
    z = np.load(fn)
    taus_glm = z["taus"]
    cells.setdefault((role, p, v), []).append(100 * (z["counts"] >= 1).mean(axis=1))
cells = {k: np.mean(v, axis=0) for k, v in cells.items()}


def pool(sel):
    return np.mean([c for k, c in cells.items() if sel(*k)], axis=0)


glm = {
    "fraud (W4A16), 15 cells": pool(lambda r, p, v: r == "fraud"),
    "honest, other card, 11 cells": pool(lambda r, p, v: r == "honest" and p != v),
    "honest, same card, other boot, TP 4": pool(
        lambda r, p, v: r == "honest" and p == v and p in ("h200", "b200")),
    "honest, same card, other boot, TP 2 and 8": pool(
        lambda r, p, v: r == "honest" and p == v and p in ("b300", "h100")),
}
styles = [(FRAUD, "-"), (HONEST, "-"), (HONEST, "--"), (HONEST, ":")]
fig, (a1, a2) = plt.subplots(1, 2, figsize=(10, 3.1), sharey=True)
for ax, taus, series, title, band in (
        (a1, taus_mm, mm, "MiniMax-M2.7, validators B300 · H200 · H100", (0.04, 0.05)),
        (a2, taus_glm, glm, "GLM-5.3-Flash, validators B300 · H200 · H100 · B200", (0.025, 0.03))):
    for (name, ys), (c, ls) in zip(series.items(), styles):
        m = taus <= 0.1
        ax.plot(taus[m], np.clip(ys[m], 0.1, None), color=c, linestyle=ls, label=name)
    ax.axvspan(*band, color="#DDDDDD", zorder=0)
    ax.set_yscale("log")
    ax.set_ylim(0.1, 110)
    ax.set_xlim(0, 0.1)
    ax.set_xlabel("τ")
    ax.set_title(title, fontsize=8.5)
    ax.legend(frameon=False, fontsize=6.5, loc="lower left")
a1.set_ylabel("nonces flagged, %")
a1.yaxis.set_major_formatter(FuncFormatter(lambda v, _: f"{v:g}"))
fig.tight_layout()
fig.savefig(os.path.join(OUT, "fig3_nonces_flagged_vs_tau.png"), dpi=200)
plt.close(fig)

# Figure 4. R = PoC nonces/s ÷ chat requests/s per configuration (freeze folders).
# R values and spreads as published in the freeze READMEs.
fleet = [
    ("MiniMax-M2.7, vLLM 0.25.1", "×1.107", "#0072B2", [
        ("4×H100", "0.884"), ("2×B200", "0.909"), ("2×H200", "0.929"), ("1×B300", "0.946"),
        ("4×A100", "0.979")]),
    ("DeepSeek-V4-Flash, vLLM 0.25.1", "×1.17", "#009E73", [
        ("4×H100 FP8", "0.99"), ("2×H200 FP8", "1.10"), ("2×B200 NVFP4", "1.10"),
        ("2×B200 FP8", "1.12"), ("1×B300 NVFP4", "1.14"), ("1×B300 FP8", "1.16")]),
    ("GLM-5.3-Flash, vLLM 0.28.1", "×1.160", "#CC79A7", [
        ("8×H100", "0.846"), ("4×H200", "0.938"), ("2×B300", "0.980"), ("4×B200", "0.982")]),
]
fig, ax = plt.subplots(figsize=(10, 2.6))
x0 = 0
ticks, labels = [], []
for name, spread, color, rows in fleet:
    xs = np.arange(len(rows)) + x0
    rs = [float(r) for _, r in rows]
    ax.bar(xs, rs, 0.7, color=color)
    for xx, (_, r) in zip(xs, rows):
        ax.text(xx, float(r) + 0.01, r, ha="center", fontsize=6.5)
    ax.text(xs.mean(), 1.24, f"{name}\nmax ÷ min {spread}", ha="center", fontsize=7.5)
    ticks += list(xs)
    labels += [c for c, _ in rows]
    x0 += len(rows) + 1
ax.set_xticks(ticks, labels, fontsize=6.5, rotation=30, ha="right")
ax.set_ylim(0.6, 1.36)
ax.set_ylabel("R = PoC nonces/s ÷\nchat requests/s")
fig.tight_layout()
fig.savefig(os.path.join(OUT, "fig4_poc_to_chat_ratio.png"), dpi=200)
plt.close(fig)
print("written to", OUT)
