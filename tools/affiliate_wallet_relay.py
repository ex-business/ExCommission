# coding: utf-8
"""隔离验收的故障代理：不改业务或钱包数据，仅模拟响应丢失、查询故障及回执恢复。"""
import http.server,threading,time,urllib.request,urllib.error
class WalletRelay:
 def __init__(self,address,wallet,api):
  self.wallet=wallet;self.api=api;self.query_enabled=False;self.callback_enabled=False;self.posts=0;self.queries=0;self.callbacks=[];self.callback_success=0;self.committed=False
  owner=self
  class Handler(http.server.BaseHTTPRequestHandler):
   def log_message(self,*args):pass
   def do_GET(self):self.proxy()
   def do_POST(self):self.proxy()
   def proxy(self):
    raw=self.rfile.read(int(self.headers.get('Content-Length','0')));callback=self.path.startswith('/v1/payment-callbacks/affiliate-wallet/')
    if callback:
     owner.callbacks.append((raw,self.headers.get('X-Payment-Signature','')))
     if not owner.callback_enabled:self.send_error(503);return
    if not callback and self.command=='GET' and self.path.startswith('/v1/wallet/credits/'):
     owner.queries+=1
     if not owner.query_enabled:self.send_error(503);return
    posting=not callback and self.command=='POST'
    if posting:owner.posts+=1
    headers={k:v for k,v in self.headers.items() if k.lower() not in ['host','connection','content-length']}
    request=urllib.request.Request((owner.api if callback else owner.wallet)+self.path,data=raw if self.command=='POST' else None,headers=headers,method=self.command)
    try:response=urllib.request.build_opener(urllib.request.ProxyHandler({})).open(request,timeout=12)
    except urllib.error.HTTPError as e:response=e
    except OSError:self.send_error(503);return
    body=response.read()
    if posting and owner.posts==1:
     # 独立钱包已提交，调用方3秒超时前不返回；不能靠直接改账本制造“已到账”。
     owner.committed=True;time.sleep(4)
    if callback and response.status==204:owner.callback_success+=1
    try:self.send_response(response.status);self.send_header('Content-Type',response.headers.get('Content-Type','application/json'));self.end_headers();self.wfile.write(body)
    except (BrokenPipeError,ConnectionResetError):pass
  self.server=http.server.ThreadingHTTPServer(address,Handler);self.thread=threading.Thread(target=self.server.serve_forever,daemon=True);self.thread.start()
 def close(self):self.server.shutdown();self.server.server_close();self.thread.join()
