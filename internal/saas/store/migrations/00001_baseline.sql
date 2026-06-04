-- 00001_baseline.sql — frozen baseline of the QuantLab SaaS schema.
--
-- GENERATED, then hand-verified: produced from `pg_dump --schema-only` of a
-- database freshly migrated by store.NewDB (GORM AutoMigrate + the raw
-- TimescaleDB/partial-index DDL in db.go), with three edits:
--   1. pg_dump preamble / \restrict / OWNER TO / extension-comment noise stripped;
--   2. the two create_hypertable-generated default indexes
--      (klines_open_time_idx, portfolio_states_now_ms_idx) removed;
--   3. two SELECT create_hypertable(...) calls injected (args identical to db.go).
--
-- Fidelity is enforced by migrate_drift_test.go: a goose-built DB and an
-- AutoMigrate-built DB must produce a byte-identical `pg_dump --schema-only`.
-- Squawk false-positives on this from-empty file are silenced per-file in
-- .squawk.toml (populated-table rules don't apply to baseline create-from-zero).

-- +goose Up

--
--

--
-- Name: timescaledb; Type: EXTENSION; Schema: -; Owner: -
--

CREATE EXTENSION IF NOT EXISTS timescaledb WITH SCHEMA public;

--
-- Name: EXTENSION timescaledb; Type: COMMENT; Schema: -; Owner: 
--

--
-- Name: agent_errors; Type: TABLE; Schema: public; Owner: ql
--

CREATE TABLE public.agent_errors (
    id bigint NOT NULL,
    created_at timestamp with time zone,
    account_id character varying(64) NOT NULL,
    instance_id character varying(32),
    code character varying(64) NOT NULL,
    message text,
    occurred_at_ms bigint,
    reported_at_ms bigint
);

--
-- Name: agent_errors_id_seq; Type: SEQUENCE; Schema: public; Owner: ql
--

CREATE SEQUENCE public.agent_errors_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: agent_errors_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: ql
--

ALTER SEQUENCE public.agent_errors_id_seq OWNED BY public.agent_errors.id;

--
-- Name: agent_tokens; Type: TABLE; Schema: public; Owner: ql
--

CREATE TABLE public.agent_tokens (
    id bigint NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    agent_id character varying(32),
    account_id character varying(32) NOT NULL,
    token_hash character varying(60) NOT NULL,
    label character varying(64),
    last_seen_at timestamp with time zone,
    revoked_at timestamp with time zone
);

--
-- Name: agent_tokens_id_seq; Type: SEQUENCE; Schema: public; Owner: ql
--

CREATE SEQUENCE public.agent_tokens_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: agent_tokens_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: ql
--

ALTER SEQUENCE public.agent_tokens_id_seq OWNED BY public.agent_tokens.id;

--
-- Name: audit_logs; Type: TABLE; Schema: public; Owner: ql
--

CREATE TABLE public.audit_logs (
    id bigint NOT NULL,
    created_at timestamp with time zone,
    now_ms bigint,
    actor character varying(64) NOT NULL,
    action character varying(48) NOT NULL,
    subject character varying(128) NOT NULL,
    data_json jsonb
);

--
-- Name: audit_logs_id_seq; Type: SEQUENCE; Schema: public; Owner: ql
--

CREATE SEQUENCE public.audit_logs_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: audit_logs_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: ql
--

ALTER SEQUENCE public.audit_logs_id_seq OWNED BY public.audit_logs.id;

--
-- Name: champion_histories; Type: TABLE; Schema: public; Owner: ql
--

CREATE TABLE public.champion_histories (
    id bigint NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    deleted_at timestamp with time zone,
    strategy_id character varying(64),
    pair character varying(32),
    challenger_id character varying(64),
    promoted_at timestamp with time zone,
    retired_at timestamp with time zone,
    retired_by character varying(64),
    retire_note text
);

--
-- Name: champion_histories_id_seq; Type: SEQUENCE; Schema: public; Owner: ql
--

CREATE SEQUENCE public.champion_histories_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: champion_histories_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: ql
--

ALTER SEQUENCE public.champion_histories_id_seq OWNED BY public.champion_histories.id;

--
-- Name: evaluation_traces; Type: TABLE; Schema: public; Owner: ql
--

CREATE TABLE public.evaluation_traces (
    id bigint NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    deleted_at timestamp with time zone,
    task_id character varying(64),
    generation bigint,
    individual_idx bigint,
    gene_json jsonb,
    score_total numeric,
    score_raw numeric,
    consistency_penalty numeric,
    fatal boolean,
    fatal_reason text,
    window_scores_json jsonb,
    fingerprint character varying(64)
);

--
-- Name: evaluation_traces_id_seq; Type: SEQUENCE; Schema: public; Owner: ql
--

CREATE SEQUENCE public.evaluation_traces_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: evaluation_traces_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: ql
--

ALTER SEQUENCE public.evaluation_traces_id_seq OWNED BY public.evaluation_traces.id;

--
-- Name: evolution_tasks; Type: TABLE; Schema: public; Owner: ql
--

CREATE TABLE public.evolution_tasks (
    id bigint NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    deleted_at timestamp with time zone,
    task_id character varying(64),
    strategy_id character varying(64),
    pair character varying(32),
    "interval" character varying(8),
    status character varying(16),
    current_generation bigint,
    requested_taker_fee_bps numeric,
    requested_slippage_bps numeric,
    test_mode boolean,
    spawn_mode character varying(16),
    oos_days bigint,
    fatal_audit_sample_rate numeric,
    epoch_seed bigint,
    started_at timestamp with time zone,
    finished_at timestamp with time zone,
    failure_reason text,
    challenger_id character varying(64),
    best_score numeric
);

--
-- Name: evolution_tasks_id_seq; Type: SEQUENCE; Schema: public; Owner: ql
--

CREATE SEQUENCE public.evolution_tasks_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: evolution_tasks_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: ql
--

ALTER SEQUENCE public.evolution_tasks_id_seq OWNED BY public.evolution_tasks.id;

--
-- Name: gene_records; Type: TABLE; Schema: public; Owner: ql
--

CREATE TABLE public.gene_records (
    id bigint NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    deleted_at timestamp with time zone,
    challenger_id character varying(64),
    strategy_id character varying(64),
    pair character varying(32),
    score_total numeric,
    score_raw numeric,
    consistency_penalty numeric,
    window_scores_json jsonb,
    window_alpha_monthly_json jsonb,
    window_alpha_weekly_json jsonb,
    oos_alpha_monthly numeric,
    oos_alpha_weekly numeric,
    dsr numeric,
    dsr_trials_n bigint,
    dsr_trials_var numeric,
    epoch_seed bigint,
    data_version character varying(64),
    engine_version character varying(64),
    strategy_version character varying(64),
    schema_version character varying(16),
    fitness_version character varying(32),
    fingerprint_version character varying(16),
    hardware_signature character varying(128),
    go_version character varying(32),
    build_id character varying(64),
    plan_hash character varying(64),
    bars_hash character varying(64),
    taker_fee_bps numeric,
    slippage_bps numeric,
    test_mode boolean,
    sbb_block_length bigint,
    decision_status character varying(16),
    decision_note text,
    reviewed_at_ts bigint,
    reviewed_by character varying(64),
    full_package_json jsonb
);

--
-- Name: gene_records_id_seq; Type: SEQUENCE; Schema: public; Owner: ql
--

CREATE SEQUENCE public.gene_records_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: gene_records_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: ql
--

ALTER SEQUENCE public.gene_records_id_seq OWNED BY public.gene_records.id;

--
-- Name: import_jobs; Type: TABLE; Schema: public; Owner: ql
--

CREATE TABLE public.import_jobs (
    id bigint NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    deleted_at timestamp with time zone,
    job_id character varying(64),
    symbol character varying(16),
    "interval" character varying(8),
    start_ms bigint,
    end_ms bigint,
    status character varying(16),
    months_done bigint,
    months_total bigint,
    rows_inserted bigint,
    gaps_detected bigint,
    cancel_requested boolean,
    requested_by character varying(64),
    started_at timestamp with time zone,
    finished_at timestamp with time zone,
    failure_reason text
);

--
-- Name: import_jobs_id_seq; Type: SEQUENCE; Schema: public; Owner: ql
--

CREATE SEQUENCE public.import_jobs_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: import_jobs_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: ql
--

ALTER SEQUENCE public.import_jobs_id_seq OWNED BY public.import_jobs.id;

--
-- Name: kline_gaps; Type: TABLE; Schema: public; Owner: ql
--

CREATE TABLE public.kline_gaps (
    id bigint NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    deleted_at timestamp with time zone,
    symbol character varying(16),
    "interval" character varying(8),
    gap_start_ms bigint,
    gap_end_ms bigint,
    detected_at timestamp with time zone
);

--
-- Name: kline_gaps_id_seq; Type: SEQUENCE; Schema: public; Owner: ql
--

CREATE SEQUENCE public.kline_gaps_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: kline_gaps_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: ql
--

ALTER SEQUENCE public.kline_gaps_id_seq OWNED BY public.kline_gaps.id;

--
-- Name: klines; Type: TABLE; Schema: public; Owner: ql
--

CREATE TABLE public.klines (
    symbol character varying(16) NOT NULL,
    "interval" character varying(8) NOT NULL,
    open_time bigint NOT NULL,
    open numeric,
    high numeric,
    low numeric,
    close numeric,
    volume numeric,
    quote_volume numeric,
    num_trades integer,
    source character varying(16) DEFAULT 'binance.vision'::character varying
);

--
-- Name: portfolio_states; Type: TABLE; Schema: public; Owner: ql
--

CREATE TABLE public.portfolio_states (
    instance_id character varying(32) NOT NULL,
    now_ms bigint NOT NULL,
    created_at timestamp with time zone,
    dead_btc numeric,
    float_btc numeric,
    cold_sealed_btc numeric,
    usdt numeric,
    last_processed_bar_time bigint,
    last_applied_exec_id bigint
);

--
-- Name: reconciliation_discrepancies; Type: TABLE; Schema: public; Owner: ql
--

CREATE TABLE public.reconciliation_discrepancies (
    id bigint NOT NULL,
    created_at timestamp with time zone,
    account_id character varying(64) NOT NULL,
    instance_id character varying(32),
    asset character varying(16) NOT NULL,
    expected_amount numeric,
    actual_amount numeric,
    diff_amount numeric,
    drift_bps numeric,
    reported_at_ms bigint,
    detected_at_ms bigint
);

--
-- Name: reconciliation_discrepancies_id_seq; Type: SEQUENCE; Schema: public; Owner: ql
--

CREATE SEQUENCE public.reconciliation_discrepancies_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: reconciliation_discrepancies_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: ql
--

ALTER SEQUENCE public.reconciliation_discrepancies_id_seq OWNED BY public.reconciliation_discrepancies.id;

--
-- Name: runtime_states; Type: TABLE; Schema: public; Owner: ql
--

CREATE TABLE public.runtime_states (
    id bigint NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    instance_id character varying(32),
    now_ms bigint NOT NULL,
    state_json jsonb NOT NULL
);

--
-- Name: runtime_states_id_seq; Type: SEQUENCE; Schema: public; Owner: ql
--

CREATE SEQUENCE public.runtime_states_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: runtime_states_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: ql
--

ALTER SEQUENCE public.runtime_states_id_seq OWNED BY public.runtime_states.id;

--
-- Name: sharpe_banks; Type: TABLE; Schema: public; Owner: ql
--

CREATE TABLE public.sharpe_banks (
    id bigint NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    deleted_at timestamp with time zone,
    strategy_id character varying(64),
    pair_id character varying(32),
    challenger_id character varying(64),
    observed_sharpe numeric,
    backtest_horizon_t bigint,
    skew numeric,
    kurtosis numeric,
    spawn_mode character varying(16),
    fingerprint_distance_to_parent numeric
);

--
-- Name: sharpe_banks_id_seq; Type: SEQUENCE; Schema: public; Owner: ql
--

CREATE SEQUENCE public.sharpe_banks_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: sharpe_banks_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: ql
--

ALTER SEQUENCE public.sharpe_banks_id_seq OWNED BY public.sharpe_banks.id;

--
-- Name: spot_executions; Type: TABLE; Schema: public; Owner: ql
--

CREATE TABLE public.spot_executions (
    id bigint NOT NULL,
    created_at timestamp with time zone,
    client_order_id character varying(32) NOT NULL,
    exchange_order_id character varying(64) NOT NULL,
    fill_quantity numeric NOT NULL,
    fill_price numeric NOT NULL,
    fill_fee_asset character varying(16) NOT NULL,
    fill_fee_amount numeric NOT NULL,
    filled_at_exchange_ms bigint NOT NULL,
    actual_slippage_bps numeric,
    trade_id bigint
);

--
-- Name: spot_executions_id_seq; Type: SEQUENCE; Schema: public; Owner: ql
--

CREATE SEQUENCE public.spot_executions_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: spot_executions_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: ql
--

ALTER SEQUENCE public.spot_executions_id_seq OWNED BY public.spot_executions.id;

--
-- Name: spot_lots; Type: TABLE; Schema: public; Owner: ql
--

CREATE TABLE public.spot_lots (
    id bigint NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    lot_id character varying(32),
    instance_id character varying(32) NOT NULL,
    symbol character varying(16) NOT NULL,
    kind character varying(8) NOT NULL,
    open_ms bigint NOT NULL,
    close_ms bigint,
    quantity numeric NOT NULL,
    entry_price numeric NOT NULL,
    entry_trade_id character varying(32)
);

--
-- Name: spot_lots_id_seq; Type: SEQUENCE; Schema: public; Owner: ql
--

CREATE SEQUENCE public.spot_lots_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: spot_lots_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: ql
--

ALTER SEQUENCE public.spot_lots_id_seq OWNED BY public.spot_lots.id;

--
-- Name: strategy_instances; Type: TABLE; Schema: public; Owner: ql
--

CREATE TABLE public.strategy_instances (
    id bigint NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    instance_id character varying(32),
    strategy_id character varying(64) NOT NULL,
    pair character varying(32) NOT NULL,
    account_id character varying(64) NOT NULL,
    owner_user_id bigint NOT NULL,
    status character varying(16) DEFAULT 'idle'::character varying,
    active_champ_id character varying(64),
    last_tick_wall_time timestamp with time zone,
    funded_at_ms bigint
);

--
-- Name: strategy_instances_id_seq; Type: SEQUENCE; Schema: public; Owner: ql
--

CREATE SEQUENCE public.strategy_instances_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: strategy_instances_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: ql
--

ALTER SEQUENCE public.strategy_instances_id_seq OWNED BY public.strategy_instances.id;

--
-- Name: strategy_templates; Type: TABLE; Schema: public; Owner: ql
--

CREATE TABLE public.strategy_templates (
    id bigint NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    strategy_id character varying(64),
    display_name character varying(128) NOT NULL,
    version character varying(32) NOT NULL,
    description text,
    active boolean DEFAULT true NOT NULL,
    chromosome_schema_json jsonb
);

--
-- Name: strategy_templates_id_seq; Type: SEQUENCE; Schema: public; Owner: ql
--

CREATE SEQUENCE public.strategy_templates_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: strategy_templates_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: ql
--

ALTER SEQUENCE public.strategy_templates_id_seq OWNED BY public.strategy_templates.id;

--
-- Name: trade_records; Type: TABLE; Schema: public; Owner: ql
--

CREATE TABLE public.trade_records (
    id bigint NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    client_order_id character varying(32),
    instance_id character varying(32) NOT NULL,
    symbol character varying(16) NOT NULL,
    side character varying(8) NOT NULL,
    order_type character varying(16) NOT NULL,
    quantity_usd numeric NOT NULL,
    limit_price numeric,
    now_ms_at_saa_s bigint NOT NULL,
    valid_until_ms bigint NOT NULL,
    status character varying(16) DEFAULT 'pending'::character varying,
    lot_id character varying(32)
);

--
-- Name: trade_records_id_seq; Type: SEQUENCE; Schema: public; Owner: ql
--

CREATE SEQUENCE public.trade_records_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: trade_records_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: ql
--

ALTER SEQUENCE public.trade_records_id_seq OWNED BY public.trade_records.id;

--
-- Name: users; Type: TABLE; Schema: public; Owner: ql
--

CREATE TABLE public.users (
    id bigint NOT NULL,
    user_id character varying(32),
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    email character varying(255),
    password_hash character varying(255) NOT NULL,
    role character varying(16) NOT NULL,
    display_name character varying(128),
    active boolean DEFAULT true NOT NULL,
    last_login_at timestamp with time zone
);

--
-- Name: users_id_seq; Type: SEQUENCE; Schema: public; Owner: ql
--

CREATE SEQUENCE public.users_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: users_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: ql
--

ALTER SEQUENCE public.users_id_seq OWNED BY public.users.id;

--
-- Name: agent_errors id; Type: DEFAULT; Schema: public; Owner: ql
--

ALTER TABLE ONLY public.agent_errors ALTER COLUMN id SET DEFAULT nextval('public.agent_errors_id_seq'::regclass);

--
-- Name: agent_tokens id; Type: DEFAULT; Schema: public; Owner: ql
--

ALTER TABLE ONLY public.agent_tokens ALTER COLUMN id SET DEFAULT nextval('public.agent_tokens_id_seq'::regclass);

--
-- Name: audit_logs id; Type: DEFAULT; Schema: public; Owner: ql
--

ALTER TABLE ONLY public.audit_logs ALTER COLUMN id SET DEFAULT nextval('public.audit_logs_id_seq'::regclass);

--
-- Name: champion_histories id; Type: DEFAULT; Schema: public; Owner: ql
--

ALTER TABLE ONLY public.champion_histories ALTER COLUMN id SET DEFAULT nextval('public.champion_histories_id_seq'::regclass);

--
-- Name: evaluation_traces id; Type: DEFAULT; Schema: public; Owner: ql
--

ALTER TABLE ONLY public.evaluation_traces ALTER COLUMN id SET DEFAULT nextval('public.evaluation_traces_id_seq'::regclass);

--
-- Name: evolution_tasks id; Type: DEFAULT; Schema: public; Owner: ql
--

ALTER TABLE ONLY public.evolution_tasks ALTER COLUMN id SET DEFAULT nextval('public.evolution_tasks_id_seq'::regclass);

--
-- Name: gene_records id; Type: DEFAULT; Schema: public; Owner: ql
--

ALTER TABLE ONLY public.gene_records ALTER COLUMN id SET DEFAULT nextval('public.gene_records_id_seq'::regclass);

--
-- Name: import_jobs id; Type: DEFAULT; Schema: public; Owner: ql
--

ALTER TABLE ONLY public.import_jobs ALTER COLUMN id SET DEFAULT nextval('public.import_jobs_id_seq'::regclass);

--
-- Name: kline_gaps id; Type: DEFAULT; Schema: public; Owner: ql
--

ALTER TABLE ONLY public.kline_gaps ALTER COLUMN id SET DEFAULT nextval('public.kline_gaps_id_seq'::regclass);

--
-- Name: reconciliation_discrepancies id; Type: DEFAULT; Schema: public; Owner: ql
--

ALTER TABLE ONLY public.reconciliation_discrepancies ALTER COLUMN id SET DEFAULT nextval('public.reconciliation_discrepancies_id_seq'::regclass);

--
-- Name: runtime_states id; Type: DEFAULT; Schema: public; Owner: ql
--

ALTER TABLE ONLY public.runtime_states ALTER COLUMN id SET DEFAULT nextval('public.runtime_states_id_seq'::regclass);

--
-- Name: sharpe_banks id; Type: DEFAULT; Schema: public; Owner: ql
--

ALTER TABLE ONLY public.sharpe_banks ALTER COLUMN id SET DEFAULT nextval('public.sharpe_banks_id_seq'::regclass);

--
-- Name: spot_executions id; Type: DEFAULT; Schema: public; Owner: ql
--

ALTER TABLE ONLY public.spot_executions ALTER COLUMN id SET DEFAULT nextval('public.spot_executions_id_seq'::regclass);

--
-- Name: spot_lots id; Type: DEFAULT; Schema: public; Owner: ql
--

ALTER TABLE ONLY public.spot_lots ALTER COLUMN id SET DEFAULT nextval('public.spot_lots_id_seq'::regclass);

--
-- Name: strategy_instances id; Type: DEFAULT; Schema: public; Owner: ql
--

ALTER TABLE ONLY public.strategy_instances ALTER COLUMN id SET DEFAULT nextval('public.strategy_instances_id_seq'::regclass);

--
-- Name: strategy_templates id; Type: DEFAULT; Schema: public; Owner: ql
--

ALTER TABLE ONLY public.strategy_templates ALTER COLUMN id SET DEFAULT nextval('public.strategy_templates_id_seq'::regclass);

--
-- Name: trade_records id; Type: DEFAULT; Schema: public; Owner: ql
--

ALTER TABLE ONLY public.trade_records ALTER COLUMN id SET DEFAULT nextval('public.trade_records_id_seq'::regclass);

--
-- Name: users id; Type: DEFAULT; Schema: public; Owner: ql
--

ALTER TABLE ONLY public.users ALTER COLUMN id SET DEFAULT nextval('public.users_id_seq'::regclass);

--
-- Name: agent_errors agent_errors_pkey; Type: CONSTRAINT; Schema: public; Owner: ql
--

ALTER TABLE ONLY public.agent_errors
    ADD CONSTRAINT agent_errors_pkey PRIMARY KEY (id);

--
-- Name: agent_tokens agent_tokens_pkey; Type: CONSTRAINT; Schema: public; Owner: ql
--

ALTER TABLE ONLY public.agent_tokens
    ADD CONSTRAINT agent_tokens_pkey PRIMARY KEY (id);

--
-- Name: audit_logs audit_logs_pkey; Type: CONSTRAINT; Schema: public; Owner: ql
--

ALTER TABLE ONLY public.audit_logs
    ADD CONSTRAINT audit_logs_pkey PRIMARY KEY (id);

--
-- Name: champion_histories champion_histories_pkey; Type: CONSTRAINT; Schema: public; Owner: ql
--

ALTER TABLE ONLY public.champion_histories
    ADD CONSTRAINT champion_histories_pkey PRIMARY KEY (id);

--
-- Name: evaluation_traces evaluation_traces_pkey; Type: CONSTRAINT; Schema: public; Owner: ql
--

ALTER TABLE ONLY public.evaluation_traces
    ADD CONSTRAINT evaluation_traces_pkey PRIMARY KEY (id);

--
-- Name: evolution_tasks evolution_tasks_pkey; Type: CONSTRAINT; Schema: public; Owner: ql
--

ALTER TABLE ONLY public.evolution_tasks
    ADD CONSTRAINT evolution_tasks_pkey PRIMARY KEY (id);

--
-- Name: gene_records gene_records_pkey; Type: CONSTRAINT; Schema: public; Owner: ql
--

ALTER TABLE ONLY public.gene_records
    ADD CONSTRAINT gene_records_pkey PRIMARY KEY (id);

--
-- Name: import_jobs import_jobs_pkey; Type: CONSTRAINT; Schema: public; Owner: ql
--

ALTER TABLE ONLY public.import_jobs
    ADD CONSTRAINT import_jobs_pkey PRIMARY KEY (id);

--
-- Name: kline_gaps kline_gaps_pkey; Type: CONSTRAINT; Schema: public; Owner: ql
--

ALTER TABLE ONLY public.kline_gaps
    ADD CONSTRAINT kline_gaps_pkey PRIMARY KEY (id);

--
-- Name: klines klines_pkey; Type: CONSTRAINT; Schema: public; Owner: ql
--

ALTER TABLE ONLY public.klines
    ADD CONSTRAINT klines_pkey PRIMARY KEY (symbol, "interval", open_time);

--
-- Name: portfolio_states portfolio_states_pkey; Type: CONSTRAINT; Schema: public; Owner: ql
--

ALTER TABLE ONLY public.portfolio_states
    ADD CONSTRAINT portfolio_states_pkey PRIMARY KEY (instance_id, now_ms);

--
-- Name: reconciliation_discrepancies reconciliation_discrepancies_pkey; Type: CONSTRAINT; Schema: public; Owner: ql
--

ALTER TABLE ONLY public.reconciliation_discrepancies
    ADD CONSTRAINT reconciliation_discrepancies_pkey PRIMARY KEY (id);

--
-- Name: runtime_states runtime_states_pkey; Type: CONSTRAINT; Schema: public; Owner: ql
--

ALTER TABLE ONLY public.runtime_states
    ADD CONSTRAINT runtime_states_pkey PRIMARY KEY (id);

--
-- Name: sharpe_banks sharpe_banks_pkey; Type: CONSTRAINT; Schema: public; Owner: ql
--

ALTER TABLE ONLY public.sharpe_banks
    ADD CONSTRAINT sharpe_banks_pkey PRIMARY KEY (id);

--
-- Name: spot_executions spot_executions_pkey; Type: CONSTRAINT; Schema: public; Owner: ql
--

ALTER TABLE ONLY public.spot_executions
    ADD CONSTRAINT spot_executions_pkey PRIMARY KEY (id);

--
-- Name: spot_lots spot_lots_pkey; Type: CONSTRAINT; Schema: public; Owner: ql
--

ALTER TABLE ONLY public.spot_lots
    ADD CONSTRAINT spot_lots_pkey PRIMARY KEY (id);

--
-- Name: strategy_instances strategy_instances_pkey; Type: CONSTRAINT; Schema: public; Owner: ql
--

ALTER TABLE ONLY public.strategy_instances
    ADD CONSTRAINT strategy_instances_pkey PRIMARY KEY (id);

--
-- Name: strategy_templates strategy_templates_pkey; Type: CONSTRAINT; Schema: public; Owner: ql
--

ALTER TABLE ONLY public.strategy_templates
    ADD CONSTRAINT strategy_templates_pkey PRIMARY KEY (id);

--
-- Name: trade_records trade_records_pkey; Type: CONSTRAINT; Schema: public; Owner: ql
--

ALTER TABLE ONLY public.trade_records
    ADD CONSTRAINT trade_records_pkey PRIMARY KEY (id);

--
-- Name: users users_pkey; Type: CONSTRAINT; Schema: public; Owner: ql
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_pkey PRIMARY KEY (id);

--
-- Name: idx_agent_errors_account_id; Type: INDEX; Schema: public; Owner: ql
--

--
-- Convert klines + portfolio_states into TimescaleDB hypertables.
-- These calls replace the create_hypertable() invocations in
-- internal/saas/store/db.go (chunk intervals match exactly). They also
-- regenerate the default DESC time index (klines_open_time_idx /
-- portfolio_states_now_ms_idx), which is why those indexes are NOT
-- created explicitly above.
--

SELECT create_hypertable('klines', 'open_time',
    if_not_exists => TRUE,
    chunk_time_interval => 604800000::bigint);

SELECT create_hypertable('portfolio_states', 'now_ms',
    if_not_exists => TRUE,
    migrate_data => TRUE,
    chunk_time_interval => 2592000000::bigint);

CREATE INDEX idx_agent_errors_account_id ON public.agent_errors USING btree (account_id);

--
-- Name: idx_agent_errors_code; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_agent_errors_code ON public.agent_errors USING btree (code);

--
-- Name: idx_agent_errors_created_at; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_agent_errors_created_at ON public.agent_errors USING btree (created_at);

--
-- Name: idx_agent_errors_instance_id; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_agent_errors_instance_id ON public.agent_errors USING btree (instance_id);

--
-- Name: idx_agent_errors_occurred_at_ms; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_agent_errors_occurred_at_ms ON public.agent_errors USING btree (occurred_at_ms);

--
-- Name: idx_agent_tokens_account_id; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_agent_tokens_account_id ON public.agent_tokens USING btree (account_id);

--
-- Name: idx_agent_tokens_agent_id; Type: INDEX; Schema: public; Owner: ql
--

CREATE UNIQUE INDEX idx_agent_tokens_agent_id ON public.agent_tokens USING btree (agent_id);

--
-- Name: idx_agent_tokens_created_at; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_agent_tokens_created_at ON public.agent_tokens USING btree (created_at);

--
-- Name: idx_agent_tokens_revoked_at; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_agent_tokens_revoked_at ON public.agent_tokens USING btree (revoked_at);

--
-- Name: idx_audit_logs_action; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_audit_logs_action ON public.audit_logs USING btree (action);

--
-- Name: idx_audit_logs_actor; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_audit_logs_actor ON public.audit_logs USING btree (actor);

--
-- Name: idx_audit_logs_created_at; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_audit_logs_created_at ON public.audit_logs USING btree (created_at);

--
-- Name: idx_audit_logs_subject; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_audit_logs_subject ON public.audit_logs USING btree (subject);

--
-- Name: idx_champion_histories_challenger_id; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_champion_histories_challenger_id ON public.champion_histories USING btree (challenger_id);

--
-- Name: idx_champion_histories_deleted_at; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_champion_histories_deleted_at ON public.champion_histories USING btree (deleted_at);

--
-- Name: idx_champion_histories_pair; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_champion_histories_pair ON public.champion_histories USING btree (pair);

--
-- Name: idx_champion_histories_strategy_id; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_champion_histories_strategy_id ON public.champion_histories USING btree (strategy_id);

--
-- Name: idx_evaluation_traces_deleted_at; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_evaluation_traces_deleted_at ON public.evaluation_traces USING btree (deleted_at);

--
-- Name: idx_evaluation_traces_fatal; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_evaluation_traces_fatal ON public.evaluation_traces USING btree (fatal);

--
-- Name: idx_evaluation_traces_fingerprint; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_evaluation_traces_fingerprint ON public.evaluation_traces USING btree (fingerprint);

--
-- Name: idx_evolution_tasks_challenger_id; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_evolution_tasks_challenger_id ON public.evolution_tasks USING btree (challenger_id);

--
-- Name: idx_evolution_tasks_deleted_at; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_evolution_tasks_deleted_at ON public.evolution_tasks USING btree (deleted_at);

--
-- Name: idx_evolution_tasks_interval; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_evolution_tasks_interval ON public.evolution_tasks USING btree ("interval");

--
-- Name: idx_evolution_tasks_pair; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_evolution_tasks_pair ON public.evolution_tasks USING btree (pair);

--
-- Name: idx_evolution_tasks_status; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_evolution_tasks_status ON public.evolution_tasks USING btree (status);

--
-- Name: idx_evolution_tasks_strategy_id; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_evolution_tasks_strategy_id ON public.evolution_tasks USING btree (strategy_id);

--
-- Name: idx_evolution_tasks_task_id; Type: INDEX; Schema: public; Owner: ql
--

CREATE UNIQUE INDEX idx_evolution_tasks_task_id ON public.evolution_tasks USING btree (task_id);

--
-- Name: idx_gene_records_bars_hash; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_gene_records_bars_hash ON public.gene_records USING btree (bars_hash);

--
-- Name: idx_gene_records_challenger_id; Type: INDEX; Schema: public; Owner: ql
--

CREATE UNIQUE INDEX idx_gene_records_challenger_id ON public.gene_records USING btree (challenger_id);

--
-- Name: idx_gene_records_decision_status; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_gene_records_decision_status ON public.gene_records USING btree (decision_status);

--
-- Name: idx_gene_records_deleted_at; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_gene_records_deleted_at ON public.gene_records USING btree (deleted_at);

--
-- Name: idx_gene_records_pair; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_gene_records_pair ON public.gene_records USING btree (pair);

--
-- Name: idx_gene_records_plan_hash; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_gene_records_plan_hash ON public.gene_records USING btree (plan_hash);

--
-- Name: idx_gene_records_strategy_id; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_gene_records_strategy_id ON public.gene_records USING btree (strategy_id);

--
-- Name: idx_import_jobs_cancel_requested; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_import_jobs_cancel_requested ON public.import_jobs USING btree (cancel_requested);

--
-- Name: idx_import_jobs_deleted_at; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_import_jobs_deleted_at ON public.import_jobs USING btree (deleted_at);

--
-- Name: idx_import_jobs_interval; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_import_jobs_interval ON public.import_jobs USING btree ("interval");

--
-- Name: idx_import_jobs_job_id; Type: INDEX; Schema: public; Owner: ql
--

CREATE UNIQUE INDEX idx_import_jobs_job_id ON public.import_jobs USING btree (job_id);

--
-- Name: idx_import_jobs_requested_by; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_import_jobs_requested_by ON public.import_jobs USING btree (requested_by);

--
-- Name: idx_import_jobs_status; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_import_jobs_status ON public.import_jobs USING btree (status);

--
-- Name: idx_import_jobs_symbol; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_import_jobs_symbol ON public.import_jobs USING btree (symbol);

--
-- Name: idx_inst_unique_active; Type: INDEX; Schema: public; Owner: ql
--

CREATE UNIQUE INDEX idx_inst_unique_active ON public.strategy_instances USING btree (owner_user_id, strategy_id, pair, account_id) WHERE ((status)::text <> 'retired'::text);

--
-- Name: idx_kline_gaps_deleted_at; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_kline_gaps_deleted_at ON public.kline_gaps USING btree (deleted_at);

--
-- Name: idx_kline_gaps_gap_end_ms; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_kline_gaps_gap_end_ms ON public.kline_gaps USING btree (gap_end_ms);

--
-- Name: idx_kline_gaps_gap_start_ms; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_kline_gaps_gap_start_ms ON public.kline_gaps USING btree (gap_start_ms);

--
-- Name: idx_kline_gaps_interval; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_kline_gaps_interval ON public.kline_gaps USING btree ("interval");

--
-- Name: idx_kline_gaps_symbol; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_kline_gaps_symbol ON public.kline_gaps USING btree (symbol);

--
-- Name: idx_klines_interval; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_klines_interval ON public.klines USING btree ("interval");

--
-- Name: idx_klines_symbol; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_klines_symbol ON public.klines USING btree (symbol);

--
-- Name: idx_reconciliation_discrepancies_account_id; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_reconciliation_discrepancies_account_id ON public.reconciliation_discrepancies USING btree (account_id);

--
-- Name: idx_reconciliation_discrepancies_created_at; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_reconciliation_discrepancies_created_at ON public.reconciliation_discrepancies USING btree (created_at);

--
-- Name: idx_reconciliation_discrepancies_instance_id; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_reconciliation_discrepancies_instance_id ON public.reconciliation_discrepancies USING btree (instance_id);

--
-- Name: idx_reconciliation_discrepancies_reported_at_ms; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_reconciliation_discrepancies_reported_at_ms ON public.reconciliation_discrepancies USING btree (reported_at_ms);

--
-- Name: idx_runtime_states_instance_id; Type: INDEX; Schema: public; Owner: ql
--

CREATE UNIQUE INDEX idx_runtime_states_instance_id ON public.runtime_states USING btree (instance_id);

--
-- Name: idx_sharpe_banks_challenger_id; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_sharpe_banks_challenger_id ON public.sharpe_banks USING btree (challenger_id);

--
-- Name: idx_sharpe_banks_deleted_at; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_sharpe_banks_deleted_at ON public.sharpe_banks USING btree (deleted_at);

--
-- Name: idx_spot_executions_client_order_id; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_spot_executions_client_order_id ON public.spot_executions USING btree (client_order_id);

--
-- Name: idx_spot_executions_exchange_order_id; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_spot_executions_exchange_order_id ON public.spot_executions USING btree (exchange_order_id);

--
-- Name: idx_spot_executions_trade_id; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_spot_executions_trade_id ON public.spot_executions USING btree (trade_id);

--
-- Name: idx_spot_lots_entry_trade_id; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_spot_lots_entry_trade_id ON public.spot_lots USING btree (entry_trade_id);

--
-- Name: idx_spot_lots_instance_id; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_spot_lots_instance_id ON public.spot_lots USING btree (instance_id);

--
-- Name: idx_spot_lots_kind; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_spot_lots_kind ON public.spot_lots USING btree (kind);

--
-- Name: idx_spot_lots_lot_id; Type: INDEX; Schema: public; Owner: ql
--

CREATE UNIQUE INDEX idx_spot_lots_lot_id ON public.spot_lots USING btree (lot_id);

--
-- Name: idx_spot_lots_symbol; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_spot_lots_symbol ON public.spot_lots USING btree (symbol);

--
-- Name: idx_strategy_instances_account_id; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_strategy_instances_account_id ON public.strategy_instances USING btree (account_id);

--
-- Name: idx_strategy_instances_active_champ_id; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_strategy_instances_active_champ_id ON public.strategy_instances USING btree (active_champ_id);

--
-- Name: idx_strategy_instances_funded_at_ms; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_strategy_instances_funded_at_ms ON public.strategy_instances USING btree (funded_at_ms);

--
-- Name: idx_strategy_instances_instance_id; Type: INDEX; Schema: public; Owner: ql
--

CREATE UNIQUE INDEX idx_strategy_instances_instance_id ON public.strategy_instances USING btree (instance_id);

--
-- Name: idx_strategy_instances_owner_user_id; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_strategy_instances_owner_user_id ON public.strategy_instances USING btree (owner_user_id);

--
-- Name: idx_strategy_instances_pair; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_strategy_instances_pair ON public.strategy_instances USING btree (pair);

--
-- Name: idx_strategy_instances_status; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_strategy_instances_status ON public.strategy_instances USING btree (status);

--
-- Name: idx_strategy_instances_strategy_id; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_strategy_instances_strategy_id ON public.strategy_instances USING btree (strategy_id);

--
-- Name: idx_strategy_pair; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_strategy_pair ON public.sharpe_banks USING btree (strategy_id, pair_id);

--
-- Name: idx_strategy_templates_active; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_strategy_templates_active ON public.strategy_templates USING btree (active);

--
-- Name: idx_strategy_templates_strategy_id; Type: INDEX; Schema: public; Owner: ql
--

CREATE UNIQUE INDEX idx_strategy_templates_strategy_id ON public.strategy_templates USING btree (strategy_id);

--
-- Name: idx_task_gen; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_task_gen ON public.evaluation_traces USING btree (task_id, generation);

--
-- Name: idx_trade_records_client_order_id; Type: INDEX; Schema: public; Owner: ql
--

CREATE UNIQUE INDEX idx_trade_records_client_order_id ON public.trade_records USING btree (client_order_id);

--
-- Name: idx_trade_records_instance_id; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_trade_records_instance_id ON public.trade_records USING btree (instance_id);

--
-- Name: idx_trade_records_lot_id; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_trade_records_lot_id ON public.trade_records USING btree (lot_id);

--
-- Name: idx_trade_records_status; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_trade_records_status ON public.trade_records USING btree (status);

--
-- Name: idx_trade_records_symbol; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_trade_records_symbol ON public.trade_records USING btree (symbol);

--
-- Name: idx_users_active; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_users_active ON public.users USING btree (active);

--
-- Name: idx_users_created_at; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_users_created_at ON public.users USING btree (created_at);

--
-- Name: idx_users_email; Type: INDEX; Schema: public; Owner: ql
--

CREATE UNIQUE INDEX idx_users_email ON public.users USING btree (email);

--
-- Name: idx_users_role; Type: INDEX; Schema: public; Owner: ql
--

CREATE INDEX idx_users_role ON public.users USING btree (role);

--
-- Name: idx_users_user_id; Type: INDEX; Schema: public; Owner: ql
--

CREATE UNIQUE INDEX idx_users_user_id ON public.users USING btree (user_id);

--
--

--
--

--
-- Name: uq_champion_active; Type: INDEX; Schema: public; Owner: ql
--

CREATE UNIQUE INDEX uq_champion_active ON public.champion_histories USING btree (strategy_id, pair) WHERE ((retired_at IS NULL) AND (deleted_at IS NULL));

--
-- Name: uq_import_jobs_active; Type: INDEX; Schema: public; Owner: ql
--

-- Predicate written in the same `IN (...)` source form as db.go's
-- importJobUniqueSQL so it round-trips to a byte-identical pg_dump rendering
-- as the AutoMigrate database. Postgres canonicalizes an IN-list and an
-- ANY(ARRAY[...]) to different stored expressions, and the drift test compares
-- the dumped form, so the source spelling must match db.go's, not pg_dump's.
CREATE UNIQUE INDEX uq_import_jobs_active ON public.import_jobs (symbol, "interval") WHERE status IN ('queued', 'running');

--
--


-- +goose Down
DROP TABLE IF EXISTS public.agent_errors CASCADE;
DROP TABLE IF EXISTS public.agent_tokens CASCADE;
DROP TABLE IF EXISTS public.audit_logs CASCADE;
DROP TABLE IF EXISTS public.champion_histories CASCADE;
DROP TABLE IF EXISTS public.evaluation_traces CASCADE;
DROP TABLE IF EXISTS public.evolution_tasks CASCADE;
DROP TABLE IF EXISTS public.gene_records CASCADE;
DROP TABLE IF EXISTS public.import_jobs CASCADE;
DROP TABLE IF EXISTS public.kline_gaps CASCADE;
DROP TABLE IF EXISTS public.klines CASCADE;
DROP TABLE IF EXISTS public.portfolio_states CASCADE;
DROP TABLE IF EXISTS public.reconciliation_discrepancies CASCADE;
DROP TABLE IF EXISTS public.runtime_states CASCADE;
DROP TABLE IF EXISTS public.sharpe_banks CASCADE;
DROP TABLE IF EXISTS public.spot_executions CASCADE;
DROP TABLE IF EXISTS public.spot_lots CASCADE;
DROP TABLE IF EXISTS public.strategy_instances CASCADE;
DROP TABLE IF EXISTS public.strategy_templates CASCADE;
DROP TABLE IF EXISTS public.trade_records CASCADE;
DROP TABLE IF EXISTS public.users CASCADE;
DROP EXTENSION IF EXISTS timescaledb CASCADE;
