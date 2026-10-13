CREATE TABLE image_pricing (
 alias TEXT PRIMARY KEY,
 currency TEXT NOT NULL CHECK (currency IN ('CNY','USD')),
 default_credits INTEGER NOT NULL CHECK (default_credits > 0),
 tiers TEXT NOT NULL DEFAULT '{}',
 updated_at TEXT NOT NULL
);
