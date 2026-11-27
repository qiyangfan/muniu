// 用量上报汇：先写本地 SQLite（计量权威兜底），再以服务身份推送锦官。
// 推送失败返回错误，由计量管道整批落本地缓冲并在恢复后按序续传；行主键幂等。
package control

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/solvela/muniu/internal/metering"
	"github.com/solvela/muniu/internal/store"
)

// UsageSink 本地 + 控制面双写；实现 metering.Sink。
type UsageSink struct {
	Local  *metering.LocalSink
	Client *Client
	Ctx    context.Context
}

// NewUsageSink 构造；ctx 只承载生命周期，投递剥离取消（关机排干必须写完）。
func NewUsageSink(ctx context.Context, db *sql.DB, client *Client) *UsageSink {
	return &UsageSink{Local: metering.NewLocalSink(ctx, db), Client: client,
		Ctx: context.WithoutCancel(ctx)}
}

// Name 汇名。
func (s *UsageSink) Name() string { return "control-plane-usage" }

// Write 先本地落库（幂等），再推送控制面；任一失败交给管道缓冲重试。
func (s *UsageSink) Write(batch []store.UsageRow) error {
	if err := s.Local.Write(batch); err != nil {
		return err
	}
	token, err := s.Client.Token.Token(s.Ctx)
	if err != nil {
		return fmt.Errorf("control: 获取服务令牌失败: %w", err)
	}
	remote := &metering.IngestSink{
		Ctx: s.Ctx, URL: s.Client.BaseURL + s.Client.Paths.Usage, Token: token,
		Source: ServiceID, SchemaVersion: SchemaVersion,
	}
	return remote.Write(batch)
}
