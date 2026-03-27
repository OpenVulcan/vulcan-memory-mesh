-- 001_init.sql declares the current DockDB baseline schema used by the local VMM runtime.
-- 001_init.sql 用于声明本地 VMM 运行时当前使用的 DockDB 基线表结构。

CREATE TABLE IF NOT EXISTS vmm_version (
    schema_version INTEGER NOT NULL,
    updated_at     TEXT    NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS vmm_noise_embeddings (
    scope         TEXT    NOT NULL,
    language      TEXT    NOT NULL,
    category_name TEXT    NOT NULL,
    phrase        TEXT    NOT NULL,
    model         TEXT    NOT NULL,
    dimension     INTEGER NOT NULL,
    rules_hash    TEXT    NOT NULL,
    vector_json   TEXT    NOT NULL,
    updated_at    TEXT    NOT NULL,
    PRIMARY KEY (scope, language, category_name, phrase, model, dimension, rules_hash)
);

CREATE TABLE IF NOT EXISTS vmm_users (
    id                  BIGINT PRIMARY KEY,
    name                TEXT NOT NULL UNIQUE,
    delete_confirm_code TEXT NOT NULL DEFAULT '',
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS vmm_teams (
    id         BIGINT PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS vmm_spaces (
    id         BIGINT PRIMARY KEY,
    team_id    BIGINT NOT NULL,
    name       TEXT   NOT NULL,
    created_at TEXT   NOT NULL,
    updated_at TEXT   NOT NULL,
    UNIQUE(team_id, name),
    FOREIGN KEY(team_id) REFERENCES vmm_teams(id)
);

CREATE TABLE IF NOT EXISTS vmm_projects (
    id         BIGINT PRIMARY KEY,
    team_id    BIGINT NOT NULL,
    space_id   BIGINT NOT NULL,
    name       TEXT   NOT NULL,
    created_at TEXT   NOT NULL,
    updated_at TEXT   NOT NULL,
    UNIQUE(space_id, name),
    FOREIGN KEY(team_id) REFERENCES vmm_teams(id),
    FOREIGN KEY(space_id) REFERENCES vmm_spaces(id)
);

CREATE TABLE IF NOT EXISTS vmm_sessions (
    id                           BIGINT PRIMARY KEY,
    session_key                  TEXT   NOT NULL UNIQUE,
    user_id                      BIGINT NOT NULL,
    team_id                      BIGINT NOT NULL,
    space_id                     BIGINT NOT NULL,
    project_id                   BIGINT NOT NULL,
    message_count                BIGINT NOT NULL DEFAULT 0,
    last_message_index           BIGINT NOT NULL DEFAULT 0,
    last_extracted_message_index BIGINT NOT NULL DEFAULT 0,
    created_at                   TEXT   NOT NULL,
    updated_at                   TEXT   NOT NULL,
    FOREIGN KEY(user_id) REFERENCES vmm_users(id),
    FOREIGN KEY(project_id) REFERENCES vmm_projects(id)
);

CREATE INDEX IF NOT EXISTS idx_vmm_sessions_scope
ON vmm_sessions(user_id, team_id, space_id, project_id, updated_at);

CREATE TABLE IF NOT EXISTS vmm_chat_messages (
    id            BIGINT PRIMARY KEY,
    session_id    BIGINT NOT NULL,
    message_index BIGINT NOT NULL,
    role          TEXT   NOT NULL,
    content       TEXT   NOT NULL,
    source_kind   TEXT   NOT NULL,
    created_at    TEXT   NOT NULL,
    UNIQUE(session_id, message_index),
    FOREIGN KEY(session_id) REFERENCES vmm_sessions(id)
);

CREATE INDEX IF NOT EXISTS idx_vmm_chat_messages_session
ON vmm_chat_messages(session_id, message_index);

CREATE TABLE IF NOT EXISTS vmm_memory_entries (
    id            TEXT   PRIMARY KEY,
    team_id       BIGINT NOT NULL,
    space_id      BIGINT NOT NULL,
    project_id    BIGINT NOT NULL,
    session_id    BIGINT NOT NULL,
    user_id       BIGINT NOT NULL,
    content       TEXT   NOT NULL,
    vector_json   TEXT   NOT NULL DEFAULT '[]',
    metadata_json TEXT   NOT NULL DEFAULT '{}',
    created_at    TEXT   NOT NULL,
    updated_at    TEXT   NOT NULL,
    FOREIGN KEY(user_id) REFERENCES vmm_users(id),
    FOREIGN KEY(project_id) REFERENCES vmm_projects(id),
    FOREIGN KEY(session_id) REFERENCES vmm_sessions(id)
);

CREATE INDEX IF NOT EXISTS idx_vmm_memory_entries_scope
ON vmm_memory_entries(user_id, team_id, space_id, project_id, session_id, updated_at);
