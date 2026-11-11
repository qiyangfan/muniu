// auth/账号声明跨源共享校验(oauth-subscription-lane 契约):YAML 加载(config.validate)
// 与控制面拓扑守门(proxy.ReplaceTopology)消费同一套判定——值域契约只在 YAML 源生效
// 即跨 lane 行为劈叉(评审域一 P1)。违规描述带 provider 名,位置前缀由调用方语境自定。
package config

import (
	"fmt"
	"net/url"

	"github.com/solvela/muniu/internal/adapter"
)

// AuthDeclarationViolations auth 声明值域/适配器兼容/渠道内同构/重复引用/oauth 段
// 完整性的全量判定;返回违规描述列表,零违规返回 nil。
func AuthDeclarationViolations(provs []Provider, oauth OAuthCfg) []string {
	var out []string
	if oauth.Declared() && !oauth.Complete() {
		out = append(out, "oauth 段部分声明非法: authorize_url/token_url/client_id/callback_url 必须成套声明")
	}
	for _, u := range []struct{ name, v string }{
		{"authorize_url", oauth.AuthorizeURL},
		{"token_url", oauth.TokenURL},
		{"callback_url", oauth.CallbackURL},
	} {
		if u.v == "" {
			continue
		}
		pu, perr := url.Parse(u.v)
		if perr != nil || (pu.Scheme != "http" && pu.Scheme != "https") || pu.Host == "" || pu.Fragment != "" {
			out = append(out, fmt.Sprintf("oauth.%s 非法: %q(须为 http(s) 绝对 URL)", u.name, u.v))
		}
	}
	// 渠道内重复账号引用:同渠道重复引用同一账号会建两个同 ID 池成员,凭据同步只达首个,
	// 其余成员持装配期旧 token 永久 401 摆动——加载期拒绝(对齐 providers 重名先例)。
	accountsByRef := map[string]string{}
	for _, p := range provs {
		if p.Auth != "" {
			// 仅 Anthropic 支持声明鉴权方式;值域文案经 adapter.AuthDecl 单源渲染。
			if p.Adapter != "anthropic" {
				out = append(out, fmt.Sprintf("provider(%s) auth 仅 anthropic 适配器支持(其余形态鉴权固有)", p.Name))
			} else if p.Auth != adapter.AuthDefault && p.Auth != adapter.AuthBearer && p.Auth != adapter.AuthOAuth {
				out = append(out, fmt.Sprintf("provider(%s) auth 非法 %q(允许 %s)", p.Name, p.Auth, adapter.AuthDecl()))
			}
		}
		if p.Auth == adapter.AuthOAuth && !oauth.Complete() {
			out = append(out, fmt.Sprintf("provider(%s) auth=oauth 需要 oauth 段完整声明(authorize_url/token_url/client_id/callback_url)", p.Name))
		}
		for _, k := range p.Keys {
			// 渠道内同构:鉴权头形态是适配器级语义,混编成员会让适配器 per-key 分叉特例
			if p.Auth == adapter.AuthOAuth {
				if k.Env != "" || k.Account == "" {
					out = append(out, fmt.Sprintf("provider(%s) keys oauth 渠道 keys 须全部为账号引用(account)", p.Name))
				}
			} else if k.Account != "" {
				out = append(out, fmt.Sprintf("provider(%s) keys 账号引用仅限 auth: oauth 渠道", p.Name))
			}
			if k.Env == "" && k.Account == "" {
				out = append(out, fmt.Sprintf("provider(%s) keys 缺 env 或 account 引用", p.Name))
			}
			if k.Account != "" {
				if prev, dup := accountsByRef[k.Account]; dup && prev == p.Name {
					out = append(out, fmt.Sprintf("provider(%s) keys 账号引用重复: %s(同渠道重复引用使凭据同步失准,每账号至多一个成员)", p.Name, k.Account))
				}
				accountsByRef[k.Account] = p.Name
			}
		}
	}
	return out
}
