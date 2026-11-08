"""Cut a streamed Qwen3 take off N times at random points, as interruptions do, and count crashes and hangs."""
import json, sys, time, random, http.client, pathlib
sys.path.insert(0, "harness")
import trial
texts = json.load(open("harness/long_texts.json"))
SHORT = "Okay, I'll stop there. What did you want to ask?"
model, n = sys.argv[1], int(sys.argv[2])
log = pathlib.Path(f"runs/context/{model}/cutloop.log").resolve()
def start():
    sp = trial.AudioCpp(dict(trial.MODELS[model]), log); sp.ready(); return sp
def stream(sp, text, cut=None):
    c = http.client.HTTPConnection("127.0.0.1", sp.port, timeout=10)
    t0 = time.time()
    c.request("POST", "/v1/audio/speech", json.dumps({**sp.body, "input": text}), {"Content-Type": "application/json", "Accept": "text/event-stream"})
    r = c.getresponse(); first = None
    for line in r:
        if line.startswith(b"data:") and b"delta" in line:
            first = first or time.time() - t0
        if cut and time.time() - t0 > cut:
            break
    c.close(); return first
random.seed(1)
sp = start(); crashes = hangs = 0; firsts = []
for i in range(n):
    cut = random.uniform(0.3, 6.0)
    try:
        stream(sp, random.choice(texts), cut)
        f = stream(sp, SHORT)
        firsts.append(f)
    except Exception as e:
        dead = sp.proc.poll() is not None
        crashes += dead; hangs += not dead
        print(f"  cut {i} at {cut:.1f} s: {'crashed' if dead else 'hung'} ({e!r})", flush=True)
        try: sp.proc.kill(); sp.proc.wait(10)
        except Exception: pass
        t = time.time(); sp = start(); print(f"  restarted in {time.time()-t:.2f} s", flush=True)
firsts.sort()
print(model, f"{n} cuts: {crashes} crashed, {hangs} hung; next reply's first audio median {firsts[len(firsts)//2]:.3f} s, worst {firsts[-1]:.3f} s", flush=True)
sp.close()
