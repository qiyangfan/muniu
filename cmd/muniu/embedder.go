// 语义缓存的嵌入通道(T4.5):进程内自调用网关 /v1/embeddings
// (semantic-cache.md「嵌入经本网关通道」;内部 key 每次启动重签,明文只存内存)。
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	"github.com/solvela/muniu/internal/auth"
)

// 内部嵌入 key 的名称(每次启动重签并吊销旧 key,明文不可恢复故无法复用)。
const internalEmbedKeyName = "internal-semantic-embedder"

// lateEmbedder 延迟绑定嵌入器:HTTP 处理链装配完成后才能自调用。
type lateEmbedder struct {
	mu sync.Mutex
	fn func(ctx context.Context, model, input string) ([]float32, error)
}

func (e *lateEmbedder) Embed(ctx context.Context, model, input string) ([]float32, error) {
	e.mu.Lock()
	fn := e.fn
	e.mu.Unlock()
	if fn == nil {
		return nil, fmt.Errorf("embedder: 嵌入通道未就绪")
	}
	return fn(ctx, model, input)
}

func (e *lateEmbedder) set(fn func(ctx context.Context, model, input string) ([]float32, error)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.fn = fn
}

// ensureInternalEmbedKey 每次启动签发新的内部嵌入 key(旧的 active 同名 key 全部吊销)。
func ensureInternalEmbedKey(db *sql.DB, logger *slog.Logger) string {
	keys, err := auth.ListKeys(db)
	if err == nil {
		for _, k := range keys {
			if k.Name == internalEmbedKeyName {
				_, _ = auth.RevokeKeyByPrefix(db, k.Prefix)
			}
		}
	}
	plaintext, _, err := auth.CreateKey(db, "default", internalEmbedKeyName, 600)
	if err != nil {
		logger.Error("内部嵌入 key 签发失败", "error_detail", err.Error())
		return ""
	}
	return plaintext
}

// recorder 最小 ResponseWriter(进程内自调用收集响应)。
type recorder struct {
	header http.Header
	code   int
	buf    bytes.Buffer
}

func (w *recorder) Header() http.Header  { return w.header }
func (w *recorder) WriteHeader(code int) { w.code = code }
func (w *recorder) Write(p []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	return w.buf.Write(p)
}
func (w *recorder) Flush() {}

// bindEmbedder 把「进程内自调用 /v1/embeddings」的嵌入函数绑定到 lateEmbedder。
func bindEmbedder(late *lateEmbedder, handler http.Handler, embedKey string) {
	late.set(func(ctx context.Context, model, input string) ([]float32, error) {
		// body 必须经 json.Marshal 构造:fmt %q 是 Go 字面量转义,\v/\x01 等控制字符
		// 不在 JSON 转义白名单内,会产生非法 JSON 让嵌入通道整链失效
		body, err := json.Marshal(struct {
			Model string `json:"model"`
			Input string `json:"input"`
		}{Model: model, Input: input})
		if err != nil {
			return nil, fmt.Errorf("embedder: 构造嵌入请求: %w", err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/v1/embeddings", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+embedKey)
		rw := &recorder{header: http.Header{}}
		handler.ServeHTTP(rw, req)
		if rw.code != http.StatusOK {
			return nil, fmt.Errorf("embedder: 嵌入通道状态 %d", rw.code)
		}
		var resp struct {
			Data []struct {
				Embedding []float32 `json:"embedding"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rw.buf.Bytes(), &resp); err != nil {
			return nil, fmt.Errorf("embedder: 嵌入响应非法: %w", err)
		}
		if len(resp.Data) == 0 || len(resp.Data[0].Embedding) == 0 {
			return nil, fmt.Errorf("embedder: 嵌入响应无数据")
		}
		return resp.Data[0].Embedding, nil
	})
}
