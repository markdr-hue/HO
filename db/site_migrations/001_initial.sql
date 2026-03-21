-- Site-level schema: all per-site tables (no site_id columns).
-- Each site gets its own SQLite database at data/sites/{id}/site.db.

-- Pages and versioning
CREATE TABLE IF NOT EXISTS ho_pages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    path TEXT NOT NULL UNIQUE,
    title TEXT,
    content TEXT,
    template TEXT,
    status TEXT DEFAULT 'published',
    metadata TEXT DEFAULT '{}',
    layout TEXT DEFAULT NULL,
    assets TEXT DEFAULT NULL,
    is_deleted BOOLEAN DEFAULT 0,
    deleted_at DATETIME,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS ho_page_versions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    page_id INTEGER NOT NULL,
    path TEXT NOT NULL,
    title TEXT,
    content TEXT,
    template TEXT,
    status TEXT,
    metadata TEXT,
    version_number INTEGER NOT NULL,
    changed_by TEXT NOT NULL DEFAULT 'brain',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_ho_page_versions ON ho_page_versions(page_id, version_number DESC);
CREATE INDEX IF NOT EXISTS idx_ho_page_versions_date ON ho_page_versions(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_ho_page_versions_page ON ho_page_versions(page_id);

-- Brain-created assets (CSS, JS, images)
CREATE TABLE IF NOT EXISTS ho_assets (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    filename TEXT NOT NULL UNIQUE,
    content_type TEXT,
    size INTEGER,
    storage_path TEXT NOT NULL,
    alt_text TEXT,
    scope TEXT NOT NULL DEFAULT 'global',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- User-uploaded files
CREATE TABLE IF NOT EXISTS ho_files (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    filename TEXT NOT NULL UNIQUE,
    content_type TEXT,
    size INTEGER,
    storage_path TEXT NOT NULL,
    description TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_files_date ON ho_files(created_at DESC);

-- Brain event log
CREATE TABLE IF NOT EXISTS ho_brain_log (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    event_type TEXT NOT NULL,
    summary TEXT,
    details TEXT,
    tokens_used INTEGER DEFAULT 0,
    model TEXT,
    duration_ms INTEGER,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_ho_brain_log_date ON ho_brain_log(created_at);
CREATE INDEX IF NOT EXISTS idx_ho_brain_log_event ON ho_brain_log(event_type);

-- Key-value memory store
CREATE TABLE IF NOT EXISTS ho_memory (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    key TEXT NOT NULL UNIQUE,
    value TEXT NOT NULL,
    category TEXT DEFAULT 'general',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Chat messages (brain + user sessions)
CREATE TABLE IF NOT EXISTS ho_chat_messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL,
    role TEXT NOT NULL,
    content TEXT NOT NULL,
    tool_calls TEXT,
    tool_call_id TEXT,
    metadata TEXT DEFAULT '{}',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_chat_session ON ho_chat_messages(session_id, created_at);

-- Questions and answers (human-in-the-loop)
CREATE TABLE IF NOT EXISTS ho_questions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    question TEXT NOT NULL,
    context TEXT,
    options TEXT,
    urgency TEXT DEFAULT 'normal',
    status TEXT DEFAULT 'pending',
    type TEXT DEFAULT 'text',
    secret_name TEXT,
    fields TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_questions_status ON ho_questions(status);

CREATE TABLE IF NOT EXISTS ho_answers (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    question_id INTEGER NOT NULL REFERENCES ho_questions(id) ON DELETE CASCADE,
    answer TEXT NOT NULL,
    answered_by INTEGER,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Dynamic tables registry (LLM-created schemas)
CREATE TABLE IF NOT EXISTS ho_dynamic_tables (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    table_name TEXT NOT NULL UNIQUE,
    schema_def TEXT NOT NULL,
    secure_columns TEXT DEFAULT '{}',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- API endpoints
CREATE TABLE IF NOT EXISTS ho_api_endpoints (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    path TEXT NOT NULL UNIQUE,
    table_name TEXT NOT NULL,
    methods TEXT DEFAULT '["GET","POST"]',
    public_columns TEXT,
    requires_auth BOOLEAN DEFAULT 0,
    public_read BOOLEAN DEFAULT 0,
    required_role TEXT DEFAULT NULL,
    rate_limit INTEGER DEFAULT 60,
    cors_origins TEXT DEFAULT '',
    cors_methods TEXT DEFAULT '',
    cors_headers TEXT DEFAULT '',
    owner_column TEXT DEFAULT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Auth endpoints
CREATE TABLE IF NOT EXISTS ho_auth_endpoints (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    table_name TEXT NOT NULL,
    path TEXT NOT NULL UNIQUE,
    username_column TEXT NOT NULL DEFAULT 'email',
    password_column TEXT NOT NULL DEFAULT 'password',
    public_columns TEXT DEFAULT '[]',
    jwt_expiry_hours INTEGER DEFAULT 24,
    default_role TEXT NOT NULL DEFAULT 'user',
    role_column TEXT NOT NULL DEFAULT 'role',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- OAuth providers (social login)
CREATE TABLE IF NOT EXISTS ho_oauth_providers (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL,
    client_id TEXT NOT NULL,
    client_secret_name TEXT NOT NULL,
    authorize_url TEXT NOT NULL,
    token_url TEXT NOT NULL,
    userinfo_url TEXT NOT NULL,
    scopes TEXT NOT NULL DEFAULT 'openid email profile',
    username_field TEXT NOT NULL DEFAULT 'email',
    auth_endpoint_path TEXT NOT NULL,
    is_enabled BOOLEAN DEFAULT 1,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Webhooks
CREATE TABLE IF NOT EXISTS ho_webhooks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    secret TEXT,
    url TEXT,
    direction TEXT DEFAULT 'incoming',
    is_enabled BOOLEAN DEFAULT 1,
    last_triggered DATETIME,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS ho_webhook_subscriptions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    webhook_id INTEGER NOT NULL REFERENCES ho_webhooks(id) ON DELETE CASCADE,
    event_type TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(webhook_id, event_type)
);

CREATE TABLE IF NOT EXISTS ho_webhook_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    webhook_id INTEGER NOT NULL REFERENCES ho_webhooks(id) ON DELETE CASCADE,
    direction TEXT NOT NULL,
    event_type TEXT,
    payload TEXT,
    status_code INTEGER,
    response TEXT,
    success BOOLEAN DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_ho_webhook_logs_date ON ho_webhook_logs(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_webhook_subs_webhook ON ho_webhook_subscriptions(webhook_id);

-- Analytics (page views)
CREATE TABLE IF NOT EXISTS ho_analytics (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    page_path TEXT NOT NULL,
    visitor_hash TEXT,
    referrer TEXT,
    user_agent TEXT,
    country TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_analytics_date ON ho_analytics(created_at);
CREATE INDEX IF NOT EXISTS idx_analytics_page_path ON ho_analytics(page_path, created_at DESC);

-- Secrets (encrypted key-value)
CREATE TABLE IF NOT EXISTS ho_secrets (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    value_encrypted TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Scheduled tasks
CREATE TABLE IF NOT EXISTS ho_scheduled_tasks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    description TEXT,
    cron_expression TEXT,
    interval_seconds INTEGER,
    prompt TEXT,
    native_action TEXT DEFAULT '',
    is_enabled BOOLEAN DEFAULT 1,
    last_run DATETIME,
    next_run DATETIME,
    run_count INTEGER DEFAULT 0,
    error_count INTEGER DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS ho_task_runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id INTEGER NOT NULL REFERENCES ho_scheduled_tasks(id) ON DELETE CASCADE,
    status TEXT NOT NULL,
    result TEXT,
    error_message TEXT,
    started_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME
);

CREATE INDEX IF NOT EXISTS idx_ho_task_runs_task ON ho_task_runs(task_id, started_at DESC);

-- Activity log
CREATE TABLE IF NOT EXISTS ho_activity_log (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    event_type TEXT NOT NULL,
    summary TEXT,
    details TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_ho_activity_log_date ON ho_activity_log(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_ho_activity_log_type ON ho_activity_log(event_type, created_at DESC);

-- Approval rules
CREATE TABLE IF NOT EXISTS ho_approval_rules (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    tool_name TEXT NOT NULL UNIQUE,
    requires_approval BOOLEAN DEFAULT 1,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Site snapshots
CREATE TABLE IF NOT EXISTS ho_site_snapshots (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    snapshot_data TEXT NOT NULL,
    created_by TEXT NOT NULL DEFAULT 'brain',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Service providers (external API integrations)
CREATE TABLE IF NOT EXISTS ho_service_providers (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    base_url TEXT NOT NULL,
    auth_type TEXT DEFAULT 'bearer',
    auth_header TEXT DEFAULT 'Authorization',
    auth_prefix TEXT DEFAULT 'Bearer',
    secret_name TEXT,
    description TEXT DEFAULT '',
    api_docs TEXT DEFAULT '',
    is_enabled BOOLEAN DEFAULT 1,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Detailed LLM request/response log
CREATE TABLE IF NOT EXISTS ho_llm_log (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    source TEXT NOT NULL,
    session_id TEXT NOT NULL DEFAULT '',
    iteration INTEGER NOT NULL DEFAULT 0,
    model TEXT NOT NULL,
    provider_type TEXT NOT NULL DEFAULT '',
    request_messages TEXT,
    request_system TEXT,
    request_tools TEXT,
    request_max_tokens INTEGER,
    response_content TEXT,
    response_tool_calls TEXT,
    response_stop_reason TEXT,
    input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    duration_ms INTEGER NOT NULL DEFAULT 0,
    error_message TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_ho_llm_log_date ON ho_llm_log(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_ho_llm_log_source ON ho_llm_log(source, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_ho_llm_log_session ON ho_llm_log(session_id, created_at DESC);

-- Server-side layout system
CREATE TABLE IF NOT EXISTS ho_layouts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    head_content TEXT DEFAULT '',
    template TEXT DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS ho_layout_versions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    layout_id INTEGER NOT NULL,
    name TEXT NOT NULL,
    head_content TEXT DEFAULT '',
    template TEXT DEFAULT '',
    version_number INTEGER NOT NULL,
    changed_by TEXT NOT NULL DEFAULT 'brain',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_ho_layout_versions ON ho_layout_versions(layout_id, version_number DESC);

-- Pipeline state: singleton row tracking current build progress
CREATE TABLE IF NOT EXISTS ho_pipeline_state (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    stage TEXT NOT NULL DEFAULT 'PLAN',
    plan_json TEXT,
    tool_calls_completed INTEGER DEFAULT 0,
    update_description TEXT,
    error_count INTEGER DEFAULT 0,
    last_error TEXT,
    paused BOOLEAN DEFAULT 0,
    pause_reason TEXT,
    checkpoint_messages TEXT,
    total_build_tokens INTEGER DEFAULT 0,
    started_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

INSERT OR IGNORE INTO ho_pipeline_state (id) VALUES (1);

-- Stage execution log
CREATE TABLE IF NOT EXISTS ho_stage_log (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    stage TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'started',
    input_tokens INTEGER DEFAULT 0,
    output_tokens INTEGER DEFAULT 0,
    tool_calls INTEGER DEFAULT 0,
    duration_ms INTEGER DEFAULT 0,
    error_message TEXT,
    started_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME
);

CREATE INDEX IF NOT EXISTS idx_ho_stage_log_stage ON ho_stage_log(stage, started_at DESC);

-- File version history for text assets (CSS, JS, SVG, etc.)
CREATE TABLE IF NOT EXISTS ho_file_versions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    storage_type TEXT NOT NULL,
    filename TEXT NOT NULL,
    content TEXT NOT NULL,
    content_type TEXT,
    size INTEGER,
    version_number INTEGER NOT NULL,
    changed_by TEXT NOT NULL DEFAULT 'brain',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_ho_file_versions_lookup
    ON ho_file_versions (storage_type, filename, version_number DESC);

-- Upload endpoints (file upload routes)
CREATE TABLE IF NOT EXISTS ho_upload_endpoints (
    id INTEGER PRIMARY KEY,
    path TEXT UNIQUE NOT NULL,
    allowed_types TEXT,
    max_size_mb INTEGER DEFAULT 5,
    requires_auth BOOLEAN DEFAULT 0,
    table_name TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- SSE stream endpoints
CREATE TABLE IF NOT EXISTS ho_stream_endpoints (
    id INTEGER PRIMARY KEY,
    path TEXT UNIQUE NOT NULL,
    event_types TEXT,
    requires_auth BOOLEAN DEFAULT 0,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- LLM chat/completion endpoints
CREATE TABLE IF NOT EXISTS ho_llm_endpoints (
    id INTEGER PRIMARY KEY,
    path TEXT UNIQUE NOT NULL,
    system_prompt TEXT NOT NULL DEFAULT '',
    model_id TEXT DEFAULT '',
    max_tokens INTEGER DEFAULT 4096,
    temperature REAL DEFAULT 0.7,
    max_history INTEGER DEFAULT 20,
    rate_limit INTEGER DEFAULT 10,
    requires_auth BOOLEAN DEFAULT 0,
    cors_origins TEXT DEFAULT '',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- URL redirects (301/302)
CREATE TABLE IF NOT EXISTS ho_redirects (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    source_path TEXT NOT NULL UNIQUE,
    target_path TEXT NOT NULL,
    status_code INTEGER NOT NULL DEFAULT 301,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_redirects_source ON ho_redirects(source_path);

-- WebSocket endpoints
CREATE TABLE IF NOT EXISTS ho_ws_endpoints (
    id INTEGER PRIMARY KEY,
    path TEXT UNIQUE NOT NULL,
    write_to_table TEXT DEFAULT '',
    room_column TEXT DEFAULT '',
    requires_auth BOOLEAN DEFAULT 0,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Subscription plans (recurring billing)
CREATE TABLE IF NOT EXISTS ho_subscription_plans (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    external_id TEXT DEFAULT '',
    amount INTEGER NOT NULL,
    currency TEXT NOT NULL DEFAULT 'usd',
    interval TEXT NOT NULL DEFAULT 'month',
    trial_days INTEGER DEFAULT 0,
    metadata TEXT DEFAULT '{}',
    is_active BOOLEAN DEFAULT 1,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Subscriptions (user-to-plan bindings)
CREATE TABLE IF NOT EXISTS ho_subscriptions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER,
    plan_id INTEGER REFERENCES ho_subscription_plans(id),
    external_id TEXT DEFAULT '',
    status TEXT NOT NULL DEFAULT 'active',
    current_period_start DATETIME,
    current_period_end DATETIME,
    canceled_at DATETIME,
    metadata TEXT DEFAULT '{}',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Server-side actions (event-driven hooks: send email, HTTP request, insert/update data)
CREATE TABLE IF NOT EXISTS ho_actions (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    event_type TEXT NOT NULL,
    event_filter TEXT,        -- JSON: {"column":"value"} to match against event payload
    action_type TEXT NOT NULL, -- send_email, http_request, insert_data, update_data
    action_config TEXT NOT NULL, -- JSON config with {{template}} variables from event payload
    is_enabled BOOLEAN DEFAULT 1,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
