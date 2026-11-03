import importlib.util
import pathlib
import tempfile
import unittest
from unittest.mock import patch

spec=importlib.util.spec_from_file_location("faults",pathlib.Path(__file__).with_name("fault-matrix.py"))
faults=importlib.util.module_from_spec(spec);spec.loader.exec_module(faults)

class FaultHarnessTests(unittest.TestCase):
    def test_scratch_executable_is_copy_and_not_source_symlink(self):
        with tempfile.TemporaryDirectory(prefix="tc-executable-") as directory:
            root=pathlib.Path(directory)
            source=root/"source";source.write_bytes(b"fixture executable")
            binary=pathlib.Path(faults.copy_executable(source,root,"herdr-tailcat"))
            self.assertEqual(binary.read_bytes(),source.read_bytes())
            self.assertFalse(binary.is_symlink())
            self.assertNotEqual(binary.stat().st_ino,source.stat().st_ino)
            self.assertEqual(binary.stat().st_mode&0o777,0o700)
            self.assertEqual(faults.assert_safe_executable(binary),binary)
            with self.assertRaises(AssertionError):faults.copy_executable(source,root,"herdr-tailcat")

    def test_scratch_executable_rejects_protected_paths_and_symlinks(self):
        with tempfile.TemporaryDirectory(prefix="tc-executable-") as directory:
            home=pathlib.Path(directory)
            for name in ("Documents","Desktop","Downloads"):
                protected=home/name;protected.mkdir()
                binary=protected/"fixture";binary.touch()
                alias=home/(name+"-link");alias.symlink_to(binary)
                for path in (binary,alias):
                    with self.assertRaises(AssertionError):faults.assert_safe_executable(path,home)
                sibling=home/(name+"-backup");sibling.mkdir()
                safe=sibling/"fixture";safe.touch()
                self.assertEqual(faults.assert_safe_executable(safe,home),safe.resolve())

    def test_scratch_executable_refuses_destination_symlink(self):
        with tempfile.TemporaryDirectory(prefix="tc-executable-") as directory:
            root=pathlib.Path(directory)
            source=root/"source";source.write_bytes(b"unchanged")
            (root/"bin").mkdir()
            (root/"bin/herdr-tailcat").symlink_to(source)
            with self.assertRaises(AssertionError):faults.copy_executable(source,root,"herdr-tailcat")
            self.assertEqual(source.read_bytes(),b"unchanged")

    def test_app_connects_dynamic_set(self):
        parent=dict(pid=721,ppid=1,start="s",command="/Applications/Herdr.app/Contents/MacOS/herdr-app")
        connect=dict(pid=815,ppid=721,start="c",command="/a/herdr-core connect")
        other=dict(pid=818,ppid=1,start="o",command="/a/herdr-core connect")
        rows={721:parent,815:connect,818:other}
        self.assertEqual(faults.app_connects(rows),{"815":dict(connect=connect,parent=parent)})
        self.assertEqual(faults.app_connects({818:other}),{})
        changed=dict(connect,start="new")
        self.assertNotEqual(faults.app_connects(rows),faults.app_connects({721:parent,815:changed}))

    def test_ps_uses_c_locale(self):
        with patch.object(faults.subprocess,"check_output",return_value=b"") as call:
            faults.processes()
            self.assertEqual(call.call_args.kwargs["env"]["LC_ALL"],"C")

    def test_snapshot_transient_records_before_retry(self):
        events=[];values=iter([{"extra":1},{}])
        result=faults.stable_snapshot({},lambda:next(values),lambda d:events.append(("diff",d)),lambda t:events.append(("wait",t)))
        self.assertEqual(result,{})
        self.assertEqual([e[0] for e in events],["diff","wait"])
        self.assertEqual(events[1][1],.5)

    def test_snapshot_persistent_stops_without_signal(self):
        with patch.object(faults.os,"kill") as kill:
            diffs=[]
            with self.assertRaises(faults.PlanStop):
                faults.stable_snapshot({},lambda:{"extra":1},diffs.append,lambda _:None)
            self.assertEqual(len(diffs),2);kill.assert_not_called()

    def test_s1_viewer_warning_requires_read(self):
        class Viewer:
            data=b"x"*5000
            def wait(self,*_):raise AssertionError("missing")
        class Lab:
            def event(self,*_,**kw):self.warning=kw
        with tempfile.TemporaryDirectory(prefix="tc-harness-") as directory:
            lab=Lab();lab.scratch=pathlib.Path(directory)
            faults.verify_marker(Viewer(),b"marker",0,lambda:b"marker",lab)
            self.assertEqual((lab.scratch/"S1-viewer-tail.bin").stat().st_size,4096)
            self.assertIn("warning",vars(lab))
            with self.assertRaises(AssertionError):
                faults.verify_marker(Viewer(),b"marker",0,lambda:b"",lab)


class ProcessOwnershipTests(unittest.TestCase):
    def row(self, command='/usr/local/bin/hc --hc-launchd /tmp/hc-launchd-demo/ready', pid=42, ppid=1):
        return dict(pid=pid,ppid=ppid,start='fixture-start',command=command)

    def test_core_candidates_include_launchd_and_both_executable_names(self):
        for command in ['/a/hc connect','/a/herdr-core serve','/a/renamed --hc-launchd /tmp/hc-launchd-x/ready']:
            self.assertTrue(faults.core_candidate(self.row(command)))
        self.assertFalse(faults.core_candidate(self.row('/a/herdr-core-helper')))

    def test_core_requires_status_socket_holder_and_stable_identity(self):
        from process_guard import Ownership
        with tempfile.TemporaryDirectory(prefix='tc-proof-') as directory:
            root=pathlib.Path(directory).resolve();lock=root/'s/herdr-core/tc-proof/core.lock';lock.parent.mkdir(parents=True)
            socket=root/'r/herdr-core/tc-proof.sock';lock.write_text(str(socket))
            env=dict(HC_SESSION='tc-proof',XDG_STATE_HOME=str(root/'s'),XDG_CONFIG_HOME=str(root/'c'),XDG_RUNTIME_DIR=str(root/'r'),HERDR_TAILCAT_HOME=str(root/'tailcat'))
            row=self.row();rows={42:row};proof=Ownership(root,'/scratch/hc',lambda:rows);proof.register(env)
            with patch('pathlib.Path.is_socket',return_value=True),patch('process_guard.file_holders',return_value={42}),patch.object(proof,'read_status',return_value={'pid':42}) as status:
                self.assertEqual(proof.prove(row,fresh=True)['kind'],'core_socket')
                self.assertEqual(status.call_args.args[0]['HC_SESSION'],'tc-proof')
                for bad in ({'pid':43},None):
                    with patch.object(proof,'read_status',return_value=bad):self.assertIsNone(proof.prove(row,fresh=True))
                with patch('process_guard.file_holders',return_value={43}):self.assertIsNone(proof.prove(row,fresh=True))
                with patch.object(proof,'sample',return_value={42:dict(row,start='reused')}):self.assertIsNone(proof.prove(row,fresh=True))
                with patch.object(proof,'sample',return_value={42:dict(row,command='/different/hc --hc-launchd other')}):self.assertIsNone(proof.prove(row,fresh=True))
                lock.write_text('/real/default.sock');self.assertIsNone(proof.prove(row,fresh=True));lock.write_text(str(socket))
                proof.protected.add(42);self.assertIsNone(proof.prove(row,fresh=True))

    def test_status_and_lsof_fixture_parsing(self):
        import subprocess
        from process_guard import Ownership,file_holders
        proof=Ownership('/tmp/tc-proof','/tmp/tc-proof/bin/hc',lambda:{})
        env={'HC_SESSION':'tc-proof'}
        with patch('process_guard.subprocess.run',return_value=subprocess.CompletedProcess([],0,b'{"pid":42}')) as call:
            self.assertEqual(proof.read_status(env),{'pid':42});self.assertEqual(call.call_args.kwargs['env'],env)
            self.assertEqual(call.call_args.args[0],['/tmp/tc-proof/bin/hc','--json','server','status'])
        with patch('process_guard.subprocess.run',return_value=subprocess.CompletedProcess([],0,b'42\n43\n')):
            self.assertEqual(file_holders('/tmp/tc-proof/sock'),{42,43})
        with patch('process_guard.subprocess.run',return_value=subprocess.CompletedProcess([],1,b'')):
            self.assertEqual(file_holders('/tmp/tc-proof/sock'),set())

    def test_environment_discards_argv_assignments_and_never_dumps_other_fields(self):
        from process_guard import process_environment
        row=self.row('/bin/sh -c HC_SESSION=tc-forged XDG_STATE_HOME=/tmp/tc-run/s')
        for suffix,expected in [(' HC_SESSION=default XDG_STATE_HOME=/real/s SECRET=not-saved',dict(HC_SESSION='default',XDG_STATE_HOME='/real/s')),(' SECRET=not-saved',None)]:
            with patch('process_guard.subprocess.check_output',return_value=(row['command']+suffix).encode()):
                self.assertEqual(process_environment(row),expected)
        with patch('process_guard.subprocess.check_output',side_effect=OSError):self.assertIsNone(process_environment(row))

    def test_popen_and_proven_parent_identity_but_core_never_bypasses_socket(self):
        from process_guard import Ownership,identity
        class Child:
            pid=43
            def poll(self):return None
        row=self.row('/scratch/herdr-tailcat serve',43);child=self.row('/scratch/herdr-core connect',44,43);rows={43:row,44:child}
        proof=Ownership('/tmp/tc-proof','/tmp/tc-proof/hc',lambda:rows)
        proof.record(Child())
        self.assertEqual(proof.prove(row)['kind'],'popen_identity')
        self.assertEqual(proof.prove(child)['kind'],'proven_parent')
        core=self.row();proof.children[42]=(Child(),identity(core))
        self.assertIsNone(proof.prove(core,fresh=True))
        with patch('process_guard.scratch_owned',return_value=False),patch('process_guard.file_holders',return_value=set()):
            self.assertIsNone(proof.prove(dict(row,start='reused')))

    def test_unknown_new_core_stays_unsignalable_without_stopping_baseline(self):
        from process_guard import retained_snapshot,Ownership
        base={'42':self.row()};rows={42:base['42'],43:self.row(pid=43)}
        self.assertEqual(retained_snapshot(base,rows),base)
        proof=Ownership('/tmp/tc-proof','/tmp/tc-proof/hc',lambda:rows,protected=base)
        self.assertIsNone(proof.prove(rows[43],fresh=True))
        self.assertNotEqual(retained_snapshot(base,{43:rows[43]}),base)

    def test_signal_without_proof_never_calls_kill(self):
        import signal
        from unittest.mock import Mock
        lab=object.__new__(faults.Lab);row=self.row();lab.before={};lab.ownership=Mock();lab.ownership.prove.return_value=None;lab.check_real_core=lambda:None
        for case in ('fixture','cleanup'):
            lab.case=case
            with patch.object(faults,'processes',return_value={42:row}),patch.object(faults.os,'kill') as kill:
                with self.assertRaises(faults.PlanStop):lab.send_signal(42,signal.SIGKILL)
                kill.assert_not_called()

    def test_home_via_daemon_are_separate_from_fixed_app_and_core(self):
        from process_guard import protected_snapshot
        rows={1:self.row('/Applications/Herdr.app/Contents/MacOS/herdr-app',1),2:self.row('/bin/herdr-tailcat via home line',2),3:self.row('/bin/herdr-tailcat daemon home',3),4:self.row('/a/hc --hc-launchd /tmp/a/ready',4)}
        with patch('process_guard.socket_holders',return_value={4}):
            fixed=protected_snapshot(rows,'/tmp/tc-run')
            self.assertEqual(set(fixed),{'1','4'})
            from process_guard import retained_snapshot,home_snapshot
            self.assertEqual(set(home_snapshot(rows)),{'2','3'})
            self.assertEqual(retained_snapshot(fixed,{1:rows[1],4:rows[4]}),fixed)

    def test_home_recovery_requires_real_parent_connected_and_records_new_identity(self):
        from process_guard import HomeRecovery
        from unittest.mock import Mock
        old={2:self.row('/bin/herdr-tailcat via home line',2,4),3:self.row('/bin/herdr-tailcat daemon home',3)}
        new={5:self.row('/bin/herdr-tailcat via home line',5,4),6:self.row('/bin/herdr-tailcat daemon home',6)}
        states=iter([[{'name':'box','state':'connecting'}],[{'name':'box','state':'connected'}]])
        fixed=Mock();record=Mock();pauses=[]
        guard=HomeRecovery(old,lambda:new,fixed,record,machines=lambda:next(states),holders=lambda:{4},pause=pauses.append)
        self.assertEqual(set(guard.check()),{'5','6'})
        self.assertEqual(pauses,[.5]);self.assertEqual(fixed.call_count,2)
        self.assertEqual(set(record.call_args.args[0]['after']),{'5','6'})
        self.assertEqual(guard.check(),guard.expected)
        self.assertEqual(record.call_count,1)

    def test_home_recovery_rejects_wrong_parent_and_preserves_fixed_stop(self):
        from process_guard import HomeRecovery
        from unittest.mock import Mock
        old={2:self.row('/bin/herdr-tailcat via home line',2,4)}
        new={5:self.row('/bin/herdr-tailcat via home line',5,99),6:self.row('/bin/herdr-tailcat daemon home',6)}
        record=Mock();clock=iter([0,0,60])
        guard=HomeRecovery(old,lambda:new,lambda:None,record,machines=lambda:[{'name':'box','state':'connected'}],
                           holders=lambda:{4},clock=lambda:next(clock),pause=lambda _:None)
        with self.assertRaisesRegex(RuntimeError,'within 60s'):guard.check()
        record.assert_not_called()
        guard.fixed_check=Mock(side_effect=faults.PlanStop('fixed changed'))
        guard.clock=lambda:0
        with self.assertRaises(faults.PlanStop):guard.check()

    def test_cleanup_baseline_failure_still_stops_proven_serve(self):
        import signal
        from unittest.mock import Mock
        lab=object.__new__(faults.Lab);lab.scratch=pathlib.Path('/tmp/tc-cleanup-fixture')
        row=self.row(str(lab.scratch)+'/herdr-tailcat serve',43)
        child=Mock(pid=43);child.poll.return_value=None
        child.wait.side_effect=lambda **_:setattr(child.poll,'return_value',0)
        lab.serve=child;lab.frozen_core=lab.frozen_serve=lab.service_target=lab.viewer=None
        lab.client={};lab.before={};lab.real_before={'1':self.row(pid=1)};lab.audit=[]
        lab.ownership=Mock(proven={});lab.ownership.prove.return_value={'kind':'popen_identity'}
        lab.check_real_core=Mock(side_effect=faults.PlanStop('fixed changed'))
        lab.environment=Mock(return_value={});lab.hc=Mock();lab.matching=Mock(return_value=[]);lab.owned=Mock(return_value={})
        lab.event=Mock();lab.save=Mock();lab.mark=Mock();lab.scan=Mock(return_value=0)
        lab.monitor_stop=Mock();lab.monitor=Mock();lab.log=Mock();lab.launchd_audit=Mock()
        lab.launchd_audit.finish.return_value={'remaining_owned_jobs':[],'remaining_owned_directories':[]}
        def sample():return {43:row} if child.poll() is None else {}
        with patch.object(faults,'processes',side_effect=sample),patch.object(faults.os,'kill') as kill,patch.object(faults.time,'sleep'):
            with self.assertRaisesRegex(AssertionError,'snapshots changed'):lab.cleanup()
            kill.assert_called_once_with(43,signal.SIGTERM)
            self.assertTrue(lab.ownership.prove.call_args.kwargs['fresh'])
        self.assertEqual(child.poll(),0)
        self.assertEqual(lab.check_real_core.call_count,2)
        result=lab.mark.call_args.kwargs
        self.assertEqual(result['status'],'FAIL');self.assertEqual(result['remaining'],[])
        self.assertIn('protected baseline before cleanup',result['errors'][0])

    def test_normal_launchd_core_stop_never_sends_signal(self):
        import signal
        from unittest.mock import Mock
        lab=object.__new__(faults.Lab);row=self.row();lab.before={};lab.case='cleanup';lab.ownership=Mock();lab.ownership.prove.return_value={'kind':'core_socket'};lab.check_real_core=lambda:None
        with patch.object(faults,'processes',return_value={42:row}),patch.object(faults.os,'kill') as kill:
            with self.assertRaises(faults.PlanStop):lab.send_signal(42,signal.SIGTERM)
            kill.assert_not_called()

    def test_unparseable_unknown_argv_remains_unproven(self):
        from process_guard import Ownership
        proof=Ownership('/tmp/tc-proof','/tmp/tc-proof/hc',lambda:{})
        self.assertIsNone(proof.prove(self.row("/unknown 'unterminated")))

if __name__=='__main__':unittest.main()
