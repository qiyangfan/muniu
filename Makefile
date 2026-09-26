GO ?= go
BIN := bin
PKG := github.com/solvela/muniu

## check: 全量质量门禁(vet + lint + web typecheck/lint/format + race test/覆盖率)——任何交付前必须全绿
.PHONY: check
check: vet lint web-check test

## web-check: 控制台类型检查与 lint(pnpm 存在时执行,否则跳过)
.PHONY: web-check
web-check:
	@command -v pnpm >/dev/null 2>&1 || { echo "[web-check] pnpm 未安装,跳过控制台门禁"; exit 0; }
	cd web && pnpm install --frozen-lockfile >/dev/null && pnpm typecheck && pnpm lint && pnpm lint:css && pnpm format:check

## web-build: 构建控制台产物到 web/dist(嵌入二进制)
.PHONY: web-build
web-build:
	@command -v pnpm >/dev/null 2>&1 || { echo "[web-build] pnpm 未安装,使用占位 dist"; exit 0; }
	cd web && pnpm install --frozen-lockfile >/dev/null && pnpm build

.PHONY: vet
vet:
	$(GO) vet ./...

## lint: golangci-lint 未安装时跳过并提示(安装见 docs/plans/phase-0-bootstrap.md)
.PHONY: lint
lint:
	@if command -v golangci-lint >/dev/null 2>&1; then golangci-lint run; else echo "[lint] golangci-lint 未安装,跳过"; fi

## test: 竞态检测下的全量测试 + 全局覆盖率门禁(total ≥65%,JaCoCo 同位一票否决;阈值只升不降)
.PHONY: test
test:
	$(GO) test -race -covermode=atomic -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out | awk '/^total:/ {gsub("%",""); total=$$3; if (total+0 < 65) { printf "coverage gate FAIL: %.1f%% < 65%%\n", total; exit 1 } printf "coverage gate PASS: %.1f%%\n", total }'

## build: 纯静态单二进制(CGO_ENABLED=0),版本号由 git tag 注入
.PHONY: build
build:
	CGO_ENABLED=0 $(GO) build -ldflags "-s -w -X $(PKG)/internal/version.Version=$$(git describe --tags --always 2>/dev/null || echo dev)" -o $(BIN)/muniu ./cmd/muniu

## run: 构建并启动(读取工作目录 muniu.yaml,示例见 muniu.example.yaml)
.PHONY: run
run: build
	./$(BIN)/muniu

## podman 后台运行(本机常驻形态;先 cp .env.local.example .env.local 填真值)
## 免拉取:宿主交叉编译 linux/arm64 静态二进制 + scratch 运行层(不依赖 registry 与代理);
## 容器内绑 0.0.0.0,宿主侧经 -p 127.0.0.1:8787:8787 收回环;
## 计量数据放命名卷 muniu-data(machine 本地磁盘——SQLite WAL 在 virtiofs 宿主目录挂载上
## 会 disk I/O error,官方不支持网络 FS 上的 WAL),查询走 make dev-sql;
## 显式清空 machine 注入的 http_proxy/https_proxy(宿主代理不在时出站会被 proxyconnect 拒绝);
## --restart=unless-stopped:容器崩溃自愈(非开机自启——podman machine 未起不拉起)

## dev-up: 构建镜像并后台起容器(已存在则先移除)
.PHONY: dev-up
dev-up: dev-down dev-build
	podman volume exists muniu-data 2>/dev/null || podman volume create muniu-data >/dev/null
	podman run -d --name muniu-gateway --restart=unless-stopped -p 127.0.0.1:8787:8787 \
		--env-file .env.local \
		-e http_proxy= -e https_proxy= -e HTTP_PROXY= -e HTTPS_PROXY= -e no_proxy= \
		-e SSL_CERT_FILE=/etc/ssl/cert.pem \
		-v $(CURDIR)/muniu.yaml:/app/muniu.yaml:ro \
		-v $(CURDIR)/dist/ca-cert.pem:/etc/ssl/cert.pem:ro \
		-v muniu-data:/app/data \
		localhost/muniu:dev
	@sleep 1 && curl -s -m 3 http://127.0.0.1:8787/healthz && echo

## dev-sql: 拷出计量库并查询(make dev-sql 或 make dev-sql SQL='select …')
## 已知边界:podman cp 为运行中热库快照,极端时点可能拷到写入中间态——
## 对账语义有重跑兜底(差异 >1% 立案即复核);需严格一致快照时先 dev-down 再拷
.PHONY: dev-sql
dev-sql:
	@mkdir -p /tmp/muniu-query && podman cp muniu-gateway:/app/data/. /tmp/muniu-query/ >/dev/null
	@if [ -n "$(SQL)" ]; then sqlite3 /tmp/muniu-query/muniu.db "$(SQL)"; else sqlite3 /tmp/muniu-query/muniu.db; fi

## dev-down: 停止并移除常驻容器(不存在则静默)
.PHONY: dev-down
dev-down:
	@podman rm -f muniu-gateway 2>/dev/null || true

## dev-build: 交叉编译 linux/arm64 静态二进制并构建 dev 镜像(版本注入宿主 git 描述)
.PHONY: dev-build
dev-build:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -ldflags "-s -w -X $(PKG)/internal/version.Version=$$(git describe --tags --always 2>/dev/null || echo dev)" -o dist/muniu-linux-arm64 ./cmd/muniu
	cp /etc/ssl/cert.pem dist/ca-cert.pem
	podman build -t localhost/muniu:dev .

## dev-status: 容器状态 + healthz
.PHONY: dev-status
dev-status:
	@podman ps --filter name=muniu-gateway --format '{{.Names}}  {{.Status}}'
	@curl -s -m 3 http://127.0.0.1:8787/healthz && echo || echo "healthz: 无响应"

## dev-restart: 重启常驻容器(重建镜像,改代码后)
.PHONY: dev-restart
dev-restart: dev-up

## bench: 全量基准测试(eval tag:选型评估含 1 万×1536 维构建,-race 下插桩放大 10 倍+,不进常规门禁)
.PHONY: bench
bench:
	$(GO) test -tags=eval -bench=. -benchmem -run=^$$ ./...

## smoke: 真上游冒烟(需真实 key 注入环境变量;CI 中禁跑)
## 用法: make smoke PROVIDER=deepseek
##   MUNIU_SMOKE_KEY=… MUNIU_SMOKE_BASE_URL=https://api.deepseek.com/v1 \
##   MUNIU_SMOKE_PROVIDER=deepseek make smoke PROVIDER=deepseek
.PHONY: smoke
smoke:
	$(GO) test -tags=smoke ./internal/adapter/... -run TestSmoke_$(PROVIDER) -v

## chaos: 故障演练(本地手动档;剧本含时间推进,约 5s)
## 用法: make chaos
.PHONY: chaos
chaos:
	MUNIU_CHAOS=1 $(GO) test -tags=chaos -race ./internal/proxy/ -run TestChaos -v

## plugin-build: 构建全部样例插件(Cargo workspace,Rust → wasm32-unknown-unknown;无 Rust 工具链时跳过并提示)
.PHONY: plugin-build
plugin-build:
	@command -v cargo >/dev/null 2>&1 || { echo "[plugin-build] cargo 未安装,跳过(见 plugins/README.md)"; exit 0; }
	plugins/build.sh

## tidy: 依赖整理(新增依赖后必跑)
.PHONY: tidy
tidy:
	$(GO) mod tidy

## clean: 清理构建产物
.PHONY: clean
clean:
	rm -rf $(BIN)
