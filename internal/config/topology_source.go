// 拓扑拉取源：控制面拓扑端点的条件请求客户端。
// 语义与价目拉取、吊销同步一致：ETag 条件请求，304 直接跳过；失败保持旧快照由调用方告警降级；
// payload 损坏拒绝应用。
package config

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// topologyCandidate 拓扑下发行的接收形态，与 YAML Candidate 同义，JSON 标签 snake_case。
type topologyCandidate struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Priority int    `json:"priority"`
	Weight   int    `json:"weight"`
}

// topologyProvider 供应商定义接收形态，与 YAML Provider 同义；key 以 env 引用形态下发。
type topologyProvider struct {
	Name    string `json:"name"`
	Adapter string `json:"adapter"`
	BaseURL string `json:"base_url"`
	Keys    []struct {
		Env    string `json:"env"`
		Weight int    `json:"weight"`
	} `json:"keys"`
	Weight   int `json:"weight"`
	Priority int `json:"priority"`
}

// TopologyPayload 控制面拓扑下发负载，与 muniu YAML 的 providers、model_routes、models 三件套同构；
// 价目、计量等其余字段不归拓扑源管。
type TopologyPayload struct {
	Providers   []topologyProvider             `json:"providers"`
	ModelRoutes map[string][]topologyCandidate `json:"model_routes"`
	Models      []ModelCapability              `json:"models"`
}

// ToTopology 将负载转为代理装配的三件套 Providers、Routes、Models。
// ETag 只保证传输一致，内容合法性仍以代理管道的守门为准。
func (p TopologyPayload) ToTopology() (providers []Provider,
	routes map[string][]Candidate, models []ModelCapability) {
	for _, tp := range p.Providers {
		prov := Provider{
			Name: tp.Name, Adapter: tp.Adapter, BaseURL: tp.BaseURL,
			Weight: tp.Weight, Priority: tp.Priority,
		}
		for _, k := range tp.Keys {
			prov.Keys = append(prov.Keys, KeyRef{Env: k.Env, Weight: k.Weight})
		}
		providers = append(providers, prov)
	}
	routes = make(map[string][]Candidate, len(p.ModelRoutes))
	for alias, cands := range p.ModelRoutes {
		for _, c := range cands {
			routes[alias] = append(routes[alias], Candidate(c)) // 同构类型直接转换
		}
	}
	models = append(models, p.Models...)
	return providers, routes, models
}

// FetchTopology 拉取并解析；etag 非空时带 If-None-Match，304 返回 notModified。
// err 非空时调用方保持旧快照并告警。
func FetchTopology(client *http.Client, url, etag string) (payload TopologyPayload, newETag string, notModified bool, err error) {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return payload, "", false, fmt.Errorf("构造拓扑拉取请求: %w", err)
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := client.Do(req)
	if err != nil {
		return payload, etag, false, fmt.Errorf("拓扑拉取失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotModified:
		return payload, etag, true, nil
	case resp.StatusCode != http.StatusOK:
		return payload, etag, false, fmt.Errorf("拓扑拉取状态 %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return payload, etag, false, fmt.Errorf("拓扑响应读取失败: %w", err)
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return payload, etag, false, fmt.Errorf("拓扑响应非法(损坏快照拒绝应用): %w", err)
	}
	if len(payload.Providers) == 0 || len(payload.ModelRoutes) == 0 {
		return payload, etag, false, fmt.Errorf("拓扑快照不完整: providers/model_routes 缺失")
	}
	return payload, resp.Header.Get("ETag"), false, nil
}
