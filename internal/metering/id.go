// 计量行主键与时间源(独立变量便于测试注入)。
package metering

import (
	"time"

	"github.com/google/uuid"
)

// newUsageID usage_logs 主键:UUIDv7(与 request_id 同源,天然去重底座)。
func newUsageID() string { return uuid.Must(uuid.NewV7()).String() }

// timeNowUTC 时间源(变量化,测试可注入)。
var timeNowUTC = func() time.Time { return time.Now().UTC() }
