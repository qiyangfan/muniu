GO ?= go
BIN := bin
PKG := github.com/solvela/muniu

## check: 全量门禁(vet、lint、死代码、许可证、密钥扫描、基础设施 lint(含文件卫生)、依赖整洁、web 检查、race test/覆盖率、插件)
.PHONY: check
check: vet lint deadcode licenses secrets doclink infra-lint tidy-check web-check test plugin-check

## web-check: 控制台类型检查/lint/组件测试/契约类型漂移(pnpm 存在时执行,否则跳过)
.PHONY: web-check
web-check:
	@command -v pnpm >/dev/null 2>&1 || { echo "[web-check] pnpm 未安装,跳过控制台门禁"; exit 0; }
	cd web && pnpm install --frozen-lockfile >/dev/null && pnpm typecheck && pnpm lint && pnpm lint:css && pnpm knip && pnpm test && pnpm format:check && node ../scripts/weblicensecheck.mjs && pnpm generate:api && git diff --exit-code -- src/lib/api-types.d.ts && pnpm exec vite build --outDir /tmp/dist-check --emptyOutDir

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

## infra-lint: workflow/容器/shell/部署清单/发布配置/文件卫生静态检查(工具缺失时跳过并提示,CI 全量执行)
.PHONY: infra-lint
infra-lint:
	@if command -v actionlint >/dev/null 2>&1; then actionlint; else echo "[infra-lint] actionlint 未安装,跳过"; fi
	@if command -v shellcheck >/dev/null 2>&1; then git ls-files '*.sh' | while IFS= read -r f; do shellcheck "$$f" || exit 1; done; else echo "[infra-lint] shellcheck 未安装,跳过"; fi
	@if command -v hadolint >/dev/null 2>&1; then hadolint Containerfile; else echo "[infra-lint] hadolint 未安装,跳过"; fi
	@if command -v kubeconform >/dev/null 2>&1; then kubeconform -strict -summary deploy/cluster/k8s.yaml; else echo "[infra-lint] kubeconform 未安装,跳过"; fi
	@if command -v goreleaser >/dev/null 2>&1; then goreleaser check; else echo "[infra-lint] goreleaser 未安装,跳过"; fi
	@if command -v pre-commit >/dev/null 2>&1; then SKIP=gitleaks pre-commit run --all-files; else echo "[infra-lint] pre-commit 未安装,跳过文件卫生检查"; fi

## doclink: 文档相对链接与标题锚点检查(零网络依赖,287 篇 Markdown 全量)
.PHONY: doclink
doclink:
	@python3 scripts/linkcheck.py

## tidy-check: go.mod/go.sum 与源码一致(防脏依赖文件过闸)
.PHONY: tidy-check
tidy-check:
	$(GO) mod tidy -diff

## deadcode: 跨包不可达函数(deadcode 未安装时跳过并提示,CI 全量执行)
.PHONY: deadcode
deadcode:
	@if command -v deadcode >/dev/null 2>&1; then ./scripts/deadcodecheck.sh; else echo "[deadcode] deadcode 未安装,跳过"; fi

## licenses: 依赖许可证白名单审计(白名单在 scripts/licensecheck.sh;go-licenses 未安装时跳过并提示,CI 全量执行)
.PHONY: licenses
licenses:
	@if command -v go-licenses >/dev/null 2>&1; then ./scripts/licensecheck.sh; else echo "[licenses] go-licenses 未安装,跳过"; fi

## secrets: gitleaks 全历史密钥扫描与 security.yml 同源(策略与 allowlist 在 .gitleaks.toml;gitleaks 未安装时跳过并提示)
.PHONY: secrets
secrets:
	@if command -v gitleaks >/dev/null 2>&1; then gitleaks detect --source . --redact --no-banner --exit-code 1; else echo "[secrets] gitleaks 未安装,跳过(CI security.yml 全量执行)"; fi

## hooks: 安装提交期钩子(凭据/文件卫生走 pre-commit 阶段,提交信息走 commit-msg 阶段)
.PHONY: hooks
hooks:
	@if command -v pre-commit >/dev/null 2>&1; then pre-commit install && pre-commit install --hook-type commit-msg; else echo "[hooks] pre-commit 未安装,见 .pre-commit-config.yaml"; fi

## test: race 全量测试 + 覆盖率门禁(聚合 65% + 逐包下限,见 scripts/covercheck.sh)
.PHONY: test
test:
	$(GO) test -race -shuffle=on -covermode=atomic -coverprofile=coverage.out ./...
	GO=$(GO) ./scripts/covercheck.sh coverage.out

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

## vuln: 可达漏洞扫描(govulncheck;需漏洞库网络.未安装时跳过并提示,CI vuln 作业全量执行)
.PHONY: vuln
vuln:
	@if command -v govulncheck >/dev/null 2>&1; then govulncheck ./...; else echo "[vuln] govulncheck 未安装,跳过(安装: go install golang.org/x/vuln/cmd/govulncheck@latest)"; fi

## audit: 控制台生产依赖高危审计(pnpm audit;需 registry 网络.本地可选,CI web-check 全量执行)
.PHONY: audit
audit:
	@if command -v pnpm >/dev/null 2>&1; then cd web && pnpm audit --prod --audit-level high; else echo "[audit] pnpm 未安装,跳过"; fi

## fuzz: 协议解析 fuzz(手动档;FUZZ 指定目标,FUZZTIME 指定时长)
FUZZ ?= FuzzParseChatRequest
FUZZTIME ?= 30s
.PHONY: fuzz
fuzz:
	$(GO) test ./internal/protocol/ -run='^$$' -fuzz='^$(FUZZ)$$' -fuzztime=$(FUZZTIME)

## plugin-check: 插件侧门禁(fmt/clippy/test/未用依赖/依赖策略/覆盖率;工具缺失时跳过并提示,CI 全量执行)
## 覆盖率下限 68 取 2026-11-19 实测 68.59% 向下取整(含 sdk 的 wasm ABI 垫片——宿主测试打不到,计 0%)
.PHONY: plugin-check
plugin-check:
	@command -v cargo >/dev/null 2>&1 || { echo "[plugin-check] cargo 未安装,跳过"; exit 0; }
	cd plugins && cargo fmt --all --check && cargo clippy --workspace --all-targets -- -D warnings && cargo test
	@if rustup target list --installed 2>/dev/null | grep -q wasm32-unknown-unknown; then \
		cd plugins && cargo clippy --workspace --target wasm32-unknown-unknown -- -D warnings; \
	else \
		echo "[plugin-check] wasm32 target 未安装,跳过 wasm 目标 clippy(安装: rustup target add wasm32-unknown-unknown)"; \
	fi
	@if command -v cargo-machete >/dev/null 2>&1; then cd plugins && cargo machete; else echo "[plugin-check] cargo-machete 未安装,跳过未用依赖检查"; fi
	@if command -v cargo-deny >/dev/null 2>&1; then cd plugins && cargo deny check; else echo "[plugin-check] cargo-deny 未安装,跳过依赖策略检查"; fi
	@if command -v cargo-llvm-cov >/dev/null 2>&1; then cd plugins && cargo llvm-cov --workspace --summary-only --fail-under-lines 68; else echo "[plugin-check] cargo-llvm-cov 未安装,跳过覆盖率门禁"; fi

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
