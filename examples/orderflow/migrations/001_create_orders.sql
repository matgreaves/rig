CREATE TABLE orders (
    id TEXT PRIMARY KEY,
    customer TEXT NOT NULL,
    items JSONB NOT NULL DEFAULT '[]',
    status TEXT NOT NULL DEFAULT 'pending',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
