import os, signal, subprocess, tempfile, termios, time, select, shutil
for sig in (signal.SIGINT, signal.SIGTERM, "happy", "mismatch"):
 root=tempfile.mkdtemp(prefix='muniu-review-tty-');cfg=root+'/test.yaml'
 open(cfg,'w').write('storage:\n  sqlite:\n    path: '+root+'/test.db\nproviders:\n  - name: test\n    adapter: openai\n    weight: 1\n    base_url: http://127.0.0.1:1/v1\n    keys:\n      - env: REVIEW_FAKE_KEY\nmodel_routes:\n  test:\n    - {provider: test, model: test}\n')
 master,slave=os.openpty();p=None
 try:
  p=subprocess.Popen(['./bin/muniu','-c',cfg,'admins','add','--username','interrupt-check'],stdin=slave,stdout=slave,stderr=slave,env={**os.environ,'REVIEW_FAKE_KEY':'dummy'})
  buf=b'';deadline=time.monotonic()+10
  while '口令: '.encode() not in buf and time.monotonic()<deadline:
   if select.select([master],[],[],.1)[0]:buf+=os.read(master,4096)
  assert '口令: '.encode() in buf,buf.decode(errors='replace')
  assert not termios.tcgetattr(slave)[3]&termios.ECHO
  if isinstance(sig,int):
   p.send_signal(sig)
  else:
   os.write(master,b'review-pass-123\n')
   deadline=time.monotonic()+5
   while '再次输入口令: '.encode() not in buf and time.monotonic()<deadline:
    if select.select([master],[],[],.1)[0]:buf+=os.read(master,4096)
   assert '再次输入口令: '.encode() in buf
   os.write(master,b'review-pass-123\n' if sig=='happy' else b'different-456\n')
  code=p.wait(timeout=5)
  while select.select([master],[],[],.1)[0]:buf+=os.read(master,4096)
  assert b'review-pass-123' not in buf and b'different-456' not in buf
  restored=bool(termios.tcgetattr(slave)[3]&termios.ECHO)
  label=signal.Signals(sig).name if isinstance(sig,int) else sig
  print(label,'退出码:',code,'终端回显恢复:',restored,'无密码回显:',True)
  assert restored and ((code==0) if sig=='happy' else (code!=0))
 finally:
  if p and p.poll() is None:p.kill();p.wait()
  os.close(master);os.close(slave);shutil.rmtree(root)
