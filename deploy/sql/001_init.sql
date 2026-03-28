-- 001_init.sql declares the current DuckDB baseline schema used by the local VMM runtime.
-- 001_init.sql 用于声明本地 VMM 运行时当前使用的 DuckDB 基线表结构。

CREATE TABLE IF NOT EXISTS vmm_version (
    singleton_id   INTEGER PRIMARY KEY,
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
    session_key                  TEXT   NOT NULL,
    user_id                      BIGINT NOT NULL,
    team_id                      BIGINT NOT NULL,
    space_id                     BIGINT NOT NULL,
    project_id                   BIGINT NOT NULL,
    turn_count                   INTEGER NOT NULL DEFAULT 0,
    last_summarized_id           BIGINT NOT NULL DEFAULT 0,
    summarize_content            TEXT    NOT NULL DEFAULT '',
    summarize_budget             INTEGER NOT NULL DEFAULT 0,
    created_timestamp            BIGINT  NOT NULL,
    updated_timestamp            BIGINT  NOT NULL,
    UNIQUE(project_id, session_key),
    FOREIGN KEY(user_id) REFERENCES vmm_users(id),
    FOREIGN KEY(project_id) REFERENCES vmm_projects(id)
);

CREATE INDEX IF NOT EXISTS idx_vmm_sessions_scope
ON vmm_sessions(user_id, team_id, space_id, project_id, updated_timestamp);

CREATE INDEX IF NOT EXISTS idx_vmm_sessions_project_session
ON vmm_sessions(project_id, session_key);

CREATE TABLE IF NOT EXISTS vmm_turn_records (
    id                 BIGINT PRIMARY KEY,
    session_id         BIGINT  NOT NULL,
    project_id         BIGINT  NOT NULL,
    dehydrated_content JSON    NOT NULL,
    dehydrated_budget  INTEGER NOT NULL DEFAULT 0,
    extracted_status   TINYINT NOT NULL DEFAULT 0,
    created_timestamp  BIGINT  NOT NULL,
    updated_timestamp  BIGINT  NOT NULL,
    FOREIGN KEY(session_id) REFERENCES vmm_sessions(id),
    FOREIGN KEY(project_id) REFERENCES vmm_projects(id)
);

CREATE INDEX IF NOT EXISTS idx_vmm_turn_records_session
ON vmm_turn_records(session_id, id);

CREATE INDEX IF NOT EXISTS idx_vmm_turn_records_project_status
ON vmm_turn_records(project_id, extracted_status, id);

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
