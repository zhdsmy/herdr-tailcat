#!/usr/bin/env python3
"""Capture current hc's single line argv in scratch; preserve protected processes."""
import importlib.util
import json
import os
import pathlib
import re
import shlex
import shutil
import subprocess
import tempfile
import time
from process_guard import LaunchdAudit, Ownership, protected_snapshot, retained_snapshot
from scratch_exec import copy_executable

spec = importlib.util.spec_from_file_location('faults', pathlib.Path(__file__).with_name('fault-matrix.py'))
faults = importlib.util.module_from_spec(spec); spec.loader.exec_module(faults)

def main():
    os.umask(0o077)
    root = pathlib.Path(__file__).resolve().parents[1]
    scratch = pathlib.Path(tempfile.mkdtemp(prefix='tc-lines-',dir='/tmp')).resolve()
    env = dict(os.environ,HC_SESSION='tc-lines',HERDR_TAILCAT_HOME=str(scratch/'tailcat'),XDG_STATE_HOME=str(scratch/'s'),XDG_CONFIG_HOME=str(scratch/'c'),XDG_RUNTIME_DIR=str(scratch/'r'))
    for name in ('HC_SOCKET','HC_TERMINAL_ID'): env.pop(name,None)
    hc = copy_executable(pathlib.Path(shutil.which('hc')).resolve(),scratch,'hc')
    before = protected_snapshot(faults.processes(),scratch)
    ownership = Ownership(scratch,hc,faults.processes,before)
    ownership.register(env)
    audit = LaunchdAudit(scratch,ownership)
    (scratch/'protected-before.json').write_text(json.dumps(before,indent=2))
    def call(*args):
        faults.stable_snapshot(before,lambda:retained_snapshot(before,faults.processes()),lambda _:None)
        result = subprocess.run([hc,*args],env=env,cwd=scratch,timeout=25,capture_output=True)
        audit.observe(faults.processes())
        return result
    bucket=scratch/'record.py'
    bucket.write_text("#!/usr/bin/env python3\nimport os,sys,json\nwith open(os.environ['TC_CAPTURE'],'a') as f:f.write(json.dumps(sys.argv[1:])+'\\n')\nsys.exit(255)\n")
    bucket.chmod(0o700); env['TC_CAPTURE']=str(scratch/'argv.jsonl')
    try:
        version=call('--version').stdout.decode().strip()
        match=re.search(r'\b0\.1\.(16|17)\b',version); assert match,version
        outdir=root/'testdata/lines'/('v'+match.group(0));outdir.mkdir(parents=True,exist_ok=True)
        for filename,session in [('default.txt',None),('work.txt','work')]:
            args=['machine','add','--via',shlex.quote(str(bucket)),'--name','tc-capture']
            if session:args+=['--session',session]
            result=call(*args); assert result.returncode!=0
            records=[json.loads(x) for x in pathlib.Path(env['TC_CAPTURE']).read_text().splitlines()]
            assert len(records[-1])==1
            content=(records[-1][0]+'\n').encode()
            (outdir/filename).write_bytes(content)
            if content!=(root/'testdata/lines/v0.1.16'/filename).read_bytes():
                raise faults.PlanStop('captured fixture differs from v0.1.16; review required')
        (scratch/'capture.json').write_text(json.dumps(dict(version=version,equal_to_v0_1_16=True)))
        print('Captured real',version,'fixtures; identical to v0.1.16',flush=True)
    finally:
        call('server','stop');time.sleep(2)
        after=retained_snapshot(before,faults.processes())
        (scratch/'protected-after.json').write_text(json.dumps(after,indent=2))
        faults.stable_snapshot(before,lambda:retained_snapshot(before,faults.processes()),lambda _:None)
        (scratch/"ownership-core-proofs.json").write_text(json.dumps(ownership.proven,indent=2))
        launchd=audit.finish();(scratch/'launchd.json').write_text(json.dumps(launchd,indent=2))
        assert not launchd['remaining_owned_jobs'] and not launchd['remaining_owned_directories'], 'scratch launchd leftovers'
        print('Capture evidence:',scratch,flush=True)

if __name__=='__main__':main()
