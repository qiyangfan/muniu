GO ?= go
BIN := bin
PKG := github.com/solvela/muniu

## check: 全量质量门禁(vet + lint + web typecheck/lint + race test)——任何交付前必须全绿
.PHONY: check
check: vet lint web-check test

## web-check: 控制台类型检查与 lint(pnpm 存在时执行,否则跳过)
.PHONY: web-check
web-check:
	@command -v pnpm >/dev/null 2>&1 || { echo "[web-check] pnpm 未安装,跳过控制台门禁"; exit 0; }
	cd web && pnpm install --frozen-lockfile >/dev/null && pnpm typecheck && pnpm lint

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
	@command -v golangci-lint >/dev/null 2>&1 || { echo "[lint] golangci-lint 未安装,跳过"; exit 0; }
	golangci-lint run

## test: 竞态检测下的全量测试
.PHONY: test
test:
	$(GO) test -race ./...

## build: 纯静态单二进制(CGO_ENABLED=0),版本号由 git tag 注入
.PHONY: build
build:
	CGO_ENABLED=0 $(GO) build -ldflags "-s -w -X $(PKG)/internal/version.Version=$$(git describe --tags --always 2>/dev/null || echo dev)" -o $(BIN)/muniu ./cmd/muniu

## run: 构建并启动(读取工作目录 muniu.yaml,示例见 muniu.example.yaml)
.PHONY: run
run: build
	./$(BIN)/muniu

## bench: 全量基准测试
.PHONY: bench
bench:
	$(GO) test -bench=. -benchmem -run=^$$ ./...

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

## tidy: 依赖整理(新增依赖后必跑)
.PHONY: tidy
tidy:
	$(GO) mod tidy

## clean: 清理构建产物
.PHONY: clean
clean:
	rm -rf $(BIN)
