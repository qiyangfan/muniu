// WASM 插件测试 fixture:手写 wat 等价的字节级装配器(无 wat2wasm 工具链依赖,CI 稳定)。
// 覆盖 ABI v0 全部行为分支:pass/modify/reject/回显改写/start 失败/ABI 不匹配/缺导出/
// 死循环/trap/越界写/proc_exit/get_config+log 宿主导入联调。
package testkit

// PluginFixtureKind fixture 行为枚举。
type PluginFixtureKind int

const (
	// FixturePass hook 恒放行。
	FixturePass PluginFixtureKind = iota
	// FixtureModify hook 恒改写,结果为常量 "MASKED"(非 JSON,用于改写产物校验路径)。
	FixtureModify
	// FixtureReject hook 恒拒绝,理由 "pii detected"。
	FixtureReject
	// FixtureModifyEcho hook 恒改写为原请求体(合法 JSON 回显,走通改写全链)。
	FixtureModifyEcho
	// FixtureModifyModel hook 恒改写为常量合法 chat 体(model=alias-b):
	// 验证「插件改写 model 后白名单再校验」路径(key-model-allowlist)。
	FixtureModifyModel
	// FixtureStartFail plugin_start 返回 7(注册拒载)。
	FixtureStartFail
	// FixtureAbiMismatch muniu_abi_version 返回 99(ABI 不匹配拒载)。
	FixtureAbiMismatch
	// FixtureLoop hook 死循环(超时 containment)。
	FixtureLoop
	// FixtureTrap hook unreachable(trap 隔离)。
	FixtureTrap
	// FixtureOOM hook 越界写(超内存 containment)。
	FixtureOOM
	// FixtureExit hook 调 wasi proc_exit(3)(ExitError 隔离 + WASI 导入联调)。
	FixtureExit
	// FixtureCfg get_config/log 宿主导入联调:读 config["greeting"] 读到即改写为配置值,
	// plugin_start 经 muniu.log 上报 "cfg-plugin-started"。
	FixtureCfg
	// FixtureMissingExport 缺 plugin_result 导出(拒载)。
	FixtureMissingExport
	// FixtureStartLoop plugin_start 死循环(注册探测超时测试)。
	FixtureStartLoop
	// FixtureFreeLoop free 中死循环(free 超时守卫测试:宿主必须有界中断并丢弃实例)。
	FixtureFreeLoop
)

// ABI 常量与宿主实现的 plugin 包单向对齐;此处仅复制 fixture 所需字面量,
// 避免 testkit 反向依赖业务包(conventions)。
const (
	fxVerdictPass     = 0
	fxVerdictModified = 1
	fxVerdictReject   = 2
	fxABIVersion      = 0
)

const (
	fxResultOff  = 8192 // 改写结果固定落点
	fxCfgKeyOff  = 4096
	fxCfgValOff  = 8192
	fxCfgMsgOff  = 4352
	fxCfgValCap  = 256
	fxMallocBase = 1024 // offset 0 保留为 malloc 失败哨兵
)

// fxModelBody FixtureModifyModel 的常量改写产物(合法 chat 体,model 为白名单外别名——
// 与 proxy 白名单测试的别名约定耦合,属测试设施内契约)。
const fxModelBody = `{"model":"alias-b","messages":[{"role":"user","content":"hi"}]}`

// FixturePlugin 装配一个最小插件二进制。
func FixturePlugin(kind PluginFixtureKind) []byte {
	hasCfg := kind == FixtureCfg
	hasEcho := kind == FixtureModifyEcho
	hasExit := kind == FixtureExit
	importCount := 0
	if hasCfg {
		importCount = 2 // muniu.log=0, muniu.get_config=1
	}
	if hasExit {
		importCount = 1 // wasi_snapshot_preview1.proc_exit=0
	}
	localBase := importCount
	// 局部函数序:version, start, malloc, free, hook, result(类型 0,0,1,2,3,4)
	fVersion := localBase
	fStart := localBase + 1
	fMalloc := localBase + 2
	fFree := localBase + 3
	fHook := localBase + 4
	fResult := localBase + 5

	// global 布局:0 = malloc 游标;cfg 加 1 = get_config 返回长度;echo 加 1/2 = 回显 off/len
	echoOffIdx, echoLenIdx := int64(1), int64(2)
	globalCount := 1
	if hasCfg {
		globalCount = 2
	}
	if hasEcho {
		globalCount = 3
		if hasCfg { // 互斥用法,防御性兜底:cfg 占 1 时 echo 顺延
			echoOffIdx, echoLenIdx = 2, 3
		}
	}

	out := &wasmBin{}
	out.b = append(out.b, 0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00) // magic + version

	// type 段:t0 ()->i32; t1 (i32)->i32; t2 (i32)->(); t3 (i32,i32)->i32;
	// t4 ()->i64; t5 (i32,i32,i32)->()(log); t6 (i32×4)->i32(get_config); t7 (i32)->()(proc_exit)
	types := &wasmBin{}
	types.uleb(8)
	for _, t := range [][]byte{
		{0x60, 0x00, 0x01, 0x7f},
		{0x60, 0x01, 0x7f, 0x01, 0x7f},
		{0x60, 0x01, 0x7f, 0x00},
		{0x60, 0x02, 0x7f, 0x7f, 0x01, 0x7f},
		{0x60, 0x00, 0x01, 0x7e},
		{0x60, 0x03, 0x7f, 0x7f, 0x7f, 0x00},
		{0x60, 0x04, 0x7f, 0x7f, 0x7f, 0x7f, 0x01, 0x7f},
		{0x60, 0x01, 0x7f, 0x00},
	} {
		types.b = append(types.b, t...)
	}
	section(out, 1, types)

	var imports *wasmBin
	switch {
	case hasCfg:
		imports = &wasmBin{}
		imports.uleb(2)
		imports.name("muniu")
		imports.name("log")
		imports.u8(0x00)
		imports.uleb(5)
		imports.name("muniu")
		imports.name("get_config")
		imports.u8(0x00)
		imports.uleb(6)
	case hasExit:
		imports = &wasmBin{}
		imports.uleb(1)
		imports.name("wasi_snapshot_preview1")
		imports.name("proc_exit")
		imports.u8(0x00)
		imports.uleb(7)
	}
	if imports != nil {
		section(out, 2, imports)
	}

	funcs := &wasmBin{}
	funcs.uleb(6)
	for _, t := range []uint64{0, 0, 1, 2, 3, 4} {
		funcs.uleb(t)
	}
	section(out, 3, funcs)

	mem := &wasmBin{}
	mem.uleb(1)
	mem.u8(0x00)
	mem.uleb(1)
	section(out, 5, mem)

	globals := &wasmBin{}
	globals.uleb(uint64(globalCount))
	globals.raw(0x7f, 0x01) // global0: mut i32 游标
	globals.i32const(fxMallocBase)
	globals.u8(0x0b)
	if hasCfg {
		globals.raw(0x7f, 0x01)
		globals.i32const(0)
		globals.u8(0x0b)
	}
	if hasEcho {
		for i := 0; i < 2; i++ { // 回显 off/len(echoOffIdx/echoLenIdx)
			globals.raw(0x7f, 0x01)
			globals.i32const(0)
			globals.u8(0x0b)
		}
	}
	section(out, 6, globals)

	exports := &wasmBin{}
	n := 7
	if kind == FixtureMissingExport {
		n = 6
	}
	exports.uleb(uint64(n))
	exports.name("memory")
	exports.raw(0x02, 0x00)
	exports.name("muniu_abi_version")
	exports.u8(0x00)
	exports.uleb(uint64(fVersion))
	exports.name("plugin_start")
	exports.u8(0x00)
	exports.uleb(uint64(fStart))
	exports.name("malloc")
	exports.u8(0x00)
	exports.uleb(uint64(fMalloc))
	exports.name("free")
	exports.u8(0x00)
	exports.uleb(uint64(fFree))
	exports.name("hook_request_body")
	exports.u8(0x00)
	exports.uleb(uint64(fHook))
	if kind != FixtureMissingExport {
		exports.name("plugin_result")
		exports.u8(0x00)
		exports.uleb(uint64(fResult))
	}
	section(out, 7, exports)

	// code 段:6 个函数体
	code := &wasmBin{}
	code.uleb(6)

	// muniu_abi_version
	b := &wasmBin{}
	b.u8(0x00)
	if kind == FixtureAbiMismatch {
		b.i32const(99)
	} else {
		b.i32const(fxABIVersion)
	}
	b.u8(0x0b)
	emitFunc(code, b)

	// plugin_start
	b = &wasmBin{}
	if hasCfg {
		// log(info, 4352, 18); n = get_config(4096, 8, 8192, 256); global1 = n; return 0
		b.u8(0x00)
		b.i32const(1)
		b.i32const(fxCfgMsgOff)
		b.i32const(18) // "cfg-plugin-started"
		b.raw(0x10, 0x00)
		b.i32const(fxCfgKeyOff)
		b.i32const(8)
		b.i32const(fxCfgValOff)
		b.i32const(fxCfgValCap)
		b.raw(0x10, 0x01)
		b.raw(0x24, 0x01)
		b.i32const(0)
	} else if kind == FixtureStartFail {
		b.u8(0x00)
		b.i32const(7)
	} else if kind == FixtureStartLoop {
		// plugin_start 死循环(loop br 0 + unreachable 尾;函数末 end 由外层统一补)
		b.raw(0x00, 0x03, 0x40, 0x0c, 0x00, 0x0b, 0x00)
	} else {
		b.u8(0x00)
		b.i32const(0)
	}
	b.u8(0x0b)
	emitFunc(code, b)

	// malloc:bump 游标(old = global0; global0 = old + len; return old)
	b = &wasmBin{}
	b.raw(0x01, 0x01, 0x7f) // 1 个 i32 局部
	b.raw(0x23, 0x00)       // global.get 0
	b.raw(0x21, 0x01)       // local.set 1
	b.raw(0x20, 0x01)       // local.get 1
	b.raw(0x20, 0x00)       // local.get 0
	b.u8(0x6a)              // i32.add
	b.raw(0x24, 0x00)       // global.set 0
	b.raw(0x20, 0x01)       // local.get 1
	b.u8(0x0b)
	emitFunc(code, b)

	// free:pass/nop,FreeLoop 变体为死循环(loop br 0 + unreachable 尾)
	b = &wasmBin{}
	if kind == FixtureFreeLoop {
		b.raw(0x00, 0x03, 0x40, 0x0c, 0x00, 0x0b, 0x00, 0x0b)
	} else {
		b.raw(0x00, 0x0b)
	}
	emitFunc(code, b)

	// hook_request_body
	b = &wasmBin{}
	switch kind {
	case FixtureModify, FixtureModifyModel:
		b.u8(0x00)
		b.i32const(fxVerdictModified)
		b.u8(0x0b)
	case FixtureModifyEcho:
		// global[echoOff] = off; global[echoLen] = len; return 1(回显:结果=入参 buffer 本体)
		b.u8(0x00)
		b.raw(0x20, 0x00) // local.get 0 (off)
		b.raw(0x24)
		b.uleb(uint64(echoOffIdx))
		b.raw(0x20, 0x01) // local.get 1 (len)
		b.raw(0x24)
		b.uleb(uint64(echoLenIdx))
		b.i32const(fxVerdictModified)
		b.u8(0x0b)
	case FixtureReject:
		b.u8(0x00)
		b.i32const(fxVerdictReject)
		b.u8(0x0b)
	case FixtureLoop:
		// (loop $l (br $l)) unreachable — 死循环;尾部 unreachable 满足校验器的结果型栈要求
		b.raw(0x00, 0x03, 0x40, 0x0c, 0x00, 0x0b, 0x00, 0x0b)
	case FixtureTrap:
		b.raw(0x00, 0x00, 0x0b) // unreachable
	case FixtureOOM:
		// i32.const 0x7fffffff 处 store → 越界写 trap(超内存形态)
		b.u8(0x00)
		b.i32const(0x7fffffff)
		b.i32const(1)
		b.raw(0x36, 0x02, 0x00, 0x00, 0x0b)
	case FixtureExit:
		// proc_exit(3) unreachable
		b.u8(0x00)
		b.i32const(3)
		b.raw(0x10, 0x00, 0x00, 0x0b)
	case FixtureCfg:
		// return global1 > 0(配置读到值即改写,verdict 1/0)
		b.raw(0x00, 0x23, 0x01, 0x41, 0x00, 0x4a, 0x0b)
	default: // FixturePass 等
		b.u8(0x00)
		b.i32const(fxVerdictPass)
		b.u8(0x0b)
	}
	emitFunc(code, b)

	// plugin_result
	b = &wasmBin{}
	b.u8(0x00)
	switch kind {
	case FixtureModify:
		b.i64const(int64(fxResultOff)<<32 | 6) // "MASKED"
	case FixtureModifyModel:
		b.i64const(int64(fxResultOff)<<32 | int64(len(fxModelBody))) // 常量合法 chat 体(model=alias-b)
	case FixtureReject:
		b.i64const(int64(fxResultOff)<<32 | 12) // "pii detected"
	case FixtureModifyEcho:
		// (global[echoLen] as i64) | ((global[echoOff] as i64) << 32)
		b.raw(0x23)
		b.uleb(uint64(echoLenIdx))
		b.u8(0xad)
		b.raw(0x23)
		b.uleb(uint64(echoOffIdx))
		b.u8(0xad)
		b.i64const(32)
		b.u8(0x86) // i64.shl
		b.u8(0x84) // i64.or
	case FixtureCfg:
		// (global1 as i64) | (8192 << 32)
		b.raw(0x23, 0x01, 0xad)
		b.i32const(fxCfgValOff)
		b.u8(0xad)
		b.i64const(32)
		b.raw(0x86, 0x84)
	default:
		b.i64const(0)
	}
	b.u8(0x0b)
	emitFunc(code, b)
	section(out, 10, code)

	// data 段(活动段,memory 0)
	var segs [][2]any
	switch kind {
	case FixtureModify:
		segs = [][2]any{{fxResultOff, "MASKED"}}
	case FixtureModifyModel:
		segs = [][2]any{{fxResultOff, fxModelBody}}
	case FixtureReject:
		segs = [][2]any{{fxResultOff, "pii detected"}}
	case FixtureCfg:
		segs = [][2]any{{fxCfgKeyOff, "greeting"}, {fxCfgMsgOff, "cfg-plugin-started"}}
	}
	if segs != nil {
		data := &wasmBin{}
		data.uleb(uint64(len(segs)))
		for _, sg := range segs {
			data.u8(0x00) // memory 0
			data.i32const(int32(sg[0].(int)))
			data.u8(0x0b)
			str := sg[1].(string)
			data.uleb(uint64(len(str)))
			data.b = append(data.b, str...)
		}
		section(out, 11, data)
	}
	return out.b
}

// emitFunc 追加一个函数体(len + bytes)。
func emitFunc(code, body *wasmBin) {
	code.uleb(uint64(len(body.b)))
	code.b = append(code.b, body.b...)
}

// FixtureChatBody 构造最小合法 chat 请求体。
func FixtureChatBody(model string) []byte {
	return []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hello"}]}`)
}
