#!/usr/bin/env python3
"""D8: import through a real scratch core terminal; never logs imported bytes."""
import importlib.util,json,os,pathlib,sys,time
spec=importlib.util.spec_from_file_location('cli_e2e',pathlib.Path(__file__).with_name('cli-e2e.py'))
cli_e2e=importlib.util.module_from_spec(spec);spec.loader.exec_module(cli_e2e)
faults=cli_e2e.faults

def no_substring(data,secret):
    if any(secret[i:i+16] in data for i in range(len(secret)-15)):
        raise faults.PlanStop('D8: imported address leaked to terminal output/snapshot; review required immediately')

def exercise(lab):
    lab.case='D8'
    lab.cli(lab.server,'server','init')
    full=lab.cli(lab.server,'server','show-address','--full',secret=True)['address'].encode()
    lab.secret_words.update(full[i:i+16] for i in range(len(full)-15))
    result=lab.json_hc(lab.client,'run','--bg','--no-parent','--name','tailcat-add-imported','--',lab.bin,'profile','add','imported','--prompt')
    address=result['address']
    lab.viewer=faults.Viewer(lab,address)
    lab.viewer.wait(b'input is hidden')
    os.write(lab.viewer.master,b'\x1b[200~'+full+b'\x1b[201~\r')
    lab.viewer.wait(b'Press Return')
    profile=pathlib.Path(lab.client['HERDR_TAILCAT_HOME'])/'profiles/imported/profile.json'
    assert profile.exists(),'profile not created'
    assert json.loads(profile.read_text())['address']==full.decode()
    read=lab.run([lab.hc_path,'read',address],lab.client,secret=True).stdout
    no_substring(read,full)
    no_substring(bytes(lab.viewer.data),full)
    # Core v0.1.16 writes terminal snapshots on finish/handover, not on a
    # periodic timer (runtime.rs:699). Finish normally before requiring a snap.
    os.write(lab.viewer.master,b'\r')
    lab.viewer.child.wait(timeout=5)
    lab.viewer.close();lab.viewer=None
    end=time.monotonic()+10
    snaps=[]
    while time.monotonic()<end:
        snaps=list(pathlib.Path(lab.client['XDG_STATE_HOME']).rglob('screens/*.snap'))
        if snaps:break
        time.sleep(.2)
    assert snaps,'core did not persist a final screen snapshot'
    for p in snaps:no_substring(p.read_bytes(),full)
    attach=(lab.scratch/'viewer.bin').read_bytes()
    no_substring(attach,full)
    for p in pathlib.Path(lab.client['XDG_STATE_HOME']).rglob('screens/*.snap'):no_substring(p.read_bytes(),full)
    (lab.scratch/'D8-read.txt').write_bytes(read)
    lab.mark('D8',status='PASS',profile_created=True,attach_bytes=len(attach),read_bytes=len(read),snapshots=len(snaps),substring_length=16,import_method='bracketed paste via hc attach PTY')
    # Continue with the same profile imported by the real terminal. This is
    # the panel's test/register/list/unregister/delete sequence, not a second
    # import channel. Only App's remote-space command is deferred to 4b.
    lab.case='D5'
    lab.cli(lab.server,'server','export','add','tc-work')
    public=lab.cli(lab.client,'profile','pubkey','imported')['public_key']
    lab.cli(lab.server,'server','allow','add',public)
    assert lab.cli(lab.server,'server','allow','ls')['clients'][0]['public_key']==public
    lab.start_serve()
    assert lab.cli(lab.client,'profile','test','imported')['server']['exports']==['tc-work'], 'unexpected scratch export list'
    lab.cli(lab.client,'profile','register','imported','--name','box','--session','tc-work','--temp')
    lab.connected(names=('box',))
    profiles=lab.cli(lab.client,'profile','ls')['profiles']
    assert len(profiles)==1 and profiles[0]['machines'][0]['name']=='box'
    vias=[r['pid'] for r in lab.matching(' via imported ')]
    assert vias,'registration did not start via'
    lab.cli(lab.client,'profile','unregister','imported','--name','box')
    lab.wait_gone(vias,2)
    assert lab.cli(lab.client,'profile','ls')['profiles'][0]['machines']==[]
    lab.cli(lab.client,'profile','rm','imported')
    lab.stop_serve()
    lab.mark('D5',status='PASS',steps='prompt -> pubkey/allow -> test -> register -> list -> unregister -> rm',max_seconds=max(t['elapsed'] for t in lab.timings),calls=len(lab.timings))

def main():
    lab=cli_e2e.CLI();code=0
    try:exercise(lab)
    except faults.PlanStop as e:lab.mark('STOP',reason=str(e));code=2
    except Exception as e:lab.mark('FAILURE',kind=type(e).__name__,reason=str(e));code=1
    finally:
        try:lab.cleanup()
        except Exception as e:lab.mark('CLEANUP_FAILURE',kind=type(e).__name__,reason=str(e));code=1
    print('Scratch evidence:',lab.scratch,flush=True)
    return code
if __name__=='__main__':sys.exit(main())
