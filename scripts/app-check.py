#!/usr/bin/env python3
"""Check actual and fixture mod renders using isolated App config; metadata only on real state."""
import hashlib,json,os,pathlib,shutil,subprocess,tempfile
ROOT=pathlib.Path(__file__).resolve().parents[1]
APP=shutil.which('herdr-app')
def manifest(path):
    result={}
    if path.exists():
        for p in [path,*path.rglob('*')]:
            s=p.lstat();result[str(p.relative_to(path))]=(s.st_mode,s.st_size,s.st_mtime_ns,s.st_ino)
    return result

def main():
    os.umask(0o077)
    root=pathlib.Path(tempfile.mkdtemp(prefix='tc-app-check-',dir='/tmp'))
    print('Scratch evidence:',root,flush=True)
    real=pathlib.Path.home()/'.config/herdr-app'
    state=pathlib.Path.home()/'.local/state/herdr-tailcat'
    before=[manifest(real),manifest(state)]
    base=dict(os.environ);base.pop('HERDR_APP_HOME',None)
    status_before=subprocess.run([APP,'status'],env=base,capture_output=True,timeout=10)
    env=dict(base,HERDR_APP_HOME=str(root/'app'),HC_SESSION='tc-app-check',XDG_STATE_HOME=str(root/'s'),XDG_CONFIG_HOME=str(root/'c'),XDG_RUNTIME_DIR=str(root/'r'),HERDR_TAILCAT_HOME=str(root/'tailcat'))
    for k in ('HC_SOCKET','HC_TERMINAL_ID'):env.pop(k,None)
    checks=[]
    def check(path,label):
        r=subprocess.run([APP,'check',str(path)],env=env,capture_output=True,timeout=35)
        checks.append(dict(label=label,exit=r.returncode,stdout=r.stdout.decode(errors='replace'),stderr=r.stderr.decode(errors='replace')))
    probe=root/'probe';probe.mkdir()
    (probe/'main.js').write_text('import { View, div } from "gpui-kit"; export default class Probe extends View { render(){ return div().child("isolated-probe"); } }')
    (probe/'mod.json').write_text('{"place":"none"}')
    check(probe,'isolation-probe')
    scratch_status=subprocess.run([APP,'status'],env=env,capture_output=True,timeout=10)
    assert scratch_status.returncode!=0,'scratch App status unexpectedly reached a window; review required'
    check(ROOT/'mods/tailcat','actual-initial')
    # Only public demo fixtures; no exec, hc, timers or background tasks in these
    # wrappers. Check eager native controls for pages and states initial check
    # cannot reach. This does not substitute for the real App test in 4b.
    fixture=root/'app/mods/tailcat';shutil.copytree(ROOT/'mods/tailcat',fixture)
    (fixture/'main.js').rename(fixture/'panel.js')
    init='''
this.closed=false; this.part=PART; this.page=PAGE; this.error=ERROR; this.busy="";
this.client={command:"herdr-tailcat"};this.profiles=[{id:"demo",client_fingerprint:"1234abcd",server_fingerprint:"abcd1234",daemon_running:true,machines:[{name:"box",id:"id",host:"demo",version:"v1",session:"work",state:STATE,why:WHY}]}];
this.selected="demo";this.session="work";this.tests={demo:{rtt_ms:123,server:{version:"v1",exports:["work"]}}};
this.keys={demo:{public_key:"nodekey:"+"a".repeat(64)}};this.clients=[{public_key:"nodekey:"+"b".repeat(64),fingerprint:"bbbbbbbb"}];
this.server={initialized:INITIALIZED,running:true,streams:1,allowed_clients:1,exports:["work"],version:"v1",restart_required:true,service:{loaded:true}};
this.pending=PENDING;this.handle=cx.focus_handle();this.revealValue=()=>"DEMONSTRATION ONLY";this.clearReveal=()=>{};
this.idInput=InputState.new();this.nameInput=InputState.new();this.keyInput=InputState.new();this.sessionInput=InputState.new();
'''
    variants=[('pending','', 'connect',True,'connected',''),('connections','', 'connect',True,'connected',''),('disconnected','', 'connect',True,'disconnected','offline'),('connecting','', 'connect',True,'connecting',''),('server-ready','', 'server',True,'connected',''),('server-empty','', 'server',False,'connected',''),('address','address', 'server',True,'connected',''),('bar','bar','connect',False,'connected',''),('error','', 'connect',False,'connected','')]
    for label,part,page,initialized,state_name,why in variants:
        body=init
        replacements=dict(PENDING={"type":"unregister","id":"demo","name":"box"} if label=="pending" else None,PART=part,PAGE=page,ERROR={'message':'Server did not answer','hint':'Check whitelist'} if label=='error' else None,STATE=state_name,WHY=why,INITIALIZED=initialized)
        for name,value in replacements.items():body=body.replace(name,json.dumps(value))
        (fixture/'main.js').write_text('import Tailcat from "./panel.js"; import {InputState} from "gpui-base"; export default class Fixture extends Tailcat { init(p,cx){'+body+'} }')
        check(fixture,label)
    status_after=subprocess.run([APP,'status'],env=base,capture_output=True,timeout=10)
    after=[manifest(real),manifest(state)]
    result=dict(root=str(root),checks=checks,scratch_status_exit=scratch_status.returncode,real_status_unchanged=(status_before.returncode,status_before.stdout,status_before.stderr)==(status_after.returncode,status_after.stdout,status_after.stderr),real_app_manifest_unchanged=before[0]==after[0],real_tailcat_manifest_unchanged=before[1]==after[1],status_before_sha256=hashlib.sha256(status_before.stdout).hexdigest(),status_after_sha256=hashlib.sha256(status_after.stdout).hexdigest())
    (root/'results.json').write_text(json.dumps(result,indent=2)+'\n')
    print(json.dumps(result,indent=2),flush=True)
    assert all(c['exit']==0 for c in checks),'mod check failed'
    assert result['real_status_unchanged'] and result['real_app_manifest_unchanged'] and result['real_tailcat_manifest_unchanged'],'real App/state difference; review required'
if __name__=='__main__':main()
