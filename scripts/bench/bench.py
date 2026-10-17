#!/usr/bin/env python3
"""Score onfly_focus against a simulated keyword search on one project.

    cd repos/hugo && onfly init && python3 ../../scripts/bench/bench.py ../../scripts/bench/cases/hugo.yaml

For each question in the cases file it calls onfly_focus (limit 5) through a
fresh `onfly serve` and checks whether the expected file is in the top five. The
baseline ranks the project's files by how many of the question's words they
contain and reads the top three whole. Token counts are bytes divided by four.
Cases file: `cases:` / `- query: ...` / `expect: [{file: a.go|b.go, symbol: Name}]`.

--min N exits 1 when onfly_focus finds fewer than N questions, so a change that
loses ground fails.
"""
import argparse, json, os, re, subprocess, sys, time

STOP = set("where which what when does with that this from have into only their there about each other them then than also before after while those these used uses using version number given".split())
EXT = (".go", ".md", ".php", ".py", ".js", ".ts", ".rs", ".rb", ".java")


def load_cases(path):
    cases = []
    for q, e in re.findall(r"query: (.*)\n\s+expect: \[\{(.*)\}\]", open(path).read()):
        cases.append((q, dict(kv.split(": ") for kv in e.split(", "))))
    return cases


def load_files():
    text = {}
    for d, _, fs in os.walk("."):
        if "/." in d or d.startswith("./."):
            continue
        for f in fs:
            if f.endswith(EXT):
                try:
                    text[os.path.join(d, f)[2:]] = open(os.path.join(d, f), errors="ignore").read()
                except OSError:
                    pass
    return text


def baseline(q, text, tests):
    terms = [w for w in re.findall(r"[A-Za-z_]{4,}", q.lower()) if w not in STOP]
    scored = []
    for f, t in text.items():
        if not tests and re.search(r"(_test\.go|(^|/)tests?/)", f):
            continue
        low = t.lower()
        d = sum(1 for w in terms if w in low)
        if d:
            scored.append((d, sum(low.count(w) for w in terms) / max(1, len(low)) * 1000, f))
    scored.sort(reverse=True)
    return [f for _, _, f in scored][:3]


class Server:
    def __init__(self):
        self.p = subprocess.Popen(["onfly", "serve"], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)
        self.n = 0
        self.call("initialize", {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "bench", "version": "0"}})
        self.p.stdin.write(json.dumps({"jsonrpc": "2.0", "method": "notifications/initialized"}) + "\n")
        self.p.stdin.flush()

    def call(self, method, params):
        self.n += 1
        self.p.stdin.write(json.dumps({"jsonrpc": "2.0", "id": self.n, "method": method, "params": params}) + "\n")
        self.p.stdin.flush()
        while True:
            m = json.loads(self.p.stdout.readline())
            if m.get("id") == self.n:
                return m["result"]

    def focus(self, q):
        return self.call("tools/call", {"name": "onfly_focus", "arguments": {"query": q, "limit": 5}})["content"][0]["text"]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("cases")
    ap.add_argument("--min", type=int, default=0, help="fail when fewer questions are found")
    a = ap.parse_args()
    cases, text, srv = load_cases(a.cases), load_files(), Server()
    tok = lambda s: len(s) // 4
    hit = hit1 = ga = gn = 0
    t_focus = t_ga = t_gn = 0
    lat = []
    for q, e in cases:
        alts = e["file"].split("|")
        t0 = time.time()
        out = srv.focus(q)
        lat.append((time.time() - t0) * 1000)
        syms = re.findall(r'<symbol name="([^"]*)" at="([^":]+)', out)
        ok = any(any(f.endswith(x) for x in alts) and ("symbol" not in e or s == e["symbol"]) for s, f in syms)
        hit += ok
        hit1 += bool(syms) and any(syms[0][1].endswith(x) for x in alts)
        top_all, top_not = baseline(q, text, True), baseline(q, text, False)
        ga += any(f.endswith(x) for f in top_all for x in alts)
        gn += any(f.endswith(x) for f in top_not for x in alts)
        t_focus += tok(out)
        t_ga += sum(tok(text[f]) for f in top_all)
        t_gn += sum(tok(text[f]) for f in top_not)
        print("%-4s %s" % ("hit" if ok else "miss", q[:90]))
    n = len(cases)
    lat.sort()
    print("\nquestions %d | onfly_focus top5 %d (first %d) | keyword top3 %d, without tests %d" % (n, hit, hit1, ga, gn))
    print("tokens: focus %d | keyword+read %d (%d without tests) | median latency %.0f ms" % (t_focus, t_ga, t_gn, lat[len(lat) // 2] if lat else 0))
    srv.p.terminate()
    sys.exit(1 if hit < a.min else 0)


main()
