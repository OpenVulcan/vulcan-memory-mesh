CREATE TABLE IF NOT EXISTS vmm_chat_sessions (
    session_id      TEXT PRIMARY KEY,
    user_id         TEXT NOT NULL,
    team_id         TEXT NOT NULL,
    project_id      TEXT NOT NULL,
    space_id        TEXT NULL,
    last_updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS vmm_chat_logs (
    session_id      TEXT NOT NULL,
    turn_index      INTEGER NOT NULL,
    user_id         TEXT NOT NULL,
    team_id         TEXT NOT NULL,
    project_id      TEXT NOT NULL,
    space_id        TEXT NULL,
    user_message    TEXT NOT NULL,
    assistant_reply TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (session_id, turn_index)
);

CREATE INDEX IF NOT EXISTS idx_vmm_chat_logs_scope ON vmm_chat_logs (user_id, project_id, space_id, session_id);
