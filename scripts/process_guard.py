"""Scratch process proof. Never persist the full ps environment or trust argv alone."""
import os
import json
import pathlib
import re
import shlex
import subprocess
import time

PS_ENV = dict(os.environ, LC_ALL="C")


def core_candidate(row):
    try:
        argv = shlex.split(row["command"])
    except ValueError:
        return False
    return bool(argv) and (pathlib.Path(argv[0]).name in ("hc", "herdr-core") or "--hc-launchd" in argv)


def process_environment(row):
    try:
        text = subprocess.check_output(["ps", "eww", "-p", str(row["pid"]), "-o", "command="], env=PS_ENV, stderr=subprocess.DEVNULL).decode().strip()
    except (subprocess.CalledProcessError, OSError, UnicodeError):
        return None
    # Discard argv first: a shell command can itself contain HC_SESSION=… .
    if not text.startswith(row["command"] + " "):
        return None
    suffix = text[len(row["command"]):]
    fields = {}
    for key in ("HC_SESSION", "XDG_STATE_HOME"):
        values = re.findall(r"(?:^|\s)" + key + r"=([^\s]*)", suffix)
        if len(values) != 1:
            return None
        fields[key] = values[0]
    return fields


def scratch_owned(row, scratch, read_env=process_environment):
    env = read_env(row)
    if not env or not re.fullmatch(r"tc-[a-zA-Z0-9_.-]+", env.get("HC_SESSION", "")):
        return False
    state = pathlib.Path(env.get("XDG_STATE_HOME", ""))
    root = pathlib.Path(scratch).resolve()
    return state.is_absolute() and state.resolve().is_relative_to(root) and state.resolve() != root


def socket_holders():
    socket = pathlib.Path.home() / ".local/state/herdr-core/default/default.sock"
    result = subprocess.run(["/usr/sbin/lsof", "-t", "--", str(socket)], env=PS_ENV, capture_output=True)
    if result.returncode not in (0, 1):
        raise RuntimeError("cannot identify protected real socket holders")
    return {int(p) for p in result.stdout.split()}


def protected_snapshot(rows, scratch):
    holders = socket_holders()
    home = home_snapshot(rows)
    protected = {str(pid): row for pid, row in rows.items() if pid in holders or core_candidate(row)}
    for pid, row in rows.items():
        command = row["command"]
        if "Herdr.app/Contents/MacOS/" in command or (
                pathlib.Path(command.split(" ", 1)[0]).name == "herdr-tailcat" and re.search(r" (via|daemon) ", command)):
            if str(pid) not in home:
                protected[str(pid)] = row
    return protected


def home_command(command):
    try:
        argv = shlex.split(command)
    except ValueError:
        return False
    return (len(argv) >= 3 and pathlib.Path(argv[0]).name == "herdr-tailcat"
            and argv[1] in ("via", "daemon") and argv[2] == "home")


def home_snapshot(rows):
    return {str(pid): row for pid, row in rows.items() if home_command(row["command"])}


def real_home_machines():
    # Read only the existing real socket; never use the caller's scratch context.
    env = dict(os.environ)
    for key in ("HC_TERMINAL_ID", "XDG_STATE_HOME", "XDG_CONFIG_HOME", "XDG_RUNTIME_DIR"):
        env.pop(key, None)
    env.update(HC_SESSION="default", HC_SOCKET=str(pathlib.Path.home() / ".local/state/herdr-core/default/default.sock"))
    result = subprocess.run(["hc", "--json", "machine", "ls"], env=env, capture_output=True, timeout=5, check=True)
    values = json.loads(result.stdout)["machines"]
    return [{"name": m["name"], "state": m["state"]} for m in values
            if m.get("via") and home_command(m.get("target", ""))]


class HomeRecovery:
    """Home may redial, but never becomes eligible for scratch signals."""
    def __init__(self, rows, sample, fixed_check, record, machines=real_home_machines,
                 holders=socket_holders, clock=time.monotonic, pause=time.sleep):
        self.expected = home_snapshot(rows)
        self.sample, self.fixed_check, self.record = sample, fixed_check, record
        self.machines, self.holders, self.clock, self.pause = machines, holders, clock, pause

    def check(self):
        current = home_snapshot(self.sample())
        if current == self.expected:
            return current
        start = self.clock()
        while True:
            self.fixed_check()
            current = home_snapshot(self.sample())
            holders = self.holders()
            vias = [r for r in current.values() if shlex.split(r["command"])[1] == "via"]
            daemons = [r for r in current.values() if shlex.split(r["command"])[1] == "daemon"]
            try:
                states = self.machines()
            except (OSError, ValueError, KeyError, subprocess.SubprocessError):
                states = []
            if vias and len(daemons) == 1 and all(r["ppid"] in holders for r in vias) and states and all(m["state"] == "connected" for m in states):
                self.record(dict(before=self.expected, after=current, machines=states, seconds=self.clock()-start))
                self.expected = current
                return current
            if self.clock()-start >= 60:
                raise RuntimeError("home did not recover with a real-core-owned via within 60s")
            self.pause(.5)


def identity(row):
    return {key: row[key] for key in ("pid", "start", "command")}


def retained_snapshot(expected, rows):
    """New unknown processes stay protected, without changing the start baseline."""
    return {key: rows[int(key)] for key in expected if int(key) in rows}


def file_holders(path):
    result = subprocess.run(["/usr/sbin/lsof", "-t", "--", str(path)], env=PS_ENV, capture_output=True)
    if result.returncode not in (0, 1):
        return set()
    return {int(pid) for pid in result.stdout.split()}


class Ownership:
    def __init__(self, scratch, hc, sample, protected=()):
        self.scratch = pathlib.Path(scratch).resolve()
        self.hc = str(hc)
        self.sample = sample
        self.protected = {int(pid) for pid in protected}
        self.contexts = {}
        self.children = {}
        self.proven = {}

    def register(self, env):
        assert env["HC_SESSION"].startswith("tc-")
        for key in ("XDG_STATE_HOME", "XDG_CONFIG_HOME", "XDG_RUNTIME_DIR", "HERDR_TAILCAT_HOME"):
            assert pathlib.Path(env[key]).resolve().is_relative_to(self.scratch)
        assert not env.get("HC_SOCKET") and not env.get("HC_TERMINAL_ID")
        self.contexts[env["XDG_STATE_HOME"]] = dict(env)

    def record(self, child):
        row = self.sample().get(child.pid)
        if row is not None and child.poll() is None:
            self.children[child.pid] = (child, identity(row))

    def read_status(self, env):
        try:
            result = subprocess.run([self.hc, "--json", "server", "status"], env=env,
                                    cwd=self.scratch, capture_output=True, timeout=5)
            return json.loads(result.stdout) if result.returncode == 0 else None
        except (OSError, subprocess.TimeoutExpired, ValueError):
            return None

    def core_proof(self, row):
        if row["pid"] in self.protected:
            return None
        for state, base in self.contexts.items():
            for lock in (pathlib.Path(state) / "herdr-core").glob("tc-*/core.lock"):
                if lock.is_symlink() or not lock.resolve().is_relative_to(self.scratch):
                    continue
                try:
                    socket = pathlib.Path(lock.read_text().strip())
                    if not socket.is_absolute() or not socket.resolve().is_relative_to(self.scratch) or not socket.is_socket():
                        continue
                except (OSError, ValueError):
                    continue
                if row["pid"] not in file_holders(socket):
                    continue
                env = dict(base, HC_SESSION=lock.parent.name)
                status = self.read_status(env)
                current = self.sample().get(row["pid"])
                if status and status.get("pid") == row["pid"] and current and identity(current) == identity(row):
                    proof = dict(kind="core_socket", pid=row["pid"], session=env["HC_SESSION"],
                                 socket=str(socket.resolve()), lock=str(lock), identity=identity(row))
                    self.proven[row["pid"]] = proof
                    return proof
        return None

    def prove(self, row, rows=None, seen=None, fresh=False):
        if row["pid"] in self.protected:
            return None
        try:
            argv = shlex.split(row["command"])
        except ValueError:
            return None
        is_server = core_candidate(row) and ("--hc-launchd" in argv or (len(argv) > 1 and argv[1] == "serve"))
        if is_server:
            cached = self.proven.get(row["pid"])
            if not fresh and cached and cached["identity"] == identity(row):
                return cached
            return self.core_proof(row)
        rows = self.sample() if rows is None else rows
        seen = set() if seen is None else set(seen)
        if row["pid"] in seen:
            return None
        seen.add(row["pid"])
        child = self.children.get(row["pid"])
        kind = None
        if child and child[0].poll() is None and child[1] == identity(row):
            kind = "popen_identity"
        else:
            parent = rows.get(row["ppid"])
            if parent and self.prove(parent, rows, seen):
                kind = "proven_parent"
            elif scratch_owned(row, self.scratch):
                kind = "scratch_environment"
            else:
                for pattern in ("**/*.sock", "**/*.lock"):
                    for path in self.scratch.glob(pattern):
                        if path.is_symlink() or not path.resolve().is_relative_to(self.scratch):
                            continue
                        if row["pid"] in file_holders(path):
                            kind = "scratch_socket_or_lock"
                            break
                    if kind:
                        break
        if kind:
            current = self.sample().get(row["pid"]) if fresh else rows.get(row["pid"])
            if current and identity(current) == identity(row):
                return dict(kind=kind, pid=row["pid"], identity=identity(row))
        return None


def launchd_snapshot():
    result = subprocess.run(["/bin/launchctl", "list"], capture_output=True, env=PS_ENV, check=True)
    jobs = {}
    for line in result.stdout.decode().splitlines():
        fields = line.split()
        if len(fields) == 3 and fields[2].startswith("dev.herdr.core."):
            jobs[fields[2]] = fields[0]
    return dict(jobs=jobs, directories=sorted(str(p.resolve()) for p in pathlib.Path("/tmp").glob("hc-launchd-*")))


class LaunchdAudit:
    def __init__(self, scratch, ownership):
        self.ownership = ownership
        self.scratch = pathlib.Path(scratch).resolve()
        self.before = launchd_snapshot()
        self.owned_directories = set()
        self.owned_jobs = set()

    def observe(self, rows):
        proven_pids = set()
        for row in rows.values():
            if "--hc-launchd" not in row["command"] or not self.ownership.prove(row, rows):
                continue
            proven_pids.add(str(row["pid"]))
            argv = shlex.split(row["command"])
            directory = pathlib.Path(argv[argv.index("--hc-launchd") + 1]).parent.resolve()
            if directory.parent == pathlib.Path("/tmp").resolve() and directory.name.startswith("hc-launchd-"):
                self.owned_directories.add(str(directory))
        if proven_pids:
            self.owned_jobs.update(label for label, pid in launchd_snapshot()["jobs"].items() if pid in proven_pids)

    def finish(self):
        after = launchd_snapshot()
        own_jobs = list(set(after["jobs"]) & self.owned_jobs)
        for label in set(after["jobs"]) - set(self.before["jobs"]):
            result = subprocess.run(["/bin/launchctl", "print", "gui/%d/%s" % (os.getuid(), label)], env=PS_ENV, capture_output=True)
            # Read launchd environment only in memory; output only owned label.
            if str(self.scratch).encode() in result.stdout and label not in own_jobs:
                own_jobs.append(label)
        remaining_dirs = sorted(set(after["directories"]) & self.owned_directories)
        result = dict(before=self.before, after=after, observed_owned_directories=sorted(self.owned_directories), remaining_owned_jobs=own_jobs, remaining_owned_directories=remaining_dirs)
        return result
