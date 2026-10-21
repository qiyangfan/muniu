// wasm 二进制装配原语，提供 LEB128 编码与段封装，仅供测试 fixture 消费。
package testkit

type wasmBin struct{ b []byte }

func (w *wasmBin) u8(v byte) { w.b = append(w.b, v) }

func (w *wasmBin) raw(vs ...byte) { w.b = append(w.b, vs...) }

func (w *wasmBin) uleb(v uint64) {
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			b |= 0x80
		}
		w.u8(b)
		if v == 0 {
			return
		}
	}
}

func (w *wasmBin) sleb(v int64) {
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if (v == 0 && b&0x40 == 0) || (v == -1 && b&0x40 != 0) {
			w.u8(b)
			return
		}
		w.u8(b | 0x80)
	}
}

func (w *wasmBin) name(s string) { w.uleb(uint64(len(s))); w.b = append(w.b, s...) }

func (w *wasmBin) i32const(v int32) { w.u8(0x41); w.sleb(int64(v)) }
func (w *wasmBin) i64const(v int64) { w.u8(0x42); w.sleb(v) }

// section 封装一个 wasm 段，写入 id、长度前缀与载荷。
func section(out *wasmBin, id byte, payload *wasmBin) {
	out.u8(id)
	out.uleb(uint64(len(payload.b)))
	out.b = append(out.b, payload.b...)
}
