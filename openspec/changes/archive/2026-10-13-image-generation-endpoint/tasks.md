# Tasks

- [x] 1.1 失败测试先行:protocol images DTO 解析(model/prompt 必填、Raw 保留)+ 档位键归一(`size|quality` 拼接、缺省不猜测)+ 预算精确预检判定(n×价 超界拒/价缺失旁路/零预算零回归)
- [x] 1.2 失败测试先行:候选循环语义——正常交付计量(cost=n×档位价,tokens 全 0,estimated=0)/ 档位未命中走兜底 / 无价目记零 / 失败候选 cost=0 观测行 / 5xx 换道只扣交付成本 / 断流不交付半包 / `images_not_supported` 400 不换道 / 别名不存在 400
- [x] 1.3 迁移 0011(`image_pricing` 表)+ store 读写 + YAML `image_pricing` 配置段与静态兜底目录
- [x] 1.4 `adapter.ImagePreparer` 能力接口 + OpenAI 兼容基座实现;`internal/proxy/images.go` lane handler(嵌入 dataPlaneMW 挂载、插件钩子、64MiB 响应上限、预算预检挂候选循环前)
- [x] 1.5 管理面:`GET/PUT/DELETE /admin/v1/image-pricing`(校验整单拒绝、原子热替换、来源标注)+ 路由表与漂移门禁同步
- [x] 1.6 openapi.yaml 同步(入站端点 + 管理端点)+ `pnpm generate:api`;控制台价目页 images 价目区(列表/编辑/删除,标准件)
- [x] 1.7 文档:metering.md「per-image 计价」节 + proxy-and-streaming 通道注记 + data-and-config schema 增补 + provider-presets 待验证标注
- [x] 1.8 `make check` 全绿 + web 四门禁 + 独立 review;archive + 主 specs 同步
