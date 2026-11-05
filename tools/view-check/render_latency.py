"""Renderer latency: how long a renderer's stream (`serve`, render/README.md) takes from a camera
to that frame on its stdout, at a size. It sends a camera every 50 ms for 3 s, as a drag does,
and times the next frame after each. See README.md.

    python3 tools/view-check/render_latency.py RENDERER MODEL WIDTH HEIGHT
"""
import json, struct, subprocess, sys, threading, time, statistics
binary, model, w, h = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4]
p = subprocess.Popen([binary, "serve", model, w, h], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
frames, ready = [], {}
def read():
    while True:
        head = p.stdout.read(4)
        if len(head) < 4: return
        body = p.stdout.read(struct.unpack(">I", head)[0])
        if body[:1] == b"V": frames.append((time.monotonic(), len(body)))
        elif body[:1] == b"R": ready.update(json.loads(body[1:]))
threading.Thread(target=read, daemon=True).start()
time.sleep(2)
home = ready["home"]
waits, sizes = [], []
for i in range(60):
    sent = time.monotonic(); n = len(frames)
    p.stdin.write((json.dumps({"camera": dict(home, yaw=home["yaw"] + 0.02 * i)}) + "\n").encode()); p.stdin.flush()
    while len(frames) == n and time.monotonic() - sent < 1: time.sleep(0.0005)
    if len(frames) > n: waits.append((frames[n][0] - sent) * 1000); sizes.append(frames[n][1])
    time.sleep(max(0, 0.05 - (time.monotonic() - sent)))
p.stdin.close(); p.wait()
waits.sort()
print(f"{w}x{h}: command to frame median {statistics.median(waits):.1f} ms, p90 {waits[int(len(waits)*0.9)]:.1f} ms, max {waits[-1]:.1f} ms; frame {statistics.median(sizes)/1000:.0f} KB median")
