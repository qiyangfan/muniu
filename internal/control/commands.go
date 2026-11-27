// 运行时命令执行器：消费锦官跨服务命令（有限动作枚举）并幂等执行/回执；
// 本地运行命令由内嵌控制台直接调用同一执行器，不经控制面队列。
package control

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"uuid"

	"github.com/solvela/muniu/internal/store"
)

const (
	// cachePurgeAction 清理语义缓存。
	cachePurgeAction = "cache_purge"
	// topologyReloadAction 重载配置文件拓扑。
	topologyReloadAction = "topology_reload"
	// actionSchemaVersion 当前支持的动作参数 schema 版本。
	actionSchemaVersion = 1
	// maxAckAttempts ack 失败重试上限，超过后保留本地终态等待人工排查。
	maxAckAttempts = 5
	// maxParamsBytes 单命令参数上限。
	maxParamsBytes = 64 << 10
)

// CommandView 命令台账对外视图（控制台）。
type CommandView struct {
	ID                  string          `json:"id"`
	Action              string          `json:"action"`
	ActionSchemaVersion int64           `json:"action_schema_version"`
	Params              json.RawMessage `json:"params,omitempty"`
	Source              string          `json:"source"`
	Status              string          `json:"status"`
	Result              string          `json:"result,omitempty"`
	Attempts            int64           `json:"attempts"`
	IdempotencyKey      string          `json:"idempotency_key,omitempty"`
	Actor               string          `json:"actor,omitempty"`
	Reason              string          `json:"reason,omitempty"`
	CreatedAt           string          `json:"created_at"`
	CompletedAt         string          `json:"completed_at,omitempty"`
}

// WireCommand 锦官队列下发的命令形态。
type WireCommand struct {
	ID                  string          `json:"id"`
	Action              string          `json:"action"`
	ActionSchemaVersion int64           `json:"action_schema_version"`
	Params              json.RawMessage `json:"params"`
	IdempotencyKey      string          `json:"idempotency_key"`
	Actor               string          `json:"actor"`
	Reason              string          `json:"reason"`
}

// CommandTargets 可执行动作的宿主能力。
type CommandTargets interface {
	PurgeCache(ctx context.Context, tenant string, all bool) (int, error)
	ReloadTopology(ctx context.Context) error
}

// CommandAudit 跨服务命令执行审计回调。
type CommandAudit func(ctx context.Context, actor, action, target, outcome, detail string)

// CommandExecutor 命令执行与回执。
type CommandExecutor struct {
	DB           *sql.DB
	Client       *Client
	InstanceID   string
	Targets      CommandTargets
	Audit        CommandAudit
	Logger       *slog.Logger
	PollInterval time.Duration
	Now          func() time.Time
}

// NewCommandExecutor 构造；缺省 15s claim 间隔。
func NewCommandExecutor(db *sql.DB, client *Client, instanceID string, targets CommandTargets,
	audit CommandAudit, poll time.Duration, logger *slog.Logger) *CommandExecutor {
	if poll <= 0 {
		poll = 15 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &CommandExecutor{DB: db, Client: client, InstanceID: instanceID, Targets: targets,
		Audit: audit, Logger: logger, PollInterval: poll, Now: time.Now}
}

// Run claim 循环；控制面不可达只告警，不影响数据面与本地命令。
func (e *CommandExecutor) Run(ctx context.Context) {
	ticker := time.NewTicker(e.PollInterval)
	defer ticker.Stop()
	for {
		if err := e.ClaimOnce(ctx); err != nil {
			e.Logger.Warn("运行时命令 claim 失败", "error_detail", err.Error())
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// claimResponse claim 响应。
type claimResponse struct {
	Commands []WireCommand `json:"commands"`
}

// ClaimOnce 拉取一批命令并逐条执行/回执。
func (e *CommandExecutor) ClaimOnce(ctx context.Context) error {
	body := map[string]any{"service_id": ServiceID, "instance_id": e.InstanceID, "limit": 10}
	_, raw, err := e.Client.Post(ctx, e.Client.Paths.Claim, body)
	if err != nil {
		return err
	}
	var resp claimResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fmt.Errorf("control: claim 响应非法: %w", err)
	}
	for _, cmd := range resp.Commands {
		e.handle(ctx, cmd)
	}
	return nil
}

// handle 单条命令：先校验后执行；幂等键命中已终态时不重复执行。
func (e *CommandExecutor) handle(ctx context.Context, cmd WireCommand) {
	if existing, err := store.GetControlCommand(ctx, e.DB, cmd.ID); err == nil {
		if existing.Status == "acked" || existing.Status == "failed" {
			_ = e.ack(ctx, cmd.ID, existing.Status, existing.Result, "")
			return
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		e.Logger.Error("运行时命令台账读取失败", "id", cmd.ID, "error_detail", err.Error())
		return
	}
	status, result, errMsg := e.execute(ctx, cmd.Action, cmd.ActionSchemaVersion, cmd.Params)
	row := store.ControlCommandRow{
		ID: cmd.ID, Action: cmd.Action, ActionSchemaVersion: cmd.ActionSchemaVersion,
		Params: string(cmd.Params), Source: "control", Status: status, Result: result,
		IdempotencyKey: cmd.IdempotencyKey, Actor: cmd.Actor, Reason: cmd.Reason,
		CreatedAt: e.nowRFC(), CompletedAt: e.nowRFC(),
	}
	if _, err := store.InsertControlCommand(ctx, e.DB, row); err != nil {
		e.Logger.Error("运行时命令台账写入失败", "id", cmd.ID, "error_detail", err.Error())
		return
	}
	if err := store.UpdateControlCommandResult(ctx, e.DB, cmd.ID, status, result, e.nowRFC()); err != nil {
		e.Logger.Error("运行时命令台账更新失败", "id", cmd.ID, "error_detail", err.Error())
	}
	e.audit(ctx, cmd.Actor, cmd.Action, cmd.ID, status, errMsg)
	if err := e.ack(ctx, cmd.ID, status, result, errMsg); err != nil {
		e.retryAck(ctx, cmd.ID, status, result, errMsg)
	}
}

// retryAck ack 失败重试；上限后保留本地终态，等待下一次 claim 或人工排查。
func (e *CommandExecutor) retryAck(ctx context.Context, id, status, result, errMsg string) {
	n, err := store.BumpControlCommandAttempts(ctx, e.DB, id)
	if err != nil {
		e.Logger.Error("运行时命令 ack 重试计数失败", "id", id, "error_detail", err.Error())
		return
	}
	if n >= maxAckAttempts {
		e.Logger.Error("运行时命令 ack 达到重试上限", "id", id, "attempts", n)
		return
	}
	if err := e.ack(ctx, id, status, result, errMsg); err != nil {
		e.Logger.Warn("运行时命令 ack 失败,等待下次 claim 重试", "id", id, "error_detail", err.Error())
	}
}

// ack 回执控制面。
func (e *CommandExecutor) ack(ctx context.Context, id, status, result, errMsg string) error {
	body := map[string]any{
		"service_id": ServiceID, "instance_id": e.InstanceID,
		"status": status, "result": truncate(result, 2048), "error": truncate(errMsg, 1024),
	}
	_, _, err := e.Client.Post(ctx, e.Client.Paths.AckPath(id), body)
	return err
}

// execute 校验并执行有限动作；未知 action/版本/参数一律 failed 且不执行。
func (e *CommandExecutor) execute(ctx context.Context, action string, version int64,
	params json.RawMessage) (status, result, errMsg string) {
	if version != actionSchemaVersion {
		return "failed", "", fmt.Sprintf("不支持的 action_schema_version %d", version)
	}
	if len(params) > maxParamsBytes {
		return "failed", "", "参数过大"
	}
	purge, err := parsePurgeParams(action, params)
	if err != nil {
		return "failed", "", err.Error()
	}
	if e.Targets == nil {
		return "failed", "", "执行目标未装配"
	}
	switch action {
	case cachePurgeAction:
		n, err := e.Targets.PurgeCache(ctx, purge.tenant, purge.all)
		if err != nil {
			return "failed", "", err.Error()
		}
		return "acked", fmt.Sprintf("purged=%d", n), ""
	case topologyReloadAction:
		if err := e.Targets.ReloadTopology(ctx); err != nil {
			return "failed", "", err.Error()
		}
		return "acked", "topology reloaded", ""
	default:
		return "failed", "", "未注册的 action"
	}
}

// purgeParams cache_purge 参数：all 或 tenant 二选一。
type purgeParams struct {
	Tenant string `json:"tenant"`
	All    bool   `json:"all"`
	tenant string
	all    bool
}

// parsePurgeParams 严格解析并校验动作参数；topology_reload 只接受空对象。
func parsePurgeParams(action string, raw json.RawMessage) (purgeParams, error) {
	if strings.TrimSpace(string(raw)) == "" {
		raw = json.RawMessage(`{}`)
	}
	switch action {
	case topologyReloadAction:
		var empty map[string]json.RawMessage
		if err := strictDecode(raw, &empty); err != nil {
			return purgeParams{}, fmt.Errorf("参数非法: %w", err)
		}
		if len(empty) != 0 {
			return purgeParams{}, errors.New("topology_reload 不接受参数")
		}
		return purgeParams{}, nil
	case cachePurgeAction:
		var p purgeParams
		if err := strictDecode(raw, &p); err != nil {
			return purgeParams{}, fmt.Errorf("参数非法: %w", err)
		}
		tenant := strings.TrimSpace(p.Tenant)
		if p.All == (tenant != "") {
			return purgeParams{}, errors.New("cache_purge 须在 all=true 与 tenant 之间二选一")
		}
		if len(tenant) > 128 {
			return purgeParams{}, errors.New("tenant 过长(≤128)")
		}
		p.tenant, p.all = tenant, p.All
		return p, nil
	default:
		return purgeParams{}, errors.New("未注册的 action")
	}
}

// strictDecode 拒绝未知字段与多个 JSON 值。
func strictDecode(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return errors.New("须为单个 JSON 对象")
	}
	return nil
}

// ExecuteLocal 内嵌控制台直接执行：不经队列，锦官不可用仍可用。
func (e *CommandExecutor) ExecuteLocal(ctx context.Context, actor, reason, action string,
	params json.RawMessage) (CommandView, error) {
	id := uuid.NewV7().String()
	status, result, errMsg := e.execute(ctx, action, actionSchemaVersion, params)
	if errMsg == "" && status == "failed" {
		errMsg = result
	}
	row := store.ControlCommandRow{
		ID: id, Action: action, ActionSchemaVersion: actionSchemaVersion, Params: string(params),
		Source: "local", Status: status, Result: result, Actor: actor, Reason: reason,
		CreatedAt: e.nowRFC(), CompletedAt: e.nowRFC(),
	}
	if _, err := store.InsertControlCommand(ctx, e.DB, row); err != nil {
		return CommandView{}, err
	}
	view := viewOf(row)
	if status != "acked" {
		return view, fmt.Errorf("命令执行失败: %s", errMsg)
	}
	return view, nil
}

// Commands 最近命令台账。
func (e *CommandExecutor) Commands(ctx context.Context, limit int) ([]CommandView, error) {
	rows, err := store.ListControlCommands(ctx, e.DB, limit)
	if err != nil {
		return nil, err
	}
	out := make([]CommandView, 0, len(rows))
	for _, r := range rows {
		out = append(out, viewOf(r))
	}
	return out, nil
}

// viewOf 台账行转视图。
func viewOf(r store.ControlCommandRow) CommandView {
	var params json.RawMessage
	if strings.TrimSpace(r.Params) != "" {
		params = json.RawMessage(r.Params)
	}
	return CommandView{
		ID: r.ID, Action: r.Action, ActionSchemaVersion: r.ActionSchemaVersion, Params: params,
		Source: r.Source, Status: r.Status, Result: r.Result, Attempts: r.Attempts,
		IdempotencyKey: r.IdempotencyKey, Actor: r.Actor, Reason: r.Reason,
		CreatedAt: r.CreatedAt, CompletedAt: r.CompletedAt,
	}
}

// audit 跨服务命令审计；回调为空时仅保留台账。
func (e *CommandExecutor) audit(ctx context.Context, actor, action, target, outcome, detail string) {
	if e.Audit == nil {
		return
	}
	e.Audit(ctx, actor, action, target, outcome, detail)
}

// nowRFC 台账时间口径。
func (e *CommandExecutor) nowRFC() string {
	t := time.Now()
	if e.Now != nil {
		t = e.Now()
	}
	return t.UTC().Format(time.RFC3339)
}

// truncate 审计/回执字段截断，避免异常长文本撑爆台账。
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
