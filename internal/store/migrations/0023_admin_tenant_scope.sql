-- 管理面租户范围(admin-tenant-scope):scope 是显式枚举,不从绑定有无反推;
-- 新主体默认 TENANT(失败关闭),存量主体 backfill GLOBAL(升级不锁死老管理员)。
-- 绑定整包替换由 store 事务实现;tenants 无删除路径,绑定无需级联。
ALTER TABLE admin_principals ADD COLUMN scope TEXT NOT NULL DEFAULT 'TENANT' CHECK(scope IN ('GLOBAL','TENANT'));
UPDATE admin_principals SET scope='GLOBAL';
CREATE TABLE IF NOT EXISTS admin_principal_tenants (
  subject   TEXT NOT NULL REFERENCES admin_principals(subject),
  tenant_id TEXT NOT NULL REFERENCES tenants(id),
  PRIMARY KEY (subject, tenant_id)
);
