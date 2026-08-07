-- Local SQLite-only demo data for manual organization Invoice verification.
-- Run from the repository root:
--   sqlite3 one-api.db < scripts/seed-organization-invoice-demo.sql
-- Then open /admin/organizations/910001, select the Invoice tab, and use
-- 2026-08-01 through 2026-08-31 as the billing period.
--
-- Re-running is safe for this reserved demo organization. It resets only the
-- demo settlement rules and logs whose request_id starts with invoice-demo-.

BEGIN IMMEDIATE;

INSERT INTO users (
  id,
  username,
  password,
  display_name,
  role,
  status,
  email,
  quota,
  used_quota,
  request_count,
  "group",
  aff_code,
  setting,
  remark,
  created_at,
  auth_version
) VALUES
  (
    910001,
    '组织账单管理员',
    'invoice-demo-disabled-login',
    '账单验收管理员',
    1,
    1,
    'invoice-demo-admin@example.invalid',
    0,
    0,
    0,
    'default',
    'invoice-demo-admin-910001',
    '{"billing_preference":"subscription_first"}',
    '本地组织 Invoice 模型分组验收账号',
    CAST(strftime('%s', '2026-07-31 16:00:00') AS INTEGER),
    1
  ),
  (
    910002,
    '组织账单成员',
    'invoice-demo-disabled-login',
    '账单验收成员',
    1,
    1,
    'invoice-demo-member@example.invalid',
    0,
    0,
    0,
    'default',
    'invoice-demo-member-910002',
    '{"billing_preference":"wallet_first"}',
    '本地组织 Invoice 模型分组验收账号',
    CAST(strftime('%s', '2026-07-31 16:00:00') AS INTEGER),
    1
  )
ON CONFLICT(id) DO UPDATE SET
  username = excluded.username,
  password = excluded.password,
  display_name = excluded.display_name,
  role = excluded.role,
  status = excluded.status,
  email = excluded.email,
  quota = excluded.quota,
  used_quota = excluded.used_quota,
  request_count = excluded.request_count,
  "group" = excluded."group",
  aff_code = excluded.aff_code,
  setting = excluded.setting,
  remark = excluded.remark,
  auth_version = excluded.auth_version;

INSERT INTO organizations (id, name, status, created_at, updated_at)
VALUES (
  910001,
  '组织账单模型分组验收数据',
  1,
  CAST(strftime('%s', '2026-07-31 16:00:00') AS INTEGER),
  CAST(strftime('%s', '2026-08-08 00:00:00') AS INTEGER)
)
ON CONFLICT(id) DO UPDATE SET
  name = excluded.name,
  status = excluded.status,
  updated_at = excluded.updated_at;

INSERT INTO organization_members (
  id,
  organization_id,
  user_id,
  role,
  joined_at,
  left_at,
  billing_start_at,
  current_key
) VALUES
  (
    910001,
    910001,
    910001,
    'admin',
    CAST(strftime('%s', '2026-07-31 16:00:00') AS INTEGER),
    0,
    0,
    '910001'
  ),
  (
    910002,
    910001,
    910002,
    'member',
    CAST(strftime('%s', '2026-07-31 16:00:00') AS INTEGER),
    0,
    0,
    '910002'
  )
ON CONFLICT(id) DO UPDATE SET
  organization_id = excluded.organization_id,
  user_id = excluded.user_id,
  role = excluded.role,
  joined_at = excluded.joined_at,
  left_at = excluded.left_at,
  billing_start_at = excluded.billing_start_at,
  current_key = excluded.current_key;

DELETE FROM organization_billing_settlement_rules
WHERE organization_id = 910001;

DELETE FROM logs
WHERE request_id LIKE 'invoice-demo-%';

INSERT INTO logs (
  user_id,
  created_at,
  type,
  content,
  username,
  token_name,
  model_name,
  quota,
  prompt_tokens,
  completion_tokens,
  use_time,
  is_stream,
  channel_id,
  channel_name,
  token_id,
  "group",
  ip,
  request_id,
  other
) VALUES
  (910001, CAST(strftime('%s', '2026-08-01 01:00:00') AS INTEGER), 2, '组织 Invoice 分类验收数据', 'invoice-demo-admin', 'invoice-demo', 'claude-fable-5', 500000, 4000, 400, 1200, 0, 0, 'Demo', 0, 'default', '127.0.0.1', 'invoice-demo-claude-fable', '{}'),
  (910002, CAST(strftime('%s', '2026-08-01 02:00:00') AS INTEGER), 2, '组织 Invoice 分类验收数据', 'invoice-demo-member', 'invoice-demo', 'claude-sonnet-5', 250000, 2000, 200, 900, 1, 0, 'Demo', 0, 'default', '127.0.0.1', 'invoice-demo-claude-sonnet', '{}'),
  (910001, CAST(strftime('%s', '2026-08-02 01:00:00') AS INTEGER), 2, '组织 Invoice 分类验收数据', 'invoice-demo-admin', 'invoice-demo', 'gpt-5.6-sol', 600000, 5000, 500, 1300, 0, 0, 'Demo', 0, 'default', '127.0.0.1', 'invoice-demo-gpt-sol', '{}'),
  (910002, CAST(strftime('%s', '2026-08-02 02:00:00') AS INTEGER), 2, '组织 Invoice 分类验收数据', 'invoice-demo-member', 'invoice-demo', 'gpt-image-2', 300000, 2500, 250, 1100, 0, 0, 'Demo', 0, 'default', '127.0.0.1', 'invoice-demo-gpt-image', '{}'),
  (910001, CAST(strftime('%s', '2026-08-03 01:00:00') AS INTEGER), 2, '组织 Invoice 分类验收数据', 'invoice-demo-admin', 'invoice-demo', 'gemini-3-flash-preview', 450000, 3800, 380, 1000, 1, 0, 'Demo', 0, 'default', '127.0.0.1', 'invoice-demo-gemini-flash', '{}'),
  (910002, CAST(strftime('%s', '2026-08-03 02:00:00') AS INTEGER), 2, '组织 Invoice 分类验收数据', 'invoice-demo-member', 'invoice-demo', 'gemini-3.1-pro-preview', 550000, 4700, 470, 1400, 0, 0, 'Demo', 0, 'default', '127.0.0.1', 'invoice-demo-gemini-pro', '{}'),
  (910001, CAST(strftime('%s', '2026-08-04 01:00:00') AS INTEGER), 2, '组织 Invoice 分类验收数据', 'invoice-demo-admin', 'invoice-demo', 'MiniMax-M3', 700000, 6000, 600, 1500, 0, 0, 'Demo', 0, 'default', '127.0.0.1', 'invoice-demo-minimax', '{}'),
  (910002, CAST(strftime('%s', '2026-08-04 02:00:00') AS INTEGER), 2, '组织 Invoice 分类验收数据', 'invoice-demo-member', 'invoice-demo', 'deepseek-v4-pro', 650000, 5400, 540, 1450, 0, 0, 'Demo', 0, 'default', '127.0.0.1', 'invoice-demo-deepseek', '{}'),
  (910001, CAST(strftime('%s', '2026-08-05 01:00:00') AS INTEGER), 2, '组织 Invoice 分类验收数据', 'invoice-demo-admin', 'invoice-demo', 'Kimi-3', 800000, 6800, 680, 1700, 1, 0, 'Demo', 0, 'default', '127.0.0.1', 'invoice-demo-kimi', '{}'),
  (910002, CAST(strftime('%s', '2026-08-05 02:00:00') AS INTEGER), 2, '组织 Invoice 分类验收数据', 'invoice-demo-member', 'invoice-demo', 'glm-5.2', 900000, 7600, 760, 1800, 0, 0, 'Demo', 0, 'default', '127.0.0.1', 'invoice-demo-glm-52', '{}'),
  (910001, CAST(strftime('%s', '2026-08-05 03:00:00') AS INTEGER), 2, '组织 Invoice 分类验收数据', 'invoice-demo-admin', 'invoice-demo', 'glm-5.1', 400000, 3300, 330, 1050, 0, 0, 'Demo', 0, 'default', '127.0.0.1', 'invoice-demo-glm-51', '{}'),
  (910002, CAST(strftime('%s', '2026-08-06 01:00:00') AS INTEGER), 2, '组织 Invoice 分类验收数据', 'invoice-demo-member', 'invoice-demo', 'qwen3.7-max', 750000, 6200, 620, 1600, 1, 0, 'Demo', 0, 'default', '127.0.0.1', 'invoice-demo-qwen-max', '{}'),
  (910001, CAST(strftime('%s', '2026-08-06 02:00:00') AS INTEGER), 2, '组织 Invoice 分类验收数据', 'invoice-demo-admin', 'invoice-demo', 'qwen3.7-plus', 250000, 2100, 210, 850, 0, 0, 'Demo', 0, 'default', '127.0.0.1', 'invoice-demo-qwen-plus', '{}'),
  (910002, CAST(strftime('%s', '2026-08-07 01:00:00') AS INTEGER), 2, '组织 Invoice 分类验收数据', 'invoice-demo-member', 'invoice-demo', 'text-embedding-3-large', 200000, 1800, 0, 600, 0, 0, 'Demo', 0, 'default', '127.0.0.1', 'invoice-demo-vector-large', '{}'),
  (910001, CAST(strftime('%s', '2026-08-07 02:00:00') AS INTEGER), 2, '组织 Invoice 分类验收数据', 'invoice-demo-admin', 'invoice-demo', 'text-embedding-3-small', 150000, 1400, 0, 550, 0, 0, 'Demo', 0, 'default', '127.0.0.1', 'invoice-demo-vector-small', '{}'),
  (910002, CAST(strftime('%s', '2026-08-07 03:00:00') AS INTEGER), 2, '组织 Invoice 分类验收数据', 'invoice-demo-member', 'invoice-demo', 'text-embedding-v4', 180000, 1600, 0, 580, 0, 0, 'Demo', 0, 'default', '127.0.0.1', 'invoice-demo-vector-v4', '{}'),
  (910001, CAST(strftime('%s', '2026-08-07 04:00:00') AS INTEGER), 2, '组织 Invoice 分类验收数据', 'invoice-demo-admin', 'invoice-demo', 'ctg-ac-ultra-latest', 120000, 1000, 0, 500, 0, 0, 'Demo', 0, 'default', '127.0.0.1', 'invoice-demo-vector-ac', '{}'),
  (910002, CAST(strftime('%s', '2026-08-07 05:00:00') AS INTEGER), 2, '组织 Invoice 分类验收数据', 'invoice-demo-member', 'invoice-demo', 'ctg-og-ultra-latest', 100000, 900, 0, 480, 0, 0, 'Demo', 0, 'default', '127.0.0.1', 'invoice-demo-vector-og', '{}'),
  (910001, CAST(strftime('%s', '2026-08-07 06:00:00') AS INTEGER), 2, '组织 Invoice 分类验收数据', 'invoice-demo-admin', 'invoice-demo', 'gpt-4o', 333333, 2800, 280, 950, 0, 0, 'Demo', 0, 'default', '127.0.0.1', 'invoice-demo-gpt-4o', '{}'),
  (910002, CAST(strftime('%s', '2026-08-07 07:00:00') AS INTEGER), 2, '组织 Invoice 分类验收数据', 'invoice-demo-member', 'invoice-demo', 'glm-5-turbo', 200000, 1700, 170, 650, 0, 0, 'Demo', 0, 'default', '127.0.0.1', 'invoice-demo-glm-5-turbo', '{}');

UPDATE logs
SET username = (
  SELECT users.username
  FROM users
  WHERE users.id = logs.user_id
)
WHERE request_id LIKE 'invoice-demo-%';

COMMIT;
