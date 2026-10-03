"""临时诊断脚本：分析媒体探针落下的 WAV（16bit 单声道 8kHz）。

输出：
  1) 时间轴（10ms 粒度）：静音段 / 有声段，附峰值与长度——回铃音是"1s 有声 + 4s 静音"
     这类节奏，一眼能看出哪个音被切断、哪里有补的静音；
  2) 有声段内部的波形跳变（切音/接缝）位置——用 |Δ| 超过阈值的点数表示。

用法：python tools/wavscan.py <wav> [wav2 ...]
"""
import struct
import sys


def read_wav(path):
    with open(path, "rb") as f:
        data = f.read()
    assert data[:4] == b"RIFF" and data[8:12] == b"WAVE", "不是 WAV"
    pos = 12
    fmt = None
    pcm = None
    while pos + 8 <= len(data):
        cid = data[pos:pos + 4]
        size = struct.unpack_from("<I", data, pos + 4)[0]
        body = data[pos + 8:pos + 8 + size]
        if cid == b"fmt ":
            fmt = struct.unpack_from("<HHIIHH", body, 0)
        elif cid == b"data":
            pcm = body
        pos += 8 + size + (size & 1)
    ch, rate, bits = fmt[1], fmt[2], fmt[5]
    assert bits == 16, "只支持 16bit"
    n = len(pcm) // 2
    samples = struct.unpack_from("<%dh" % n, pcm, 0)
    if ch > 1:
        samples = samples[::ch]
        rate = rate
    return list(samples), rate


def seg_timeline(samples, rate, win_ms=10):
    win = rate * win_ms // 1000
    segs = []
    for i in range(0, len(samples) - win + 1, win):
        blk = samples[i:i + win]
        peak = max(abs(v) for v in blk)
        rms = (sum(v * v for v in blk) / len(blk)) ** 0.5
        segs.append((i, peak, rms))
    # 有声判定：rms 超过 200（回铃音幅度通常几千）
    out = []
    cur = None
    for i, peak, rms in segs:
        loud = rms > 200
        if cur is None or cur[0] != loud:
            if cur:
                out.append(cur)
            cur = [loud, i, i + win, peak]
        else:
            cur[2] = i + win
            cur[3] = max(cur[3], peak)
    if cur:
        out.append(cur)
    return out, win


def clicks(samples, rate, thresh_ratio=0.45, win_ms=10):
    """有声段内的相邻样本大跳变（切音造成的咔哒）。"""
    win = rate * win_ms // 1000
    res = []
    for i in range(0, len(samples) - win, win):
        blk = samples[i:i + win]
        peak = max(abs(v) for v in blk)
        if peak < 800:
            continue
        th = peak * thresh_ratio
        cnt = 0
        for j in range(i + 1, i + win):
            if abs(samples[j] - samples[j - 1]) > th:
                cnt += 1
        if cnt:
            res.append((i / rate * 1000, cnt, peak))
    return res


def main():
    for path in sys.argv[1:]:
        samples, rate = read_wav(path)
        dur = len(samples) / rate
        print("=" * 78)
        print("%s  时长 %.2fs  采样率 %d  峰值 %d" %
              (path, dur, rate, max(abs(v) for v in samples) if samples else 0))
        segs, win = seg_timeline(samples, rate)
        print("-- 时间轴（10ms 粒度，[有声]/[静音] 段） --")
        for loud, a, b, peak in segs:
            tag = "有声" if loud else "静音"
            print("  %s %7.0fms → %7.0fms  长 %6.0fms  峰值 %5d" %
                  (tag, a / rate * 1000, b / rate * 1000, (b - a) / rate * 1000, peak))
        print("-- 有声段内的波形跳变（可能听成咔哒/断续） --")
        cl = clicks(samples, rate)
        if not cl:
            print("  （无）")
        for ms, cnt, peak in cl[:40]:
            print("  %.0fms  跳变点 %d  该窗口峰值 %d" % (ms, cnt, peak))
        if len(cl) > 40:
            print("  ... 共 %d 处" % len(cl))


if __name__ == "__main__":
    main()
