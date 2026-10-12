# Tasks: responses-state-delete-proxy

- [x] 1.1 接口扩展 PrepareNativeFetch(verb,...) 与 openai 实现;GET 调用点同步
- [x] 1.2 HandlerResponsesStateless 按 method 泛化;DELETE 路由+openapi+routeEntry 同步
- [x] 1.3 e2e:亲和已知 DELETE 代理转发(上游存储真被删)/ 亲和未知 404 / GET 零回归(TestStickyE2E_DELETE代理)
- [x] 1.4 make check 全绿;archive + 主 specs 同步
