-- 多模态向量用量与视觉输入价；既有记录与旧价目视觉部分默认为 0。
ALTER TABLE usage_logs ADD COLUMN vision_tokens INTEGER NOT NULL DEFAULT 0 CHECK (vision_tokens >= 0);
ALTER TABLE model_pricing ADD COLUMN vision_input_credits INTEGER NOT NULL DEFAULT 0 CHECK (vision_input_credits >= 0);
