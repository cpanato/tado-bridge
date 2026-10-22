CREATE TABLE IF NOT EXISTS tado_bridge_snapshots (
    collected_at timestamptz PRIMARY KEY,
    payload jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object')
);
