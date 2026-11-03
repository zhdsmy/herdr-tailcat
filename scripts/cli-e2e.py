#!/usr/bin/env python3
"""Management CLI E2E. Scratch only, DERP, default core timing. Exit 2 means review needed."""
import json
import os
import pathlib
import secrets
import shlex
import shutil
import signal
import subprocess
import sys
import time
import importlib.util
spec=importlib.util.spec_from_file_location("faults",pathlib.Path(__file__).with_name("fault-matrix.py"))
faults=importlib.util.module_from_spec(spec);spec.loader.exec_module(faults)
PlanStop=faults.PlanStop

class CLI(faults.Lab):
    def __init__(self):
        super().__init__(prefix="tc-cli-")
        self.protected_connects=faults.app_connects(self.before)
        self.save("protected-app-connects.json",self.protected_connects)
        self.timings=[]
        self.secret_words=set()
        self.label="dev.herdr.tailcat.test.cli."+secrets.token_hex(6)
        self.plist_dir=self.scratch/"launchd"
        self.plist_dir.mkdir(mode=0o700)
        # A spy forwards to the private core copy, and proves register cannot
        # accidentally use the user's core environment. No env dump is saved.
        bindir=self.scratch/"spy";bindir.mkdir(mode=0o700)
        wrapper=bindir/"hc"
        wrapper.write_text("#!"+sys.executable+"\n"+'''import json,os,pathlib,subprocess,sys
root=os.environ["TC_SCRATCH"]
assert os.environ["HC_SESSION"].startswith("tc-")
for name in ("XDG_STATE_HOME","XDG_CONFIG_HOME","XDG_RUNTIME_DIR","HERDR_TAILCAT_HOME"):
    assert pathlib.Path(os.environ[name]).is_relative_to(root)
assert not os.environ.get("HC_SOCKET") and not os.environ.get("HC_TERMINAL_ID")
with open(root+"/hc-spy.jsonl","a") as f:f.write(json.dumps(sys.argv[1:])+"\\n")
if os.environ.get("TC_FAKE_SSH") and sys.argv[1:]==["--json","machine","ls"]:
    print(json.dumps({"machines":[{"name":"scratch-ssh","via":False,"target":"scratch-ssh-never-dialed","state":"disconnected"}]}));sys.exit(0)
os.execv(os.environ["TC_REAL_HC"],[os.environ["TC_REAL_HC"],*sys.argv[1:]])
''')
        wrapper.chmod(0o700)
        for env in (self.client,self.server):
            env.update(PATH=str(bindir)+":"+str(self.scratch/"bin")+":"+env["PATH"],TC_SCRATCH=str(self.scratch),TC_REAL_HC=self.hc_path)
        self.plist_args=("--label",self.label,"--plist-dir",str(self.plist_dir))

    def check_real_core(self):
        snapshot=super().check_real_core()
        if faults.app_connects(faults.processes()) != self.protected_connects:
            raise PlanStop("protected App connect set changed; no signal sent")
        return snapshot

    def cli(self,env,*args,data=None,kind=None,secret=False,attempt=1):
        assert env["HC_SESSION"].startswith("tc-")
        assert all(pathlib.Path(env[k]).is_relative_to(self.scratch) for k in faults.FIELDS)
        self.check_real_core()
        self.capture_secrets()
        start=time.monotonic()
        try:r=self.run([self.bin,*args,"--json"],env,data=data,secret=secret,check=False,timeout=25)
        except subprocess.TimeoutExpired as e:raise PlanStop("management command cannot finish in <25s") from e
        self.capture_secrets()
        self.check_real_core()
        elapsed=time.monotonic()-start
        self.timings.append(dict(command=list(args),elapsed=elapsed,exit=r.returncode))
        self.save("timings.json",self.timings)
        if elapsed>=25:raise PlanStop("management command cannot finish in <25s")
        assert r.stderr==b"", "JSON stderr is not empty"
        value=json.loads(r.stdout)
        # A short register preflight can legitimately time out on a jittery
        # public relay. Retry only an unanswered preflight, never a core error
        # or a command that exceeded its budget. Every attempt remains logged.
        if r.returncode and value.get("error",{}).get("kind")=="no_answer" and kind!="no_answer" and attempt<3:
            self.event("transient-ping-retry",command=list(args),attempt=attempt,elapsed=elapsed)
            time.sleep(.5)
            return self.cli(env,*args,data=data,kind=kind,secret=secret,attempt=attempt+1)
        if kind:
            assert r.returncode!=0 and value["ok"] is False and value["error"]["kind"]==kind,(args,value)
        elif secret:
            assert r.returncode==0 and set(value)=={"address"}
        else:
            assert r.returncode==0 and value["ok"] is True,(args,value)
        return value

    def capture_secrets(self):
        for p in self.scratch.rglob("*"):
            if p.name=="server.key":
                v=json.loads(p.read_text());values=[v["Private"],v["Public"]["PresharedKey"]]
            elif p.name=="client.key":values=[json.loads(p.read_text())]
            elif p.name=="profile.json":values=[json.loads(p.read_text())["address"]]
            else:continue
            for value in values:
                raw=value.encode();self.secret_words.update([raw,raw[:24]])
                if b":" in raw:
                    body=raw.split(b":",1)[1];self.secret_words.update([body,body[:24]])

    def scan(self):
        self.capture_secrets()
        if hasattr(self,"short"):self.secret_words.update([self.short,self.short[:24]])
        hits=[]
        for p in self.scratch.rglob("*"):
            if p.is_file() and p.name not in ("server.key","client.key","profile.json","herdr-core","hc"):
                if any(w and w in p.read_bytes() for w in self.secret_words):hits.append(str(p.relative_to(self.scratch)))
        source=subprocess.check_output(["git","ls-files","-z","--cached","--others","--exclude-standard"],cwd=faults.ROOT).split(b"\0")
        for name in source:
            if not name:continue
            p=faults.ROOT/os.fsdecode(name)
            if p.is_file() and any(w and w in p.read_bytes() for w in self.secret_words):hits.append("source/"+os.fsdecode(name))
        self.save("secret-scan.json",dict(patterns=len(self.secret_words),hits=hits,source_files=len(source)-1))
        assert not hits,"secret hits in "+repr(hits)
        return len(self.secret_words)

    def spy_calls(self):
        p=self.scratch/"hc-spy.jsonl"
        return p.read_bytes() if p.exists() else b""

    def exercise(self):
        self.case="C1"
        v=self.cli(self.server,"server","status",*self.plist_args)
        assert not v["initialized"] and not v["running"]
        self.cli(self.server,"server","init")
        for session in ("tc-work","tc-other"):
            self.cli(self.server,"server","export","add",session)
        self.cli(self.server,"server","export","add","tc-unused")
        self.cli(self.server,"server","export","rm","tc-unused")
        assert self.cli(self.server,"server","export","ls")["exports"]==["tc-work","tc-other"]
        full=self.cli(self.server,"server","show-address","--full",secret=True)["address"].encode()
        self.short=self.cli(self.server,"server","show-address",secret=True)["address"].encode()
        self.cli(self.client,"profile","add","p1",data=full)
        public=self.cli(self.client,"profile","pubkey","p1")["public_key"]
        self.cli(self.server,"server","allow","add",public)
        assert self.cli(self.server,"server","allow","ls")["clients"][0]["public_key"]==public
        self.start_serve()
        v=self.cli(self.client,"profile","test","p1")
        assert v["server"]["exports"]==["tc-work","tc-other"] and v["rtt_ms"]>0
        for name,session in (("box","tc-work"),("box2","tc-other")):
            v=self.cli(self.client,"profile","register","p1","--name",name,"--session",session,"--temp")
            assert v["machine"]["name"]==name and v["machine"]["session"]==session
            assert "target" not in v["machine"]
        for machine in self.json_hc(self.client,"machine","ls")["machines"]:
            target=shlex.split(machine["target"])
            assert target==[self.bin,"via","p1"], "via target must execute scratch copy"
            faults.assert_safe_executable(target[0])
        self.connected()
        v=self.cli(self.client,"profile","ls")["profiles"][0]
        assert v["daemon_running"] and {m["name"]:m["state"] for m in v["machines"]}=={"box":"connected","box2":"connected"}
        self.address=self.json_hc(self.client,"run","--bg","--on","box","--","sh")["address"]
        self.hc(self.client,"send",self.address,"printf 'CLI_%s\\n' 'REMOTE_OK'\r")
        self.wait_read(b"CLI_REMOTE_OK")
        # State file is periodically updated, not a synchronous control channel.
        deadline=time.monotonic()+7
        while True:
            v=self.cli(self.server,"server","status",*self.plist_args)
            if v["streams"]==2:break
            assert time.monotonic()<deadline,"stream count did not reach two"
            time.sleep(.2)
        assert not v["restart_required"]
        mode=(pathlib.Path(self.server["HERDR_TAILCAT_HOME"])/"server/serve.state.json").stat().st_mode&0o777
        assert mode==0o600
        self.mark("positive",status="PASS",core_timing="default",forced_derp=True,stream_count=2)

        self.case="C3"
        self.cli(self.client,"profile","register","p1","--name","box","--session","tc-work",kind="core:name_taken")
        self.cli(self.client,"profile","register","p1","--name","box3","--session","tc-work",kind="core:already_added")
        before=self.spy_calls()
        self.cli(self.client,"profile","register","p1","--name","box3","--session","tc-unexported",kind="session_not_exported")
        assert before==self.spy_calls(),"unexported session invoked hc"
        self.cli(self.client,"profile","test","missing",kind="profile_not_found")
        fake=dict(self.client,TC_FAKE_SSH="1")
        self.cli(fake,"profile","unregister","p1","--name","scratch-ssh",kind="target_mismatch")
        self.cli(self.client,"profile","rm","p1",kind="profile_in_use")
        self.mark("negatives",status="PASS",fake_ssh="isolated machine-ls fixture; never dialed")

        self.case="C5"
        # A second foreground serve must fail promptly without disturbing the first.
        r=self.run([self.bin,"serve"],self.server,check=False,timeout=3)
        assert r.returncode!=0 and "已有 serve 在运行" in r.stderr.decode()
        assert self.serve.poll() is None
        self.cli(self.server,"server","allow","rm",public)
        assert self.cli(self.server,"server","status",*self.plist_args)["restart_required"]
        self.stop_serve()
        # Empty policy must fail closed; allow an unrelated key to test actual
        # rejection by a running server rather than just a startup validation.
        r=self.run([self.bin,"serve"],self.server,check=False,timeout=3)
        assert r.returncode!=0 and b"at least one" in r.stderr
        self.cli(self.client,"profile","add","p2",data=full)
        foreign=self.cli(self.client,"profile","pubkey","p2")["public_key"]
        self.cli(self.server,"server","allow","add",foreign)
        self.start_serve()
        self.cli(self.client,"profile","test","p1",kind="no_answer")
        self.stop_serve()
        self.cli(self.client,"profile","test","p1",kind="no_answer")
        self.cli(self.server,"server","allow","add",public)
        self.cli(self.server,"server","allow","rm",foreign)
        self.mark("policy_and_lock",status="PASS",old_client_denied=True,offline_no_answer=True)

        self.case="C6"
        generated=self.cli(self.server,"server","install-service",*self.plist_args,"--force-derp")
        path=pathlib.Path(generated["path"])
        assert path.is_relative_to(self.scratch)
        self.assert_service_binary(path)
        self.service_target="gui/%d/%s"%(os.getuid(),self.label)
        self.service_plist=path
        assert not self.cli(self.server,"server","service","status",*self.plist_args)["loaded"]
        assert self.cli(self.server,"server","service","start",*self.plist_args)["loaded"]
        self.connected(timeout=35)
        v=self.cli(self.server,"server","service","status",*self.plist_args)
        assert v["loaded"] and v["pid"]>1
        assert self.cli(self.client,"profile","test","p1")["server"]
        assert not self.cli(self.server,"server","service","stop",*self.plist_args)["loaded"]
        assert not self.cli(self.server,"server","service","status",*self.plist_args)["loaded"]
        self.mark("service",status="PASS",label=self.label,loaded_after_stop=False)

        self.case="C4"
        # Bring the service back so force-removal acts on live registered vias.
        self.cli(self.server,"server","service","start",*self.plist_args)
        self.connected(timeout=35)
        daemon=self.unique(" daemon p1")["pid"]
        self.cli(self.client,"profile","rm","p1","--force")
        self.wait_gone([daemon],3)
        assert self.json_hc(self.client,"machine","ls")["machines"]==[]
        assert not (pathlib.Path(self.client["HERDR_TAILCAT_HOME"])/"profiles/p1").exists()
        self.cli(self.client,"profile","rm","p2")
        self.cli(self.server,"server","service","stop",*self.plist_args)
        assert not self.cli(self.server,"server","service","status",*self.plist_args)["loaded"]
        path.unlink()
        self.mark("force_cleanup",status="PASS",daemon_gone=True,machines_removed=True,profile_removed=True,plist_removed=True)
        self.mark("complete",status="PASS",commands=len(self.timings),max_seconds=max(r["elapsed"] for r in self.timings))

    def stop_serve(self):
        if self.serve and self.serve.poll() is None:
            self.send_signal(self.serve.pid,signal.SIGTERM)
            self.serve.wait(timeout=10)

    def wait_read(self,needle):
        end=time.monotonic()+10
        while time.monotonic()<end:
            if needle in self.hc(self.client,"read",self.address).stdout:return
            time.sleep(.1)
        raise AssertionError("remote read missing marker")


def main():
    lab=CLI();code=0
    try:lab.exercise()
    except PlanStop as e:lab.mark("STOP",reason=str(e));code=2
    except Exception as e:lab.mark("FAILURE",kind=type(e).__name__,reason=str(e));code=1
    finally:
        try:lab.cleanup()
        except Exception as e:lab.mark("CLEANUP_FAILURE",kind=type(e).__name__,reason=str(e));code=1
    print("Scratch evidence:",lab.scratch,flush=True)
    return code
if __name__=="__main__":sys.exit(main())
