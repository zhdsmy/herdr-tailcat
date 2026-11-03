#!/usr/bin/env python3
"""Scratch-only DERP fault matrix (default core timing). Exit 2 means review needed.

Default core reconnect/heartbeat timings are always used. On an architectural
conflict this runner preserves evidence, cleans scratch, and exits with a review-needed status.
"""
import argparse
import datetime
import fcntl
import json
import math
import os
import pathlib
import plistlib
import re
import secrets
import select
import shlex
import shutil
import signal
import struct
import subprocess
import sys
import tempfile
import termios
import threading
import time

from process_guard import core_candidate, process_environment, scratch_owned, protected_snapshot, retained_snapshot, identity, Ownership, LaunchdAudit, HomeRecovery, home_snapshot
from scratch_exec import assert_safe_executable, copy_executable

ROOT = pathlib.Path(__file__).resolve().parents[1]
FIELDS = ("XDG_STATE_HOME", "XDG_CONFIG_HOME", "XDG_RUNTIME_DIR", "HERDR_TAILCAT_HOME")
PS_ENV = dict(os.environ, LC_ALL="C")


class PlanStop(RuntimeError):
    pass


def processes():
    out = subprocess.check_output(["ps", "-axo", "pid=,ppid=,lstart=,command="], env=PS_ENV).decode()
    rows = {}
    for line in out.splitlines():
        parts = line.strip().split(None, 7)
        if len(parts) == 8:
            rows[int(parts[0])] = dict(pid=int(parts[0]), ppid=int(parts[1]),
                                     start=" ".join(parts[2:7]), command=parts[7])
    return rows


def app_connects(rows):
    """Discover every App-owned connect, retaining PID/start/parent identity."""
    return {str(pid): dict(connect=row, parent=rows[row["ppid"]])
            for pid, row in rows.items()
            if core_candidate(row) and row["command"].endswith(" connect")
            and "Herdr.app/Contents/MacOS/" in rows.get(row["ppid"], {}).get("command", "")}


def stable_snapshot(expected, sample, record, pause=time.sleep):
    for attempt in range(2):
        actual=sample()
        if actual==expected:return actual
        record(dict(attempt=attempt+1,added={k:v for k,v in actual.items() if k not in expected},
                    removed={k:v for k,v in expected.items() if k not in actual},
                    changed={k:dict(before=expected[k],after=v) for k,v in actual.items() if k in expected and expected[k]!=v}))
        if attempt==0:pause(.5)
    raise PlanStop("real core baseline changed on two consecutive snapshots; no signal sent")


class Viewer:
    def __init__(self, lab, address):
        self.lab = lab
        self.master, slave = os.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 24, 120, 0, 0))
        self.child = subprocess.Popen([lab.hc_path, "attach", address], env=lab.client, cwd=lab.scratch,
                                      stdin=slave, stdout=slave, stderr=slave)
        lab.ownership.record(self.child)
        os.close(slave)
        self.data = bytearray()
        self.notices = []
        self.notice_end = 0
        self.closed = threading.Event()
        self.thread = threading.Thread(target=self.collect, daemon=True)
        self.thread.start()

    def collect(self):
        with (self.lab.scratch / "viewer.bin").open("ab", buffering=0) as f:
            while not self.closed.is_set():
                if select.select([self.master], [], [], .1)[0]:
                    try:
                        chunk = os.read(self.master, 65536)
                    except OSError:
                        return
                    if not chunk:
                        return
                    self.data.extend(chunk)
                    f.write(chunk)
                    for match in re.finditer(rb"line to (\w+) lost; reconnecting", self.data):
                        if match.end() > self.notice_end:
                            arrived = time.monotonic()
                            self.notices.append((match.start(), match.group(0), arrived))
                            self.lab.event("viewer-notice", machine=match.group(1).decode(), byte_offset=match.start())
                            self.notice_end = match.end()

    def wait(self, needle, offset=0, timeout=10):
        end = time.monotonic() + timeout
        while time.monotonic() < end:
            if needle in bytes(self.data[offset:]):
                return next((at for pos, text, at in self.notices if pos >= offset and needle in text), time.monotonic())
            if self.child.poll() is not None:
                raise PlanStop("persistent attach viewer exited (%s)" % self.child.returncode)
            time.sleep(.05)
        raise AssertionError("viewer did not receive expected output")

    def marker(self, tag, readback=None):
        nonce = secrets.token_hex(12)
        # The output marker never appears as a contiguous string in the input.
        marker = (tag + "_" + nonce[:12] + "_" + nonce[12:]).encode()
        offset = len(self.data)
        command = "printf '%s_%s_%s\\n' '%s' '%s' '%s'\r" % ("%s", "%s", "%s", tag, nonce[:12], nonce[12:])
        os.write(self.master, command.encode())
        if readback is None:
            self.wait(marker, offset)
        else:
            verify_marker(self, marker, offset, readback, self.lab)
        return marker

    def close(self):
        if self.child.poll() is None:
            os.write(self.master, b"\x1c")
            try:
                self.child.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.lab.send_signal(self.child.pid, signal.SIGTERM)
                self.child.wait(timeout=5)
        self.closed.set()
        self.thread.join(timeout=2)
        os.close(self.master)


def verify_marker(viewer, marker, offset, readback, lab):
    viewer_error=None
    try:viewer.wait(marker,offset)
    except (AssertionError, PlanStop) as e:viewer_error=str(e)
    assert marker in readback(),"S1 read did not contain final marker"
    if viewer_error:
        (lab.scratch/"S1-viewer-tail.bin").write_bytes(bytes(viewer.data[-4096:]))
        lab.event("warning",message="S1 read succeeded but viewer did not receive marker",detail=viewer_error)


class Lab:
    def __init__(self,prefix="tc-faults-"):
        os.umask(0o077)
        self.scratch = pathlib.Path(tempfile.mkdtemp(prefix=prefix, dir="/tmp")).resolve()
        self.epoch = time.monotonic()
        self.case = "setup"
        self.event_lock = threading.Lock()
        self.event_log = (self.scratch / "timeline.jsonl").open("w", buffering=1)
        self.monitor_stop = threading.Event()
        self.machine_states = None
        self.before = processes()
        self.real_before = protected_snapshot(self.before, self.scratch)
        self.save("core-before.json", self.real_before)
        self.save("process-before.json", self.before)
        self.results = {}
        self.audit = []
        self.frozen_core = None
        self.frozen_serve = None
        self.service_target = None
        self.service_plist = None
        self.viewer = None
        self.serve = None
        self.log = (self.scratch / "commands.log").open("w", buffering=1)
        self.base = dict(os.environ, TS_DEBUG_ALWAYS_USE_DERP="true", HERDR_TAILCAT_IDLE="5s", HERDR_TAILCAT_TRACE_EXIT="1")
        for name in ("HC_SOCKET", "HC_TERMINAL_ID", "HC_RECONNECT_MS", "HC_HEARTBEAT_MS", "TS_DEBUG_ADDR"):
            self.base.pop(name, None)
        self.client = self.environment("a", "tc-client")
        self.server = self.environment("b", "tc-server")
        # A private copy makes every test core/connect command provably scratch-owned.
        bindir = self.scratch / "bin"
        self.bin = copy_executable(ROOT / "bin/herdr-tailcat", self.scratch, "herdr-tailcat")
        self.core_path = copy_executable(pathlib.Path(shutil.which("herdr-core")).resolve(), self.scratch, "herdr-core")
        self.save("executables.json", dict(tailcat=self.bin, core=self.core_path))
        self.hc_path = str(bindir / "hc")
        pathlib.Path(self.hc_path).symlink_to("herdr-core")
        self.ownership = Ownership(self.scratch, self.hc_path, processes, self.real_before)
        self.ownership.protected.update(int(pid) for pid in home_snapshot(self.before))
        self.home_recovery = HomeRecovery(self.before, processes, self.check_fixed_core,
                                          lambda details: self.event("home-recovery", **details))
        self.ownership.register(self.client)
        self.ownership.register(self.server)
        self.launchd_audit = LaunchdAudit(self.scratch, self.ownership)
        self.save("launchd-before.json", self.launchd_audit.before)
        self.monitor = threading.Thread(target=self.monitor_logs, daemon=True)
        self.monitor.start()
        print("Scratch evidence:", self.scratch, flush=True)

    def event(self, kind, **details):
        row = dict(kind=kind, case=self.case, monotonic=time.monotonic(),
                   elapsed=round(time.monotonic()-self.epoch, 6),
                   utc=datetime.datetime.now(datetime.timezone.utc).isoformat(), **details)
        with self.event_lock:
            self.event_log.write(json.dumps(row)+chr(10))
        return row["monotonic"]

    def begin(self, case):
        self.case = case
        return len(self.viewer.data), self.event("begin")

    def monitor_logs(self):
        positions = {}
        while not self.monitor_stop.wait(.2):
            for side, session in (("a", "tc-client"), ("b", "tc-work"), ("b", "tc-other"), ("b", "tc-k2b")):
                p = self.scratch / side / "s/herdr-core" / session / "core.log"
                try:
                    data = p.read_bytes()
                except FileNotFoundError:
                    continue
                old = positions.get(p, 0)
                if len(data) < old:
                    old = 0
                end = data.rfind(bytes([10]))+1
                for line in data[old:end].decode(errors="replace").splitlines():
                    self.event("core-log-arrived", session=session, line=line)
                positions[p] = end

    def machines(self):
        rows = self.json_hc(self.client, "machine", "ls")["machines"]
        states = {m["name"]: m["state"] for m in rows}
        if states != self.machine_states:
            self.event("machine-state", states=states)
            self.machine_states = states
        return rows

    def wait_notice(self, offset, deadline):
        needle = b"line to box lost; reconnecting"
        while True:
            if needle in bytes(self.viewer.data[offset:]):
                arrived = self.viewer.wait(needle, offset, timeout=.2)
                if arrived <= deadline:
                    return arrived
                break
            if time.monotonic() >= deadline:
                break
            self.machines()  # Local status only; never sends viewer input.
            time.sleep(.2)
        raise PlanStop("%s: passive line-loss notice exceeded deadline" % self.case)

    def save(self, name, value):
        (self.scratch / name).write_text(json.dumps(value, indent=2) + "\n")

    def mark(self, case, **details):
        self.event("result", result_case=case, details=details)
        self.results[case] = details
        self.save("results.json", self.results)
        print(case + ": " + json.dumps(details), flush=True)

    def environment(self, side, session):
        assert session.startswith("tc-")
        d = self.scratch / side
        env = dict(self.base, HC_SESSION=session, XDG_STATE_HOME=str(d / "s"),
                   XDG_CONFIG_HOME=str(d / "c"), XDG_RUNTIME_DIR=str(d / "r"),
                   HERDR_TAILCAT_HOME=str(d / "tailcat"))
        for field in FIELDS:
            pathlib.Path(env[field]).mkdir(parents=True, exist_ok=True, mode=0o700)
        if hasattr(self, "ownership"): self.ownership.register(env)
        return env

    def run(self, argv, env, *, data=None, secret=False, check=True, timeout=25):
        start = time.monotonic()
        if argv[0] in (self.bin, self.core_path, self.hc_path):
            assert_safe_executable(argv[0])
        r = subprocess.run(argv, env=env, cwd=self.scratch, input=data, capture_output=True, timeout=timeout)
        self.log.write(shlex.join(argv) + "\n")
        if not secret:
            self.log.write(r.stdout.decode(errors="replace"))
        self.log.write(r.stderr.decode(errors="replace"))
        self.log.write("exit=%d elapsed=%.3f\n" % (r.returncode, time.monotonic() - start))
        if check and r.returncode:
            raise AssertionError("command exit %d (see private commands.log)" % r.returncode)
        return r

    def hc(self, env, *args, **kwargs):
        assert env["HC_SESSION"].startswith("tc-")
        assert all(str(self.scratch) in env[n] for n in FIELDS)
        assert "HC_SOCKET" not in env and "HC_TERMINAL_ID" not in env
        return self.run([self.hc_path, *args], env, **kwargs)

    def tc(self, env, *args, **kwargs):
        return self.run([self.bin, *args], env, **kwargs)

    def json_hc(self, env, *args, **kwargs):
        return json.loads(self.hc(env, "--json", *args, **kwargs).stdout)

    def send_signal(self, pid, sig):
        rows = processes()
        row = rows.get(pid)
        if row is None:
            return
        # Cleanup continues after a baseline failure, but never bypasses fresh
        # scratch ownership proof, the pre-existing PID exclusion or identity.
        if self.case != "cleanup":
            self.check_real_core()
        assert pid not in self.before, "refusing signal to a pre-existing process"
        proof = self.ownership.prove(row, rows, fresh=True)
        if not proof:
            raise PlanStop("cannot prove scratch process ownership; refusing signal")
        if "--hc-launchd" in row["command"] and sig in (signal.SIGTERM, signal.SIGKILL) and not (sig == signal.SIGKILL and self.case == "K2a"):
            raise PlanStop("normal launchd core stop must use scratch hc server stop")
        current = processes().get(pid)
        if current is None:
            return
        assert current == row, "PID identity changed"
        self.audit.append(dict(**row, signal=sig.name, ownership_proof=proof))
        self.save("signal-audit.json", self.audit)
        self.event("signal", pid=pid, signal=sig.name)
        os.kill(pid, sig)

    def check_fixed_core(self):
        return stable_snapshot(self.real_before,lambda:retained_snapshot(self.real_before, processes()),
                               lambda diff:self.event("real-core-difference",**diff))

    def check_real_core(self):
        fixed = self.check_fixed_core()
        try:
            home = self.home_recovery.check()
        except RuntimeError as e:
            raise PlanStop(str(e)) from e
        self.ownership.protected.update(int(pid) for pid in home)
        return fixed

    def owned(self):
        rows = processes()
        owned = {p: r for p, r in rows.items() if p not in self.before and
                (core_candidate(r) or str(self.scratch) in r["command"]) and
                self.ownership.prove(r, rows)}
        self.launchd_audit.observe(owned)
        return owned

    def matching(self, text):
        return [r for r in self.owned().values() if text in r["command"]]

    def unique(self, text):
        rows = self.matching(text)
        assert len(rows) == 1, "expected one %s, got %s" % (text, rows)
        return rows[0]

    def wait_gone(self, pids, timeout):
        end = time.monotonic() + timeout
        while time.monotonic() < end:
            if not set(pids) & set(processes()):
                return
            time.sleep(.05)
        raise AssertionError("test processes did not exit: %s" % pids)

    def start_serve(self):
        f = (self.scratch / "serve-process.log").open("ab")
        self.serve = subprocess.Popen([self.bin, "serve"], env=self.server, cwd=self.scratch, stdin=subprocess.DEVNULL,
                                      stdout=f, stderr=f, start_new_session=True)
        f.close()
        self.ownership.record(self.serve)
        self.event("serve-started", pid=self.serve.pid)

    def add(self, name, session):
        return self.json_hc(self.client, "machine", "add", "--via", shlex.quote(self.bin) + " via p1",
                            "--name", name, "--session", session)

    def connected(self, names=("box", "box2"), timeout=35):
        start = time.monotonic()
        last = None
        while time.monotonic() - start < timeout:
            last = self.machines()
            if {m["name"] for m in last} == set(names) and all(m["state"] == "connected" for m in last):
                return time.monotonic() - start
            time.sleep(.1)
        self.save("connection-timeout.json", last)
        raise PlanStop("machines not automatically connected within %ss" % timeout)

    def terminal_pid(self):
        return self.json_hc(self.client, "get", self.address)["pid"]

    def scan(self):
        needles = []
        for p in self.scratch.rglob("*"):
            if p.name == "server.key":
                identity = json.loads(p.read_text())
                needles += [identity["Private"], identity["Public"]["PresharedKey"]]
            elif p.name == "client.key":
                needles += [json.loads(p.read_text())]
            elif p.name == "profile.json":
                needles += [json.loads(p.read_text())["address"]]
        words = set()
        for value in needles:
            raw = value.encode()
            words.update([raw, raw[:24]])
            if b":" in raw:
                body = raw.split(b":", 1)[1]
                words.update([body, body[:24]])
        if hasattr(self, "short"):
            words.update([self.short, self.short[:24]])
        hits = []
        for p in self.scratch.rglob("*"):
            if p.is_file() and p.name not in ("server.key", "client.key", "profile.json", "herdr-core", "hc"):
                data = p.read_bytes()
                if any(w and w in data for w in words):
                    hits.append(str(p.relative_to(self.scratch)))
        assert not hits, "secret hits in " + repr(hits)
        return len(words)

    def settled(self, names=("box", "box2"), timeout=10):
        end = time.monotonic() + timeout
        while time.monotonic() < end:
            rows = self.owned()
            vias = [r for r in rows.values() if r["command"].startswith(self.bin + " via ")]
            daemons = [r for r in rows.values() if r["command"] == self.bin + " daemon p1"]
            connects = [r for r in rows.values() if r["command"].startswith(self.core_path + " connect ")]
            if len(vias) == len(names) and len(connects) == len(names) and len(daemons) == bool(names):
                self.save("process-settled-%s.json" % len(self.results), rows)
                return
            time.sleep(.1)
        self.save("process-unsettled.json", self.owned())
        raise AssertionError("unexpected proxy process counts (see process-unsettled.json)")

    def setup(self):
        version = self.hc(self.client, "--version").stdout.decode().strip()
        assert re.search(r"0[.]1[.](16|17)\b", version), version
        self.save("core-version.json", dict(version=version))
        self.tc(self.server, "server", "init")
        cfgpath = pathlib.Path(self.server["HERDR_TAILCAT_HOME"]) / "server/config.json"
        cfg = json.loads(cfgpath.read_text()); cfg["herdr_core"] = self.core_path
        cfgpath.write_text(json.dumps(cfg))
        full = self.tc(self.server, "server", "show-address", "--full", secret=True).stdout.strip()
        # Short address is also secret; include it in scans via a private in-memory field.
        self.short = self.tc(self.server, "server", "show-address", secret=True).stdout.strip()
        for profile in ("p1", "p2"):
            self.tc(self.client, "profile", "add", profile, data=full)
        public = self.tc(self.client, "profile", "pubkey", "p1").stdout.decode().strip()
        self.tc(self.server, "server", "allow", "add", public)
        for session in ("tc-work", "tc-other", "tc-k2b"):
            self.tc(self.server, "server", "export", "add", session)
        self.start_serve()
        self.first = self.add("box", "tc-work")
        self.second = self.add("box2", "tc-other")
        self.connected()
        terminal = self.json_hc(self.client, "run", "--bg", "--on", "box", "--", "sh")
        self.address = terminal["address"]
        self.original_pid = self.terminal_pid()
        self.viewer = Viewer(self, self.address)
        time.sleep(.5)
        self.viewer.marker("SETUP")
        self.event("fixture",address=self.address,pid=self.original_pid)
        self.settled()
        self.mark("setup", status="PASS", address=self.address, pid=self.original_pid,
                  default_reconnect=True, default_heartbeat=True, forced_derp=True)

    def verify(self, case, offset, start, *, notice=True, other_pid=None):
        elapsed = self.connected()
        if other_pid is not None:
            assert self.unique("connect --session tc-other")["pid"] == other_pid, "box2 line changed"
        assert self.terminal_pid() == self.original_pid, "original shell PID changed"
        if notice:
            self.viewer.wait(b"line to box lost; reconnecting", offset)
        marker = self.viewer.marker(case)
        assert marker in self.hc(self.client, "read", self.address).stdout
        self.settled()
        count = self.scan()
        self.mark(case, status="PASS", recovery_seconds=round(time.monotonic()-start, 3),
                  connected_wait=round(elapsed, 3), terminal_pid=self.original_pid,
                  viewer_notice=notice, viewer_fresh_marker=True, no_orphans=True,
                  secret_patterns=count, secret_hits=0)

    def via_rows(self):
        return [r for r in self.owned().values() if r["command"].startswith(self.bin + " via ")]

    def exit_reasons(self, pids):
        p = pathlib.Path(self.client["HERDR_TAILCAT_HOME"]) / "profiles/p1/via-exit.log"
        rows = p.read_text().splitlines() if p.exists() else []
        return {str(pid): [l.split("reason=", 1)[1] for l in rows if l.startswith("pid=%d " % pid)] for pid in pids}

    def core_status(self, session):
        return self.json_hc(self.environment("b", session), "server", "status")

    def k2a(self):
        self.case = "K2a"
        self.event("begin")
        vias = self.via_rows(); assert len(vias) == 2
        local = self.json_hc(self.client, "server", "status")
        assert all(r["ppid"] == local["pid"] for r in vias)
        remote_before = [self.core_status(s) for s in ("tc-work", "tc-other")]
        connects = [r["pid"] for r in self.matching(" connect --session ")]
        start = time.monotonic()
        self.send_signal(local["pid"], signal.SIGKILL)
        self.wait_gone([r["pid"] for r in vias], 2)
        exit_elapsed = time.monotonic()-start
        reasons = self.exit_reasons([r["pid"] for r in vias])
        assert all(any(v in ("stdin-eof", "parent-changed") for v in entries) for entries in reasons.values()), reasons
        self.wait_gone(connects, max(.1, 10-(time.monotonic()-start)))
        daemons = self.matching(" daemon p1")
        self.wait_gone([r["pid"] for r in daemons], 8)
        remote_after = [self.core_status(s) for s in ("tc-work", "tc-other")]
        assert [(r["pid"],r["core"]["id"]) for r in remote_before] == [(r["pid"],r["core"]["id"]) for r in remote_after]
        self.viewer.child.wait(timeout=5)
        viewer_exit = self.viewer.child.returncode
        self.viewer.close(); self.viewer = None
        start = time.monotonic()
        self.hc(self.client, "server", "status")
        recovery = self.connected(timeout=max(.1,35-(time.monotonic()-start)))
        assert self.terminal_pid() == self.original_pid
        self.viewer = Viewer(self, self.address); time.sleep(.5)
        marker = self.viewer.marker("K2a")
        assert marker in self.hc(self.client,"read",self.address).stdout
        self.settled()
        self.mark("K2a",status="PASS",via_exit_seconds=round(exit_elapsed,3),exit_reasons=reasons,
                  viewer_exit=viewer_exit,new_viewer_marker=True,recovery_wait=round(recovery,3),
                  remote_cores_unchanged=True,terminal_pid=self.original_pid,secret_patterns=self.scan())

    def k2b(self):
        stable = {r["pid"] for r in self.via_rows()+self.matching(" connect --session ")}
        for kill_parent in (True,False):
            self.case = "K2b-parent" if kill_parent else "K2b-stdin"
            self.event("begin")
            line = (ROOT / "testdata/lines/v0.1.16/work.txt").read_text().removesuffix("\n").replace("HC_SESSION=work ", "HC_SESSION=tc-k2b ")
            command = shlex.quote(self.bin) + ' via p1 "$1"; :'
            errfile = (self.scratch / "K2b-stderr.log").open("ab")
            child = subprocess.Popen(["/bin/sh","-c",command,"sh",line],env=self.client,cwd=self.scratch,
                                     stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=errfile)
            self.ownership.record(child)
            errfile.close()
            try:
                until = time.monotonic()+5
                via = None
                while time.monotonic()<until:
                    via = next((r for r in self.via_rows() if r["ppid"]==child.pid),None)
                    if via: break
                    time.sleep(.05)
                assert via is not None, "external sh was not retained"
                child.stdin.write((json.dumps({"jsonrpc":"2.0","id":1,"method":"hello","params":{"generation":1}})+"\n").encode())
                child.stdin.flush()
                reply = b""; until=time.monotonic()+20
                while b"\n" not in reply and time.monotonic()<until:
                    if select.select([child.stdout],[],[],.1)[0]:
                        data=os.read(child.stdout.fileno(),65536)
                        if not data: break
                        reply+=data
                hello=json.loads(reply.split(b"\n",1)[0])
                self.save("K2b-hello.json", hello)
                assert hello["result"]["core"]["id"] == self.core_status("tc-k2b")["core"]["id"]
                extra = [r["pid"] for r in self.matching(" connect --session ") if r["pid"] not in stable]
                assert len(extra)==1
                start=time.monotonic()
                if kill_parent:
                    self.send_signal(child.pid,signal.SIGKILL)
                    child.wait(timeout=2)
                    # Pipes remain deliberately open until the via has exited.
                    self.wait_gone([via["pid"]],2)
                else:
                    child.stdin.close()
                    self.wait_gone([via["pid"]],5)
                    child.wait(timeout=2)
                elapsed=time.monotonic()-start
                reasons=self.exit_reasons([via["pid"]])
                expected="parent-changed" if kill_parent else "stdin-eof"
                assert expected in reasons[str(via["pid"])],reasons
                self.wait_gone(extra,max(.1,10-elapsed))
                self.connected()
                assert {r["pid"] for r in self.via_rows()+self.matching(" connect --session ")} == stable
                self.settled()
                self.mark("K2b-parent" if kill_parent else "K2b-stdin",status="PASS",via_exit_seconds=round(elapsed,3),
                          exit_reasons=reasons,existing_lines_unchanged=True,secret_patterns=self.scan())
            finally:
                for pipe in (child.stdin,child.stdout):
                    if pipe and not pipe.closed: pipe.close()
                if child.poll() is None:
                    self.send_signal(child.pid,signal.SIGTERM);child.wait(timeout=5)

    def k3b(self):
        self.case = "K3b"
        self.event("begin")
        local=self.json_hc(self.client,"server","status")
        remote=[self.core_status(s) for s in ("tc-work","tc-other")]
        connects=[r["pid"] for r in self.matching(" connect --session ")]
        offset,start=len(self.viewer.data),time.monotonic()
        self.send_signal(local["pid"],signal.SIGSTOP);self.frozen_core=local["pid"]
        self.send_signal(self.unique(" daemon p1")["pid"],signal.SIGKILL)
        end=start+105;gone={};last_update=0
        while time.monotonic()<end:
            rows=processes();now=time.monotonic()-start
            for pid in connects:
                if pid not in rows and pid not in gone:gone[pid]=round(now,3)
            if len(gone)==len(connects):break
            if now-last_update>=25:
                print("K3b default 90s idle: %.1fs, connects remaining=%d"%(now,len(connects)-len(gone)),flush=True);last_update=now
            time.sleep(.2)
        self.save("K3b-reap.json",dict(connects=connects,reaped_seconds=gone))
        if len(gone)!=len(connects):raise PlanStop("K3b connects not reclaimed within default 90s + 15s")
        after=[self.core_status(s) for s in ("tc-work","tc-other")]
        assert [(r["pid"],r["core"]["id"]) for r in remote]==[(r["pid"],r["core"]["id"]) for r in after]
        self.send_signal(local["pid"],signal.SIGCONT);self.frozen_core=None
        self.verify("K3b",offset,time.monotonic())
        self.mark("K3b-default-idle",status="PASS",timeout_seconds=90,reaped_seconds=gone)

    def rebuild_count(self):
        log=pathlib.Path(self.client["HERDR_TAILCAT_HOME"])/"profiles/p1/daemon.log"
        return log.read_bytes().count(b"client rebuilt generation=")

    def new_lines(self, old_vias, deadline):
        while time.monotonic() < deadline:
            rows = self.machines()
            vias = self.via_rows()
            if len(rows)==2 and all(m["state"]=="connected" for m in rows) and len(vias)==2 and not old_vias.intersection(r["pid"] for r in vias):
                self.event("new-lines-connected", pids=[r["pid"] for r in vias],previous_pids=sorted(old_vias))
                return vias
            time.sleep(.2)
        self.save(self.case+"-line-timeout.json",dict(previous_pids=sorted(old_vias),vias=self.via_rows(),machines=self.machines()))
        raise PlanStop("%s: new lines did not recover within deadline" % self.case)

    def k4(self, planned=False):
        case = "K4b" if planned else "K4"
        daemon=self.unique(" daemon p1")["pid"]
        before=self.rebuild_count()
        old_vias={r["pid"] for r in self.via_rows()}
        offset,start=self.begin(case)
        self.send_signal(self.serve.pid,signal.SIGTERM if planned else signal.SIGKILL)
        self.serve.wait(timeout=10 if planned else 5)
        exited=time.monotonic()
        self.event("serve-exited", pid=self.serve.pid)
        time.sleep(3);self.start_serve()
        # Passive detection: do not write to the viewer to manufacture a reset.
        detected=self.wait_notice(offset,start+(5 if planned else 50))
        if detected-start > (5 if planned else 50):
            raise PlanStop(case+": notice exceeded deadline")
        vias=self.new_lines(old_vias,start+35 if planned else detected+25)
        recovered=time.monotonic()
        # ps lstart rounds birth down to the second: this is a conservative
        # upper bound on each current via's total lifetime through hello.
        ages={str(r["pid"]):round(time.time()-time.mktime(time.strptime(r["start"],"%a %b %d %H:%M:%S %Y")),3) for r in vias}
        if not planned and max(ages.values())>=20:
            raise PlanStop("K4: current via hello age upper bound exceeded 20s")
        self.verify(case,offset,start)
        after=self.rebuild_count()
        if not planned:
            if self.unique(" daemon p1")["pid"]!=daemon:raise PlanStop("K4 unexpectedly replaced daemon")
            if after-before!=1:raise PlanStop("K4 reconstruction count was %d"%(after-before))
        self.mark(case+"-timing",status="PASS",rebuilds=after-before,
                  notice_seconds=round(detected-start,3),serve_exit_seconds=round(exited-start,3),
                  redial_seconds=round(recovered-detected,3),recovery_seconds=round(recovered-start,3),
                  via_hello_age_upper_seconds=ages)

    def new_fixture(self, tag):
        terminal=self.json_hc(self.client,"run","--bg","--on","box","--","sh")
        self.address=terminal["address"]
        self.original_pid=self.terminal_pid()
        self.viewer=Viewer(self,self.address)
        time.sleep(.3)
        marker=self.viewer.marker(tag)
        assert marker in self.hc(self.client,"read",self.address).stdout
        self.event("fixture",address=self.address,pid=self.original_pid)

    def k5(self):
        remote=self.core_status("tc-work")
        other=self.unique("connect --session tc-other")["pid"]
        old_vias={r["pid"] for r in self.via_rows() if "HC_SESSION=tc-work " in r["command"]}
        offset,start=self.begin("K5")
        self.hc(self.environment("b","tc-work"),"server","restart")
        self.new_lines(old_vias,start+35)
        after=self.core_status("tc-work")
        assert after["pid"]!=remote["pid"] and after["core"]["id"]==remote["core"]["id"]
        assert self.terminal_pid()==self.original_pid
        time.sleep(.3)
        old_viewer=self.viewer.child.poll()
        if old_viewer is not None:
            self.viewer.close();self.viewer=Viewer(self,self.address);time.sleep(.3)
        self.verify("K5",offset if old_viewer is None else 0,start,notice=False,other_pid=other)
        self.mark("K5-core-semantics",status="PASS",old_core_pid=remote["pid"],new_core_pid=after["pid"],
                  core_id_unchanged=True,original_viewer_exit=old_viewer,
                  viewer_reopened=old_viewer is not None,terminal_pid=self.original_pid)

    def k6(self):
        remote=self.core_status("tc-work");other_core=self.core_status("tc-other")
        other=self.unique("connect --session tc-other")["pid"]
        old_connect=self.unique("connect --session tc-work")["pid"]
        old_address,old_shell=self.address,self.original_pid
        old_vias={r["pid"] for r in self.via_rows() if "HC_SESSION=tc-work " in r["command"]}
        offset,start=self.begin("K6")
        self.hc(self.environment("b","tc-work"),"server","stop")
        self.wait_gone([old_connect],max(.1,10-(time.monotonic()-start)))
        self.wait_gone([old_shell],max(.1,10-(time.monotonic()-start)))
        reaped=time.monotonic()-start
        self.new_lines(old_vias,start+35)
        after=self.core_status("tc-work")
        if after["core"]["id"]!=remote["core"]["id"]:raise PlanStop("K6 core id changed")
        other_after=self.core_status("tc-other")
        assert (other_after["pid"],other_after["core"]["id"])==(other_core["pid"],other_core["core"]["id"])
        assert self.unique("connect --session tc-other")["pid"]==other
        record=self.hc(self.client,"--json","get",old_address,check=False)
        text=(record.stdout+record.stderr).decode(errors="replace")
        self.save("K6-terminal-record.json",dict(returncode=record.returncode,output=text))
        removed=record.returncode!=0 and any(word in text.lower() for word in ("not found","unknown terminal","does not exist"))
        assert removed or (record.returncode==0 and "server_stop" in text),"unexpected K6 finish semantics"
        self.viewer.child.wait(timeout=10)
        viewer_exit=self.viewer.child.returncode
        self.save("K6-viewer-ending.json",dict(exit_code=viewer_exit,output=bytes(self.viewer.data[offset:]).decode(errors="replace")))
        self.viewer.close();self.viewer=None
        self.new_fixture("K6_NEW")
        self.settled();self.scan()
        self.mark("K6",status="PASS",old_address=old_address,old_shell_pid=old_shell,
                  old_shell_gone=True,old_connect_reaped_seconds=round(reaped,3),terminal_removed=removed,
                  viewer_exit=viewer_exit,core_id_unchanged=True,box2_unchanged=True,
                  new_address=self.address,new_shell_pid=self.original_pid,
                  recovery_and_fixture_seconds=round(time.monotonic()-start,3),secret_hits=0)

    def k7(self):
        local=self.json_hc(self.client,"server","status")
        old_vias={r["pid"] for r in self.via_rows()}
        old_connects={r["pid"] for r in self.matching(" connect --session ")}
        saved=self.viewer.marker("K7_SAVED")
        watcher=None
        if sys.platform=="darwin":
            watcher=select.kqueue()
            # macOS sys/event.h NOTE_EXITSTATUS: return the wait status when
            # the observer owns/can signal the exact PID. This sends no signal.
            watcher.control([select.kevent(pid,filter=select.KQ_FILTER_PROC,
                            flags=select.KQ_EV_ADD|select.KQ_EV_ENABLE|select.KQ_EV_ONESHOT,
                            fflags=select.KQ_NOTE_EXIT|0x04000000) for pid in old_vias],0,0)
        offset,start=self.begin("K7")
        self.hc(self.client,"server","restart")
        self.wait_gone(old_vias,max(.1,2-(time.monotonic()-start)))
        via_elapsed=time.monotonic()-start
        reasons=self.exit_reasons(old_vias)
        if watcher is not None:
            events=watcher.control(None,len(old_vias),.2);watcher.close()
            for event in events:
                status=event.data
                reason=("kernel-signal:"+signal.Signals(os.WTERMSIG(status)).name) if os.WIFSIGNALED(status) else "kernel-exit:%d"%os.WEXITSTATUS(status)
                reasons[str(event.ident)].append(reason)
                self.event("via-exit-observed",pid=event.ident,wait_status=status,reason=reason)
        if any(not value for value in reasons.values()):raise PlanStop("K7 exit reason not observed")
        self.wait_gone(old_connects,max(.1,10-(time.monotonic()-start)))
        connect_elapsed=time.monotonic()-start
        self.new_lines(old_vias,start+35)
        recovered=time.monotonic()-start
        after=self.json_hc(self.client,"server","status")
        assert after["pid"]!=local["pid"] and after["core"]["id"]==local["core"]["id"]
        assert self.terminal_pid()==self.original_pid
        self.viewer.child.wait(timeout=5)
        viewer_exit=self.viewer.child.returncode
        self.save("K7-viewer-ending.json",dict(exit_code=viewer_exit,output=bytes(self.viewer.data[offset:]).decode(errors="replace")))
        self.viewer.close();self.viewer=Viewer(self,self.address)
        self.viewer.wait(saved,timeout=10)
        marker=self.viewer.marker("K7_NEW")
        assert marker in self.hc(self.client,"read",self.address).stdout
        self.settled();self.scan()
        self.mark("K7",status="PASS",recovery_seconds=round(recovered,3),
                  via_exit_seconds=round(via_elapsed,3),connect_exit_seconds=round(connect_elapsed,3),
                  exit_reasons=reasons,original_viewer_exit=viewer_exit,saved_screen_seen=True,
                  terminal_pid=self.original_pid,address=self.address,core_id_unchanged=True,
                  new_viewer_marker=True,no_orphans=True,secret_hits=0)

    def k8(self):
        remote=[self.core_status(s) for s in ("tc-work","tc-other")]
        old_vias={r["pid"] for r in self.via_rows()}
        offset,start=self.begin("K8")
        self.send_signal(self.serve.pid,signal.SIGSTOP);self.frozen_serve=self.serve.pid
        detected=self.wait_notice(offset,start+50)
        while time.monotonic()<start+60:
            self.machines();time.sleep(.2)
        old_connects=[r["pid"] for r in self.matching(" connect --session ")]
        self.send_signal(self.serve.pid,signal.SIGCONT);self.frozen_serve=None
        resumed=self.event("serve-resumed")
        self.new_lines(old_vias,resumed+35)
        recovered=time.monotonic()
        self.settled(timeout=max(.1,105-(time.monotonic()-resumed)))
        remaining=set(old_connects).intersection(processes())
        if remaining:raise PlanStop("K8 old frozen connects remain")
        after=[self.core_status(s) for s in ("tc-work","tc-other")]
        assert [(v["pid"],v["core"]["id"]) for v in remote]==[(v["pid"],v["core"]["id"]) for v in after]
        self.verify("K8",offset,start)
        self.mark("K8-timing",status="PASS",notice_seconds=round(detected-start,3),
                  frozen_seconds=round(resumed-start,3),recovery_after_resume_seconds=round(recovered-resumed,3),
                  cleanup_after_resume_seconds=round(time.monotonic()-resumed,3),old_connects=old_connects)

    def k9(self):
        self.case="K9";self.event("begin")
        daemon=self.unique(" daemon p1")["pid"]
        for name,session,remaining in (("box","tc-work",("box2",)),("box2","tc-other",())):
            old=[r["pid"] for r in self.via_rows() if "HC_SESSION=%s "%session in r["command"]]
            old += [r["pid"] for r in self.matching("connect --session "+session)]
            start=time.monotonic();self.hc(self.client,"machine","rm",name)
            self.wait_gone(old,max(.1,10-(time.monotonic()-start)))
            self.connected(names=remaining)
            self.event("machine-removed",machine=name,proxy_exit_seconds=round(time.monotonic()-start,3))
        self.wait_gone([daemon],8)
        self.settled(names=())
        terminal=self.json_hc(self.environment("b","tc-work"),"get",self.address.split("/",1)[1])
        assert terminal["pid"]==self.original_pid
        self.scan()
        self.mark("K9",status="PASS",proxies_gone=True,daemon_idle_exit=True,remote_terminal_pid=self.original_pid,secret_hits=0)

    def restore(self):
        if self.viewer:
            self.viewer.close();self.viewer=None
        self.add("box","tc-work");self.add("box2","tc-other");self.connected()
        assert self.terminal_pid()==self.original_pid
        self.viewer=Viewer(self,self.address);time.sleep(.3);self.viewer.marker("RESTORE")
        self.settled()

    def s1(self):
        self.case="S1";start=self.event("begin")
        log=pathlib.Path(self.server["HERDR_TAILCAT_HOME"])/"serve.log"
        offset=log.stat().st_size;samples=[];exercises=[]
        for tick in range(61):
            due=start+tick*5
            if time.monotonic()<due:time.sleep(due-time.monotonic())
            rows=self.machines()
            assert {m["name"] for m in rows}=={"box","box2"} and all(m["state"]=="connected" for m in rows)
            samples.append(dict(seconds=round(time.monotonic()-start,3),states={m["name"]:m["state"] for m in rows}))
            if tick%12==0 and tick<60:
                for name in ("box","box2"):
                    marker="S1_"+secrets.token_hex(12)
                    terminal=self.json_hc(self.client,"run","--bg","--on",name,"--","sh","-c","printf '%s' "+shlex.quote(marker)+"; sleep 2")
                    deadline=time.monotonic()+5
                    while marker.encode() not in self.hc(self.client,"read",terminal["address"]).stdout:
                        if time.monotonic()>deadline:raise AssertionError("S1 read marker missing")
                        time.sleep(.1)
                    exercises.append(dict(machine=name,address=terminal["address"],seconds=round(time.monotonic()-start,3)))
                print("S1: %ds, both connected"%(tick*5),flush=True)
        data=log.read_bytes()[offset:]
        metrics={}
        for stream,value in re.findall(rb"stream=(\d+) client_silence_max_seconds=([0-9.]+)",data):
            key=stream.decode();metrics[key]=max(metrics.get(key,0),float(value))
        self.save("S1-samples.json",dict(samples=samples,exercises=exercises,client_silence=metrics))
        if len(metrics)!=2 or max(metrics.values())>=30:
            raise PlanStop("S1 healthy client silence not proven below 30s")
        assert self.terminal_pid()==self.original_pid
        self.viewer.marker("S1_DONE",readback=lambda:self.hc(self.client,"read",self.address).stdout)
        self.settled();self.scan()
        self.mark("S1",status="PASS",duration_seconds=round(time.monotonic()-start,3),samples=len(samples),
                  exercises=len(exercises),maximum_client_silence_seconds=max(metrics.values()),streams=metrics,secret_hits=0)

    def s2(self):
        self.case="S2";self.event("begin");times=[]
        if self.viewer:self.viewer.close();self.viewer=None
        self.hc(self.client,"machine","rm","box");self.hc(self.client,"machine","rm","box2")
        self.wait_gone([r["pid"] for r in self.via_rows()+self.matching(" connect --session ")],10)
        for trial in range(20):
            for daemon in self.matching(" daemon p1"):
                self.send_signal(daemon["pid"],signal.SIGTERM);self.wait_gone([daemon["pid"]],3)
            assert not self.matching(" daemon p1")
            start=self.event("cold-dial-start",trial=trial+1)
            self.add("box","tc-work");self.connected(names=("box",),timeout=20)
            elapsed=time.monotonic()-start;times.append(elapsed)
            self.event("cold-dial-connected",trial=trial+1,seconds=elapsed)
            self.hc(self.client,"machine","rm","box")
            self.wait_gone([r["pid"] for r in self.via_rows()+self.matching(" connect --session ")],10)
            print("S2: %d/20 %.3fs"%(trial+1,elapsed),flush=True)
        ordered=sorted(times);p95=ordered[math.ceil(.95*len(ordered))-1]
        p50=(ordered[9]+ordered[10])/2
        self.save("S2-cold-dials.json",dict(seconds=times,p50=p50,p95=p95,maximum=max(times),failures=0))
        if p95>=10:raise PlanStop("S2 p95 exceeded 10s")
        self.restore();self.scan()
        self.mark("S2",status="PASS",n=20,p50_seconds=round(p50,3),p95_seconds=round(p95,3),max_seconds=round(max(times),3),failures=0,secret_hits=0)

    def launch(self, *args, check=True):
        return self.run(["/bin/launchctl",*args],self.server,check=check,timeout=15)

    def assert_service_binary(self, path):
        arguments=plistlib.loads(pathlib.Path(path).read_bytes())["ProgramArguments"]
        assert arguments==[self.bin,"serve"], "service must execute the scratch binary"
        assert_safe_executable(arguments[0])

    def wait_job_absent(self, timeout=10):
        end=time.monotonic()+timeout
        while time.monotonic()<end:
            if self.launch("print",self.service_target,check=False).returncode!=0:
                self.event("launchd-absent",target=self.service_target)
                return
            time.sleep(.1)
        raise AssertionError("test launchd job still loaded")

    def l1(self):
        self.case="L1";self.event("begin")
        label="dev.herdr-tailcat.test."+secrets.token_hex(8)
        assert "test" in label
        directory=self.scratch/"launchd"
        path=self.tc(self.server,"server","install-service","--label",label,"--plist-dir",str(directory),"--force-derp").stdout.decode().strip()
        assert pathlib.Path(path).parent==directory
        self.assert_service_binary(path)
        self.service_plist=path;self.service_target="gui/%d/%s"%(os.getuid(),label)
        (self.scratch/"linux-service-unit.txt").write_bytes(self.tc(self.server,"server","install-service","--os","linux","--dry-run").stdout)
        remote=self.core_status("tc-work")
        old={r["pid"] for r in self.via_rows()}
        self.send_signal(self.serve.pid,signal.SIGTERM);self.serve.wait(timeout=10)
        self.launch("bootstrap","gui/%d"%os.getuid(),path)
        self.new_lines(old,time.monotonic()+35);self.settled()
        # Only herdr-tailcat serve matches this exact binary prefix; scratch
        # hc serve processes have their own scratch paths and are excluded below.
        hosted=next(r["pid"] for r in self.owned().values() if r["command"]==self.bin+" serve")
        offset,start=self.begin("L1-stop")
        # Capture active identities before injection. Core may already start a
        # replacement via while bootout is still draining the old server.
        old={r["pid"] for r in self.via_rows()}
        self.event("launchd-pre-stop-vias",pids=sorted(old))
        self.launch("bootout",self.service_target)
        self.wait_gone([hosted],10)
        self.wait_job_absent()
        after=self.core_status("tc-work")
        assert (after["pid"],after["core"]["id"])==(remote["pid"],remote["core"]["id"])
        direct=self.json_hc(self.environment("b","tc-work"),"get",self.address.split("/",1)[1])
        assert direct["pid"]==self.original_pid
        stopped=self.event("launchd-stopped-core-survives",core_pid=after["pid"],terminal_pid=direct["pid"])
        self.event("launchd-post-stop-vias",pids=[r["pid"] for r in self.via_rows()])
        self.launch("bootstrap","gui/%d"%os.getuid(),path)
        self.new_lines(old,time.monotonic()+35)
        self.verify("L1",offset,start)
        hosted=next(r["pid"] for r in self.owned().values() if r["command"]==self.bin+" serve")
        self.launch("bootout",self.service_target);self.wait_gone([hosted],10)
        self.wait_job_absent()
        absent=self.launch("print",self.service_target,check=False)
        assert absent.returncode!=0,"test launchd job still loaded"
        self.event("launchd-absent",target=self.service_target,returncode=absent.returncode)
        self.mark("L1-service",status="PASS",label=label,plist=path,core_pid=remote["pid"],
                  terminal_pid=self.original_pid,job_absent=True,stop_survival_seconds=round(stopped-start,3))

    def k1(self):
        offset, start = self.begin("K1")
        other = self.unique("connect --session tc-other")["pid"]
        via = next(r for r in self.matching(" via p1 ") if "HC_SESSION=tc-work " in r["command"])
        self.send_signal(via["pid"], signal.SIGKILL)
        self.verify("K1", offset, start, other_pid=other)

    def matrix(self):
        self.k1()

        self.k2a()
        self.k2b()

        offset, start = self.begin("K3")
        self.send_signal(self.unique(" daemon p1")["pid"], signal.SIGKILL)
        try:
            self.verify("K3", offset, start)
        except AssertionError as e:
            if "unexpected proxy process counts" not in str(e):
                raise
            self.mark("K3", status="STOP", reason=str(e),
                      elapsed_seconds=round(time.monotonic()-start,3),
                      evidence="process-unsettled.json")
            raise PlanStop("K3: proxy cleanup exceeded 10s; reviewer decision required") from e

        self.k3b()
        self.k4()
        self.k4(planned=True)
        self.k5()
        self.k6()
        self.k7()
        self.k8()
        self.k9()

    def cleanup(self):
        errors = []
        self.case = "cleanup"
        def attempt(label, action):
            try:
                return action()
            except Exception as e:
                errors.append(label + ": " + str(e))
                self.event("cleanup-error", action=label, kind=type(e).__name__)
                return None

        # Record baseline failures without abandoning proven scratch resources.
        attempt("protected baseline before cleanup", self.check_real_core)
        for field in ("frozen_serve", "frozen_core"):
            pid = getattr(self, field)
            if pid is not None:
                attempt(field, lambda pid=pid: self.send_signal(pid, signal.SIGCONT))
                setattr(self, field, None)
        if self.service_target is not None:
            def stop_service():
                loaded=self.launch("print",self.service_target,check=False)
                if loaded.returncode==0:self.launch("bootout",self.service_target)
                self.wait_job_absent()
            attempt("scratch service stop", stop_service)
        if self.viewer:
            attempt("viewer", self.viewer.close)
        for env in (self.client, self.environment("b", "tc-work"), self.environment("b", "tc-other"), self.environment("b", "tc-k2b")):
            attempt("scratch core stop", lambda env=env: self.hc(env, "server", "stop", check=False, timeout=12))
        # A test ending with serve offline can leave a legitimate dial pending.
        # Shut down the test daemon explicitly, rather than classify its bounded
        # pending dial as an orphan. K9 separately verifies natural idle exit.
        for daemon in attempt("find scratch daemons", lambda: self.matching(" daemon p1")) or []:
            self.event("cleanup-daemon-shutdown",pid=daemon["pid"])
            attempt("scratch daemon signal", lambda daemon=daemon: self.send_signal(daemon["pid"],signal.SIGTERM))
            attempt("scratch daemon exit", lambda daemon=daemon: self.wait_gone([daemon["pid"]],2))
        if self.serve and self.serve.poll() is None:
            def stop_serve():
                self.send_signal(self.serve.pid, signal.SIGTERM)
                try:
                    self.serve.wait(timeout=12)
                except subprocess.TimeoutExpired:
                    self.send_signal(self.serve.pid, signal.SIGKILL)
                    self.serve.wait(timeout=5)
            attempt("scratch serve stop", stop_serve)
        time.sleep(10)
        remaining = attempt("find remaining scratch", self.owned) or {}
        # Only exact, newly observed scratch children are eligible for final cleanup.
        leftovers = [r for r in remaining.values() if " attach " not in r["command"]]
        for row in leftovers:
            if "--hc-launchd" in row["command"]:
                errors.append("scratch core remains after hc server stop")
                continue
            attempt("orphan terminate", lambda row=row: self.send_signal(row["pid"], signal.SIGTERM))
        time.sleep(.3)
        for row in (attempt("find scratch after terminate", self.owned) or {}).values():
            if "--hc-launchd" not in row["command"]:
                attempt("orphan kill", lambda row=row: self.send_signal(row["pid"], signal.SIGKILL))
        after = processes()
        self.save("process-after.json", after)
        real_after = attempt("protected baseline after cleanup", self.check_real_core)
        self.save("core-after.json", real_after)
        self.monitor_stop.set()
        self.monitor.join(timeout=2)
        self.log.close()
        self.save("ownership-core-proofs.json", self.ownership.proven)
        launchd = attempt("scratch launchd audit", self.launchd_audit.finish)
        self.save("launchd-after.json", launchd)
        if launchd and (launchd["remaining_owned_jobs"] or launchd["remaining_owned_directories"]):
            errors.append("scratch launchd jobs/directories remain")
        # An unproven process is never signaled, but still makes cleanup fail.
        command_leftovers = [r for r in after.values() if str(self.scratch) in r["command"]]
        if command_leftovers:
            errors.append("process commands still contain this scratch path")
        self.mark("cleanup", status="PASS" if not errors and not leftovers else "FAIL",
                  errors=errors, leftovers=leftovers, remaining=command_leftovers,
                  real_core_unchanged=self.real_before == real_after, secret_patterns=self.scan())
        assert self.real_before == real_after, "real core snapshots changed"
        assert not errors and not leftovers, "cleanup required orphan removal"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--only",help="development subset, e.g. K5,K6,K7; omitted runs full default matrix")
    args=parser.parse_args()
    lab = Lab()
    code = 0
    try:
        lab.setup()
        if args.only:
            def k3():
                offset,start=lab.begin("K3")
                old=[r["pid"] for r in lab.matching(" connect --session ")]
                lab.send_signal(lab.unique(" daemon p1")["pid"],signal.SIGKILL)
                lab.wait_gone(old,max(.1,10-(time.monotonic()-start)))
                lab.verify("K3",offset,start)
            allowed={"K1":lab.k1,"K2a":lab.k2a,"K3":k3,"K4":lab.k4,"K4b":lambda:lab.k4(planned=True),"K5":lab.k5,"K6":lab.k6,"K7":lab.k7,"K8":lab.k8,"K9":lab.k9,"S1":lab.s1,"S2":lab.s2,"L1":lab.l1}
            for case in args.only.split(","):allowed[case]()
        else:
            lab.matrix()
            lab.restore()
            lab.s1()
            lab.s2()
            lab.l1()
            lab.mark("complete",status="PASS",default_core_timing=True)
    except PlanStop as e:
        lab.mark("STOP", reason=str(e)); code = 2
    except Exception as e:
        lab.mark("FAILURE", kind=type(e).__name__, reason=str(e)); code = 1
    finally:
        try:
            lab.cleanup()
        except Exception as e:
            lab.mark("CLEANUP_FAILURE", kind=type(e).__name__, reason=str(e)); code = 1
    print("Scratch evidence:", lab.scratch, flush=True)
    return code


if __name__ == "__main__":
    sys.exit(main())
