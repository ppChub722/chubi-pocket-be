-- Baseline (paper plan, 2026-11-02): the whole schema in one file; migrations 1-130 live in git history.
--
-- PostgreSQL database dump
--



SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

--
-- Name: avatar_src(uuid, jsonb); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.avatar_src(p_user_id uuid, p_icon jsonb) RETURNS jsonb
    LANGUAGE sql STABLE
    AS $$
    SELECT COALESCE(
        (SELECT jsonb_build_object(
                    'icon_code', COALESCE(u.icon_code, p_icon),
                    'settings', u.avatar_settings,
                    'photo_id', a.id,
                    'photo_version', a.version)
           FROM users u
           LEFT JOIN user_avatars a
             ON a.user_id = u.id
            AND a.source = u.avatar_settings ->> 'source'
            AND (a.source = 'upload' OR EXISTS (
                    SELECT 1 FROM user_connections c
                     WHERE c.user_id = u.id AND c.provider = a.source))
          WHERE u.id = p_user_id),
        jsonb_build_object('icon_code', p_icon))
$$;


--
-- Name: project_transactions_locked_check(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.project_transactions_locked_check() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF TG_OP IN ('UPDATE', 'DELETE') AND EXISTS (
        SELECT 1 FROM projects WHERE id = OLD.project_id AND completed_at IS NOT NULL FOR SHARE) THEN
        RAISE EXCEPTION 'EVENT_COMPLETED' USING ERRCODE = 'P0001', HINT = 'event_completed';
    END IF;
    IF TG_OP IN ('INSERT', 'UPDATE') AND EXISTS (
        SELECT 1 FROM projects WHERE id = NEW.project_id AND completed_at IS NOT NULL FOR SHARE) THEN
        RAISE EXCEPTION 'EVENT_COMPLETED' USING ERRCODE = 'P0001', HINT = 'event_completed';
    END IF;
    RETURN COALESCE(NEW, OLD);
END $$;


--
-- Name: recurring_skip_on_debt_delete(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.recurring_skip_on_debt_delete() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    UPDATE recurring_items
    SET archived = TRUE, edited = TRUE, debt_entry_id = NULL, produced_at = NULL, auto_created_at = NULL,
        cleared_at = NULL,
        draft = jsonb_set(draft, '{amount}', to_jsonb(OLD.amount))
    WHERE debt_entry_id = OLD.id;
    RETURN OLD;
END;
$$;


--
-- Name: set_timestamp(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.set_timestamp() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  NEW.updated_at = NOW();
  RETURN NEW;
END;
$$;


--
-- Name: split_people_locked(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.split_people_locked() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM project_transactions b JOIN projects p ON p.id = b.project_id
                WHERE b.id = COALESCE(NEW.split_transaction_id, OLD.split_transaction_id)
                  AND p.completed_at IS NOT NULL FOR SHARE OF p) THEN
        RAISE EXCEPTION 'EVENT_COMPLETED' USING ERRCODE = 'P0001', HINT = 'event_completed';
    END IF;
    RETURN COALESCE(NEW, OLD);
END $$;


--
-- Name: trg_sync_split_people_member(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.trg_sync_split_people_member() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    UPDATE split_transaction_people
       SET name = NEW.display_name, user_id = COALESCE(NEW.user_id, user_id)
     WHERE project_member_id = NEW.id
       AND (name IS DISTINCT FROM NEW.display_name OR (NEW.user_id IS NOT NULL AND user_id IS DISTINCT FROM NEW.user_id));
    RETURN NEW;
END $$;


SET default_tablespace = '';

SET default_table_access_method = heap;

--
-- Name: account_members; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.account_members (
    id uuid NOT NULL,
    account_id uuid NOT NULL,
    user_id uuid NOT NULL,
    role character varying(10) NOT NULL,
    report_scope character varying(10) DEFAULT 'none'::character varying NOT NULL,
    joined_at timestamp with time zone,
    left_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    created_by_user_id uuid,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by_user_id uuid,
    sort_order integer DEFAULT 0 NOT NULL,
    CONSTRAINT account_members_report_scope_check CHECK (((report_scope)::text = ANY (ARRAY[('none'::character varying)::text, ('own'::character varying)::text, ('all'::character varying)::text]))),
    CONSTRAINT account_members_role_check CHECK (((role)::text = ANY (ARRAY[('owner'::character varying)::text, ('member'::character varying)::text])))
);


--
-- Name: accounts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.accounts (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    name character varying(100) NOT NULL,
    type character varying(20) NOT NULL,
    balance numeric(15,2) DEFAULT 0 NOT NULL,
    currency character varying(3) NOT NULL,
    status character varying(20) DEFAULT 'active'::character varying NOT NULL,
    credit_limit numeric(15,2),
    statement_date smallint,
    payment_due_date smallint,
    minimum_payment numeric(15,2),
    sort_order integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    created_by_user_id uuid,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by_user_id uuid,
    description text,
    note text,
    icon_code jsonb,
    logo_url text,
    identifiers jsonb DEFAULT '[]'::jsonb NOT NULL,
    track_balance boolean DEFAULT true NOT NULL,
    CONSTRAINT accounts_payment_due_date_check CHECK (((payment_due_date IS NULL) OR ((payment_due_date >= 1) AND (payment_due_date <= 31)))),
    CONSTRAINT accounts_statement_date_check CHECK (((statement_date IS NULL) OR ((statement_date >= 1) AND (statement_date <= 31)))),
    CONSTRAINT accounts_status_check CHECK (((status)::text = ANY (ARRAY[('active'::character varying)::text, ('archived'::character varying)::text]))),
    CONSTRAINT accounts_type_check CHECK (((type)::text = ANY (ARRAY[('cash'::character varying)::text, ('bank'::character varying)::text, ('e_wallet'::character varying)::text, ('credit_card'::character varying)::text, ('pay_later'::character varying)::text])))
);


--
-- Name: project_transactions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.project_transactions (
    id uuid NOT NULL,
    project_id uuid,
    owner_user_id uuid NOT NULL,
    type character varying(20) NOT NULL,
    amount numeric(15,2) NOT NULL,
    date date NOT NULL,
    note text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    created_by_user_id uuid,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by_user_id uuid,
    description text,
    payback_kind character varying(10),
    version integer DEFAULT 1 NOT NULL,
    split_mode character varying(10) DEFAULT 'custom'::character varying NOT NULL,
    CONSTRAINT project_transactions_amount_check CHECK ((amount > (0)::numeric)),
    CONSTRAINT project_transactions_payback_kind_check CHECK (((payback_kind)::text = ANY (ARRAY[('payback'::character varying)::text, ('loan'::character varying)::text]))),
    CONSTRAINT project_transactions_split_mode_check CHECK (((split_mode)::text = ANY ((ARRAY['equal'::character varying, 'custom'::character varying])::text[]))),
    CONSTRAINT project_transactions_type_check CHECK (((type)::text = ANY (ARRAY[('expense'::character varying)::text, ('income'::character varying)::text, ('payback'::character varying)::text])))
);


--
-- Name: split_transaction_people; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.split_transaction_people (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    split_transaction_id uuid NOT NULL,
    user_id uuid,
    project_member_id uuid,
    name character varying(100) NOT NULL,
    share numeric(15,2) DEFAULT 0 NOT NULL,
    paid numeric(15,2) DEFAULT 0 NOT NULL,
    paid_back boolean DEFAULT false NOT NULL,
    sort integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    status character varying(10) DEFAULT 'joined'::character varying NOT NULL,
    paid_back_by_user_id uuid,
    paid_back_at timestamp with time zone,
    CONSTRAINT split_transaction_people_paid_check CHECK ((paid >= (0)::numeric)),
    CONSTRAINT split_transaction_people_share_check CHECK ((share >= (0)::numeric)),
    CONSTRAINT split_transaction_people_status_check CHECK (((status)::text = ANY ((ARRAY['invited'::character varying, 'joined'::character varying, 'declined'::character varying, 'left'::character varying])::text[])))
);


--
-- Name: bill_pairs; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.bill_pairs AS
 SELECT b.id AS pt_id,
    b.project_id,
    b.type,
        CASE
            WHEN ((b.type)::text = 'income'::text) THEN pa.id
            ELSE s.id
        END AS debtor_person,
        CASE
            WHEN ((b.type)::text = 'income'::text) THEN s.id
            ELSE pa.id
        END AS creditor_person,
        CASE
            WHEN ((b.type)::text = 'income'::text) THEN pa.project_member_id
            ELSE s.project_member_id
        END AS debtor,
        CASE
            WHEN ((b.type)::text = 'income'::text) THEN s.project_member_id
            ELSE pa.project_member_id
        END AS creditor,
    round(((s.share * pa.paid) / b.amount), 2) AS amount
   FROM ((public.project_transactions b
     JOIN public.split_transaction_people pa ON (((pa.split_transaction_id = b.id) AND (pa.paid > (0)::numeric))))
     JOIN public.split_transaction_people s ON (((s.split_transaction_id = b.id) AND (s.share > (0)::numeric) AND (s.id <> pa.id))))
  WHERE (round(((s.share * pa.paid) / b.amount), 2) > (0)::numeric);


--
-- Name: budget_categories; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.budget_categories (
    budget_id uuid NOT NULL,
    category_id uuid NOT NULL,
    include_children boolean DEFAULT true NOT NULL
);


--
-- Name: budget_periods; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.budget_periods (
    id uuid NOT NULL,
    budget_id uuid NOT NULL,
    seq integer NOT NULL,
    start_date date NOT NULL,
    end_date date,
    amount numeric(15,2) NOT NULL,
    overridden boolean DEFAULT false NOT NULL,
    note text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    base_amount numeric(15,2) NOT NULL,
    CONSTRAINT budget_periods_amount_check CHECK ((amount > (0)::numeric)),
    CONSTRAINT budget_periods_base_amount_check CHECK ((base_amount > (0)::numeric))
);


--
-- Name: budget_wallets; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.budget_wallets (
    budget_id uuid NOT NULL,
    account_id uuid NOT NULL
);


--
-- Name: budgets; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.budgets (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    name character varying(100) NOT NULL,
    description text,
    note text,
    icon_code jsonb,
    amount numeric(15,2) NOT NULL,
    warn_pct smallint,
    scope_all boolean DEFAULT false NOT NULL,
    tag_names text[] DEFAULT '{}'::text[] NOT NULL,
    recurring boolean DEFAULT false NOT NULL,
    freq character varying(10),
    interval_n integer DEFAULT 1 NOT NULL,
    start_date date NOT NULL,
    anchor_date date NOT NULL,
    end_kind character varying(10) DEFAULT 'none'::character varying NOT NULL,
    end_date date,
    end_count integer,
    closed_on date,
    status character varying(10) DEFAULT 'active'::character varying NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT budgets_amount_check1 CHECK ((amount > (0)::numeric)),
    CONSTRAINT budgets_check CHECK ((recurring = (freq IS NOT NULL))),
    CONSTRAINT budgets_end_count_check CHECK ((end_count >= 1)),
    CONSTRAINT budgets_end_kind_check CHECK (((end_kind)::text = ANY (ARRAY[('none'::character varying)::text, ('date'::character varying)::text, ('count'::character varying)::text]))),
    CONSTRAINT budgets_freq_check CHECK (((freq)::text = ANY (ARRAY[('daily'::character varying)::text, ('weekly'::character varying)::text, ('monthly'::character varying)::text, ('yearly'::character varying)::text]))),
    CONSTRAINT budgets_interval_n_check CHECK ((interval_n >= 1)),
    CONSTRAINT budgets_status_check1 CHECK (((status)::text = ANY (ARRAY[('active'::character varying)::text, ('archived'::character varying)::text]))),
    CONSTRAINT budgets_warn_pct_check CHECK (((warn_pct >= 1) AND (warn_pct <= 100)))
);


--
-- Name: categories; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.categories (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    name character varying(100) NOT NULL,
    type character varying(10) NOT NULL,
    parent_id uuid,
    is_system boolean DEFAULT false NOT NULL,
    system_kind character varying(30),
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    created_by_user_id uuid,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by_user_id uuid,
    sort_order integer DEFAULT 0 NOT NULL,
    include_in_report boolean DEFAULT true NOT NULL,
    description text,
    note text,
    icon_code jsonb,
    CONSTRAINT categories_type_check CHECK (((type)::text = ANY (ARRAY[('income'::character varying)::text, ('expense'::character varying)::text]))),
    CONSTRAINT chk_categories_no_self_parent CHECK ((id <> parent_id)),
    CONSTRAINT chk_categories_system_kind CHECK ((((is_system = true) AND (system_kind IS NOT NULL)) OR ((is_system = false) AND (system_kind IS NULL))))
);


--
-- Name: connection_link_codes; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.connection_link_codes (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    code_hash bytea NOT NULL,
    purpose character varying(10) DEFAULT 'link'::character varying NOT NULL,
    user_id uuid,
    provider character varying(20) NOT NULL,
    code_challenge text,
    external_id text,
    account_label text,
    external_name text,
    confirmed_at timestamp with time zone,
    expires_at timestamp with time zone NOT NULL,
    used_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT connection_link_codes_check CHECK ((((purpose)::text = 'login'::text) OR (user_id IS NOT NULL))),
    CONSTRAINT connection_link_codes_check1 CHECK ((((purpose)::text = 'link'::text) OR (code_challenge IS NOT NULL))),
    CONSTRAINT connection_link_codes_provider_check CHECK (((provider)::text = ANY (ARRAY[('telegram'::character varying)::text, ('line'::character varying)::text, ('google'::character varying)::text]))),
    CONSTRAINT connection_link_codes_purpose_check CHECK (((purpose)::text = ANY (ARRAY[('link'::character varying)::text, ('login'::character varying)::text])))
);


--
-- Name: contacts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.contacts (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    display_name character varying(100) NOT NULL,
    email character varying(255),
    phone character varying(50),
    note text,
    linked_user_id uuid,
    status character varying(20) DEFAULT 'active'::character varying NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    created_by_user_id uuid,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by_user_id uuid,
    last_used_at timestamp with time zone,
    icon_code jsonb,
    description text,
    nickname character varying(100),
    show_nickname boolean DEFAULT true NOT NULL,
    CONSTRAINT contacts_no_self_link CHECK ((user_id <> linked_user_id)),
    CONSTRAINT contacts_status_check CHECK (((status)::text = ANY (ARRAY[('active'::character varying)::text, ('archived'::character varying)::text])))
);


--
-- Name: event_settle_bookings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.event_settle_bookings (
    project_id uuid NOT NULL,
    user_id uuid NOT NULL,
    lines jsonb DEFAULT '[]'::jsonb NOT NULL,
    booked_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: event_tags; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.event_tags (
    id uuid NOT NULL,
    project_id uuid NOT NULL,
    name character varying(50) NOT NULL,
    icon_code jsonb,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    created_by_user_id uuid,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by_user_id uuid
);


--
-- Name: idempotency_keys; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.idempotency_keys (
    user_id uuid NOT NULL,
    key character varying(200) NOT NULL,
    request_hash bytea NOT NULL,
    status_code smallint,
    content_type character varying(100),
    response_body bytea,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: import_logs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.import_logs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    kind character varying(32) NOT NULL,
    scheme character varying(20),
    code character varying(20),
    trans_ref character varying(64),
    details jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: notifications; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.notifications (
    id uuid NOT NULL,
    recipient_user_id uuid NOT NULL,
    type character varying(50) NOT NULL,
    actor_user_id uuid,
    payload jsonb NOT NULL,
    deep_link character varying(500),
    read_at timestamp with time zone,
    actioned_at timestamp with time zone,
    dismissed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    created_by_user_id uuid,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by_user_id uuid,
    CONSTRAINT notifications_terminal_xor CHECK (((actioned_at IS NULL) OR (dismissed_at IS NULL))),
    CONSTRAINT notifications_type_check CHECK (((type)::text = ANY ((ARRAY['contact_link_request'::character varying, 'account_invite'::character varying, 'project_invite'::character varying, 'recurring_due'::character varying, 'recurring_ending'::character varying, 'recurring_auto_created'::character varying, 'debt_disconnected'::character varying, 'debt_repaid'::character varying, 'event_completed'::character varying, 'event_member_removed'::character varying, 'event_deleted'::character varying, 'bill_invite'::character varying, 'bill_new'::character varying, 'bill_changed'::character varying, 'bill_removed'::character varying, 'bill_owner_changed'::character varying, 'bill_moved'::character varying])::text[])))
);


--
-- Name: payment_providers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.payment_providers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    kind character varying(20) NOT NULL,
    scheme character varying(20) NOT NULL,
    code character varying(20) NOT NULL,
    name_th text NOT NULL,
    name_en text NOT NULL,
    short_name character varying(20),
    color character varying(9),
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT payment_providers_kind_check CHECK (((kind)::text = ANY (ARRAY[('bank'::character varying)::text, ('e_wallet'::character varying)::text, ('card_issuer'::character varying)::text, ('other'::character varying)::text])))
);


--
-- Name: pending_transactions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pending_transactions (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    source character varying(32) DEFAULT 'manual'::character varying NOT NULL,
    kind character varying(16) DEFAULT 'create'::character varying NOT NULL,
    draft jsonb DEFAULT '{}'::jsonb NOT NULL,
    target_debt_id uuid,
    target_transaction_id uuid,
    source_ref jsonb,
    last_error jsonb,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT pending_kind_target CHECK (((((kind)::text = ANY (ARRAY[('create'::character varying)::text, ('saving'::character varying)::text])) AND (target_debt_id IS NULL) AND (target_transaction_id IS NULL)) OR (((kind)::text = 'settle_debt'::text) AND (target_debt_id IS NOT NULL)) OR (((kind)::text = 'update_tx'::text) AND (target_transaction_id IS NOT NULL)))),
    CONSTRAINT pending_transactions_kind_check CHECK (((kind)::text = ANY (ARRAY[('create'::character varying)::text, ('settle_debt'::character varying)::text, ('update_tx'::character varying)::text, ('saving'::character varying)::text])))
);


--
-- Name: personal_debt_bills; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.personal_debt_bills (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    debt_id uuid NOT NULL,
    user_id uuid NOT NULL,
    transaction_id uuid,
    amount numeric(15,2) NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    split_transaction_id uuid NOT NULL,
    split_person_id uuid,
    CONSTRAINT personal_debt_bills_amount_check CHECK ((amount >= (0)::numeric))
);


--
-- Name: personal_debts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.personal_debts (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    direction character varying(20) NOT NULL,
    counterparty_contact_id uuid,
    counterparty_person_name character varying(100) NOT NULL,
    project_id uuid,
    amount numeric(15,2) NOT NULL,
    currency character varying(3) NOT NULL,
    note text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    created_by_user_id uuid,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by_user_id uuid,
    counterpart_debt_id uuid,
    description text,
    date date NOT NULL,
    paid boolean DEFAULT false NOT NULL,
    source_kind character varying(20),
    due_date date,
    kind character varying(20) DEFAULT 'debt'::character varying NOT NULL,
    disconnected_at timestamp with time zone,
    private_note text,
    CONSTRAINT personal_debts_amount_check CHECK ((amount >= (0)::numeric)),
    CONSTRAINT personal_debts_direction_check CHECK (((direction)::text = ANY (ARRAY[('paid'::character varying)::text, ('received'::character varying)::text]))),
    CONSTRAINT personal_debts_kind_check CHECK (((kind)::text = ANY (ARRAY[('debt'::character varying)::text, ('repayment'::character varying)::text]))),
    CONSTRAINT personal_debts_source_kind_check CHECK (((source_kind IS NULL) OR ((source_kind)::text = 'paper'::text)))
);


--
-- Name: project_member_contacts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.project_member_contacts (
    project_member_id uuid NOT NULL,
    user_id uuid NOT NULL,
    contact_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: project_members; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.project_members (
    id uuid NOT NULL,
    project_id uuid NOT NULL,
    user_id uuid,
    display_name character varying(100) NOT NULL,
    role character varying(20) DEFAULT 'contributor'::character varying NOT NULL,
    status character varying(20) DEFAULT 'pending'::character varying NOT NULL,
    invited_at timestamp with time zone,
    joined_at timestamp with time zone,
    left_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    created_by_user_id uuid,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by_user_id uuid,
    icon_code jsonb,
    removed_at timestamp with time zone,
    invited_user_id uuid,
    CONSTRAINT project_members_role_check CHECK (((role)::text = ANY (ARRAY[('owner'::character varying)::text, ('contributor'::character varying)::text, ('viewer'::character varying)::text]))),
    CONSTRAINT project_members_status_check CHECK (((status)::text = ANY (ARRAY[('pending'::character varying)::text, ('active'::character varying)::text, ('left'::character varying)::text])))
);


--
-- Name: project_my_defaults; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.project_my_defaults (
    project_id uuid NOT NULL,
    user_id uuid NOT NULL,
    category_id uuid,
    account_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: project_my_tags; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.project_my_tags (
    project_id uuid NOT NULL,
    user_id uuid NOT NULL,
    tag_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: project_transaction_tags; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.project_transaction_tags (
    project_transaction_id uuid NOT NULL,
    event_tag_id uuid NOT NULL
);


--
-- Name: projects; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.projects (
    id uuid NOT NULL,
    owner_user_id uuid NOT NULL,
    name character varying(100) NOT NULL,
    description text,
    start_date date,
    end_date date,
    status character varying(20) DEFAULT 'active'::character varying NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    created_by_user_id uuid,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by_user_id uuid,
    icon_code jsonb,
    planned_amount numeric(15,2),
    note text,
    completed_at timestamp with time zone,
    CONSTRAINT projects_planned_amount_check CHECK ((planned_amount > (0)::numeric)),
    CONSTRAINT projects_status_check CHECK (((status)::text = ANY (ARRAY[('active'::character varying)::text, ('completed'::character varying)::text, ('cancelled'::character varying)::text, ('archived'::character varying)::text])))
);


--
-- Name: recurring_due_checks; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.recurring_due_checks (
    user_id uuid NOT NULL,
    checked_on date NOT NULL,
    checked_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: recurring_items; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.recurring_items (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    rule_id uuid NOT NULL,
    seq integer,
    slot date,
    date date NOT NULL,
    draft jsonb NOT NULL,
    edited boolean DEFAULT false NOT NULL,
    manual boolean DEFAULT false NOT NULL,
    archived boolean DEFAULT false NOT NULL,
    produced_at timestamp with time zone,
    pending_id uuid,
    transaction_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    saving_entry_id uuid,
    cleared_at timestamp with time zone,
    auto_created_at timestamp with time zone,
    debt_entry_id uuid
);


--
-- Name: recurring_rules; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.recurring_rules (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    name character varying(100) NOT NULL,
    description text,
    note text,
    icon_code jsonb,
    type character varying(10) NOT NULL,
    amount numeric(15,2) NOT NULL,
    account_id uuid,
    to_account_id uuid,
    category_id uuid,
    tag_ids uuid[] DEFAULT '{}'::uuid[] NOT NULL,
    freq character varying(10) NOT NULL,
    interval_n integer DEFAULT 1 NOT NULL,
    weekdays integer[] DEFAULT '{}'::integer[] NOT NULL,
    monthly_mode character varying(10),
    month_day integer,
    month_nth integer,
    month_weekday integer,
    dates date[] DEFAULT '{}'::date[] NOT NULL,
    start_date date NOT NULL,
    end_kind character varying(5) DEFAULT 'none'::character varying NOT NULL,
    end_date date,
    end_count integer,
    total_count integer,
    generated_until date,
    status character varying(10) DEFAULT 'active'::character varying NOT NULL,
    ending_notified boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    goal_id uuid,
    kind character varying(12) DEFAULT 'subscription'::character varying NOT NULL,
    trial_until date,
    tracked_from date DEFAULT CURRENT_DATE NOT NULL,
    due_notified_until date,
    auto_create boolean DEFAULT false NOT NULL,
    auto_created_until date,
    paused_from date,
    pause_reason text,
    gaps jsonb DEFAULT '[]'::jsonb NOT NULL,
    contact_id uuid,
    person_name character varying(100),
    debt_kind character varying(20),
    debt_direction character varying(20),
    zero_rounds integer DEFAULT 0 NOT NULL,
    CONSTRAINT recurring_rules_amount_check CHECK ((amount >= (0)::numeric)),
    CONSTRAINT recurring_rules_debt_direction_check CHECK (((debt_direction)::text = ANY (ARRAY[('paid'::character varying)::text, ('received'::character varying)::text]))),
    CONSTRAINT recurring_rules_debt_fields_check CHECK (((((type)::text = 'debt'::text) = ((person_name IS NOT NULL) AND (debt_kind IS NOT NULL) AND (debt_direction IS NOT NULL))) AND (((type)::text = 'debt'::text) OR (contact_id IS NULL)))),
    CONSTRAINT recurring_rules_debt_kind_check CHECK (((debt_kind)::text = ANY (ARRAY[('debt'::character varying)::text, ('repayment'::character varying)::text]))),
    CONSTRAINT recurring_rules_end_kind_check CHECK (((end_kind)::text = ANY (ARRAY[('none'::character varying)::text, ('date'::character varying)::text, ('count'::character varying)::text]))),
    CONSTRAINT recurring_rules_freq_check CHECK (((freq)::text = ANY (ARRAY[('daily'::character varying)::text, ('weekly'::character varying)::text, ('monthly'::character varying)::text, ('yearly'::character varying)::text, ('dates'::character varying)::text]))),
    CONSTRAINT recurring_rules_interval_n_check CHECK ((interval_n >= 1)),
    CONSTRAINT recurring_rules_kind_check CHECK (((kind)::text = ANY (ARRAY[('subscription'::character varying)::text, ('installment'::character varying)::text, ('income'::character varying)::text, ('reminder'::character varying)::text, ('saving'::character varying)::text, ('debt'::character varying)::text]))),
    CONSTRAINT recurring_rules_monthly_mode_check CHECK (((monthly_mode IS NULL) OR ((monthly_mode)::text = ANY (ARRAY[('day'::character varying)::text, ('weekday'::character varying)::text])))),
    CONSTRAINT recurring_rules_pause_reason_check CHECK ((pause_reason = ANY (ARRAY['manual'::text, 'overdue_limit'::text, 'goal_inactive'::text, 'no_balance'::text]))),
    CONSTRAINT recurring_rules_status_check CHECK (((status)::text = ANY (ARRAY[('active'::character varying)::text, ('paused'::character varying)::text, ('archived'::character varying)::text]))),
    CONSTRAINT recurring_rules_type_check CHECK (((type)::text = ANY (ARRAY[('expense'::character varying)::text, ('income'::character varying)::text, ('transfer'::character varying)::text, ('saving'::character varying)::text, ('debt'::character varying)::text]))),
    CONSTRAINT recurring_rules_zero_rounds_check CHECK (((zero_rounds >= 0) AND (zero_rounds <= 3)))
);


--
-- Name: saving_entries; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.saving_entries (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    group_id uuid NOT NULL,
    kind character varying(10) NOT NULL,
    account_id uuid,
    from_bucket character varying(10) NOT NULL,
    from_goal_id uuid,
    to_bucket character varying(10) NOT NULL,
    to_goal_id uuid,
    amount numeric(15,2) NOT NULL,
    date date NOT NULL,
    note text,
    source character varying(10) DEFAULT 'manual'::character varying NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT saving_entries_amount_check CHECK ((amount > (0)::numeric)),
    CONSTRAINT saving_entries_check CHECK (((((kind)::text = 'save'::text) AND ((from_bucket)::text = 'available'::text) AND ((to_bucket)::text <> 'available'::text)) OR (((kind)::text = 'withdraw'::text) AND ((from_bucket)::text <> 'available'::text) AND ((to_bucket)::text = 'available'::text)) OR (((kind)::text = 'move'::text) AND ((from_bucket)::text <> 'available'::text) AND ((to_bucket)::text <> 'available'::text)))),
    CONSTRAINT saving_entries_from_bucket_check CHECK (((from_bucket)::text = ANY (ARRAY[('available'::character varying)::text, ('spare'::character varying)::text, ('goal'::character varying)::text]))),
    CONSTRAINT saving_entries_kind_check CHECK (((kind)::text = ANY (ARRAY[('save'::character varying)::text, ('withdraw'::character varying)::text, ('move'::character varying)::text]))),
    CONSTRAINT saving_entries_source_check CHECK (((source)::text = ANY (ARRAY[('manual'::character varying)::text, ('recurring'::character varying)::text, ('pending'::character varying)::text]))),
    CONSTRAINT saving_entries_to_bucket_check CHECK (((to_bucket)::text = ANY (ARRAY[('available'::character varying)::text, ('spare'::character varying)::text, ('goal'::character varying)::text])))
);


--
-- Name: saving_goals; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.saving_goals (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    name character varying(100) NOT NULL,
    target_amount numeric(15,2),
    deadline date,
    icon_code jsonb,
    status character varying(20) DEFAULT 'active'::character varying NOT NULL,
    note text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    created_by_user_id uuid,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by_user_id uuid,
    description text,
    milestones integer[] DEFAULT '{25,50,75,100}'::integer[] NOT NULL,
    closed_at timestamp with time zone,
    period_amount numeric(15,2),
    period_freq text,
    period_interval integer DEFAULT 1 NOT NULL,
    period_anchor date,
    CONSTRAINT saving_goals_period_amount_check CHECK ((period_amount > (0)::numeric)),
    CONSTRAINT saving_goals_period_freq_check CHECK ((period_freq = ANY (ARRAY['daily'::text, 'weekly'::text, 'monthly'::text, 'yearly'::text]))),
    CONSTRAINT saving_goals_period_interval_check CHECK (((period_interval >= 1) AND (period_interval <= 99))),
    CONSTRAINT saving_goals_status_check CHECK (((status)::text = ANY (ARRAY[('active'::character varying)::text, ('completed'::character varying)::text, ('archived'::character varying)::text]))),
    CONSTRAINT saving_goals_target_amount_check CHECK ((target_amount > (0)::numeric))
);



--
-- Name: split_person_contacts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.split_person_contacts (
    split_person_id uuid NOT NULL,
    user_id uuid NOT NULL,
    contact_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: tags; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tags (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    name character varying(50) NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    created_by_user_id uuid,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by_user_id uuid,
    icon_code jsonb,
    description text,
    note text
);


--
-- Name: telegram_messages; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.telegram_messages (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    update_id bigint NOT NULL,
    chat_id bigint NOT NULL,
    from_username text,
    text text NOT NULL,
    received_at timestamp with time zone DEFAULT now() NOT NULL,
    user_id uuid
);


--
-- Name: transaction_tags; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.transaction_tags (
    transaction_id uuid NOT NULL,
    tag_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    created_by_user_id uuid,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by_user_id uuid
);


--
-- Name: transactions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.transactions (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    account_id uuid,
    type character varying(10) NOT NULL,
    amount numeric(15,2) NOT NULL,
    category_id uuid,
    date date NOT NULL,
    note text,
    transfer_group_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    created_by_user_id uuid,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by_user_id uuid,
    source_personal_debt_id uuid,
    description text,
    debt_connected boolean DEFAULT false NOT NULL,
    debt_prev_category_id uuid,
    private_note text,
    split_transaction_id uuid,
    split_person_id uuid,
    my_share numeric(15,2),
    CONSTRAINT transactions_amount_check CHECK ((amount >= (0)::numeric)),
    CONSTRAINT transactions_my_share_check CHECK ((my_share >= (0)::numeric)),
    CONSTRAINT transactions_source_at_most_one CHECK (((source_personal_debt_id IS NULL) OR (split_transaction_id IS NULL))),
    CONSTRAINT transactions_type_check CHECK (((type)::text = ANY (ARRAY[('expense'::character varying)::text, ('income'::character varying)::text, ('transfer'::character varying)::text])))
);


--
-- Name: user_avatars; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_avatars (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    source character varying(20) NOT NULL,
    image bytea NOT NULL,
    version integer DEFAULT 1 NOT NULL,
    imported_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT user_avatars_source_check CHECK (((source)::text = ANY (ARRAY[('upload'::character varying)::text, ('telegram'::character varying)::text, ('line'::character varying)::text, ('google'::character varying)::text])))
);


--
-- Name: user_connections; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_connections (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    provider character varying(20) NOT NULL,
    external_id text NOT NULL,
    account_label text,
    linked_at timestamp with time zone DEFAULT now() NOT NULL,
    permissions jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT user_connections_provider_check CHECK (((provider)::text = ANY (ARRAY[('telegram'::character varying)::text, ('line'::character varying)::text, ('google'::character varying)::text])))
);


--
-- Name: user_notification_settings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_notification_settings (
    user_id uuid NOT NULL,
    default_account_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    created_by_user_id uuid,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by_user_id uuid,
    muted_types text[] DEFAULT '{}'::text[] NOT NULL,
    auto_sync_events boolean DEFAULT true NOT NULL,
    auto_sync_debts boolean DEFAULT true NOT NULL,
    auto_accept_events boolean DEFAULT true NOT NULL,
    auto_accept_debts boolean DEFAULT false NOT NULL,
    auto_accept_event_invites boolean DEFAULT false NOT NULL,
    auto_accept_contact_links boolean DEFAULT false NOT NULL,
    auto_accept_wallet_invites boolean DEFAULT false NOT NULL,
    on_split_removed character varying(10) DEFAULT 'ask'::character varying NOT NULL,
    auto_accept_repayments boolean DEFAULT false NOT NULL,
    CONSTRAINT user_notification_settings_on_split_removed_check CHECK (((on_split_removed)::text = ANY ((ARRAY['ask'::character varying, 'keep'::character varying, 'delete'::character varying])::text[])))
);


--
-- Name: user_pack_permissions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_pack_permissions (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    pack_id text NOT NULL,
    granted_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: user_preferences; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_preferences (
    user_id uuid NOT NULL,
    preferences jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    created_by_user_id uuid,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by_user_id uuid
);


--
-- Name: users; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.users (
    id uuid NOT NULL,
    username character varying(50) NOT NULL,
    email character varying(255),
    email_verified_at timestamp with time zone,
    display_name character varying(100) NOT NULL,
    password_hash character varying(255),
    currency character varying(3) DEFAULT 'THB'::character varying NOT NULL,
    status character varying(30) DEFAULT 'active'::character varying NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    created_by_user_id uuid,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by_user_id uuid,
    icon_code jsonb,
    avatar_settings jsonb DEFAULT '{"border": null, "colors": [], "source": "icon"}'::jsonb NOT NULL,
    created_via character varying(20) DEFAULT 'password'::character varying NOT NULL,
    CONSTRAINT users_created_via_check CHECK (((created_via)::text = ANY (ARRAY[('password'::character varying)::text, ('telegram'::character varying)::text, ('line'::character varying)::text, ('google'::character varying)::text]))),
    CONSTRAINT users_password_or_external CHECK (((password_hash IS NOT NULL) OR ((created_via)::text <> 'password'::text))),
    CONSTRAINT users_status_check CHECK (((status)::text = ANY (ARRAY[('active'::character varying)::text, ('inactive'::character varying)::text])))
);


--
-- Name: account_members account_members_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.account_members
    ADD CONSTRAINT account_members_pkey PRIMARY KEY (id);


--
-- Name: accounts accounts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.accounts
    ADD CONSTRAINT accounts_pkey PRIMARY KEY (id);


--
-- Name: budget_categories budget_categories_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.budget_categories
    ADD CONSTRAINT budget_categories_pkey PRIMARY KEY (budget_id, category_id);


--
-- Name: budget_periods budget_periods_budget_id_start_date_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.budget_periods
    ADD CONSTRAINT budget_periods_budget_id_start_date_key UNIQUE (budget_id, start_date);


--
-- Name: budget_periods budget_periods_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.budget_periods
    ADD CONSTRAINT budget_periods_pkey PRIMARY KEY (id);


--
-- Name: budget_wallets budget_wallets_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.budget_wallets
    ADD CONSTRAINT budget_wallets_pkey PRIMARY KEY (budget_id, account_id);


--
-- Name: budgets budgets_pkey1; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.budgets
    ADD CONSTRAINT budgets_pkey1 PRIMARY KEY (id);


--
-- Name: categories categories_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.categories
    ADD CONSTRAINT categories_pkey PRIMARY KEY (id);


--
-- Name: connection_link_codes connection_link_codes_code_hash_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.connection_link_codes
    ADD CONSTRAINT connection_link_codes_code_hash_key UNIQUE (code_hash);


--
-- Name: connection_link_codes connection_link_codes_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.connection_link_codes
    ADD CONSTRAINT connection_link_codes_pkey PRIMARY KEY (id);


--
-- Name: contacts contacts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.contacts
    ADD CONSTRAINT contacts_pkey PRIMARY KEY (id);


--
-- Name: event_settle_bookings event_settle_bookings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.event_settle_bookings
    ADD CONSTRAINT event_settle_bookings_pkey PRIMARY KEY (project_id, user_id);


--
-- Name: event_tags event_tags_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.event_tags
    ADD CONSTRAINT event_tags_pkey PRIMARY KEY (id);


--
-- Name: idempotency_keys idempotency_keys_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.idempotency_keys
    ADD CONSTRAINT idempotency_keys_pkey PRIMARY KEY (user_id, key);


--
-- Name: import_logs import_logs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.import_logs
    ADD CONSTRAINT import_logs_pkey PRIMARY KEY (id);


--
-- Name: notifications notifications_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notifications
    ADD CONSTRAINT notifications_pkey PRIMARY KEY (id);


--
-- Name: payment_providers payment_providers_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payment_providers
    ADD CONSTRAINT payment_providers_pkey PRIMARY KEY (id);


--
-- Name: payment_providers payment_providers_scheme_code; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payment_providers
    ADD CONSTRAINT payment_providers_scheme_code UNIQUE (scheme, code);


--
-- Name: pending_transactions pending_transactions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pending_transactions
    ADD CONSTRAINT pending_transactions_pkey PRIMARY KEY (id);


--
-- Name: personal_debt_bills personal_debt_bills_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.personal_debt_bills
    ADD CONSTRAINT personal_debt_bills_pkey PRIMARY KEY (id);


--
-- Name: personal_debts personal_debts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.personal_debts
    ADD CONSTRAINT personal_debts_pkey PRIMARY KEY (id);


--
-- Name: project_member_contacts project_member_contacts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_member_contacts
    ADD CONSTRAINT project_member_contacts_pkey PRIMARY KEY (project_member_id, user_id);


--
-- Name: project_members project_members_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_members
    ADD CONSTRAINT project_members_pkey PRIMARY KEY (id);


--
-- Name: project_my_defaults project_my_defaults_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_my_defaults
    ADD CONSTRAINT project_my_defaults_pkey PRIMARY KEY (project_id, user_id);


--
-- Name: project_my_tags project_my_tags_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_my_tags
    ADD CONSTRAINT project_my_tags_pkey PRIMARY KEY (project_id, tag_id);


--
-- Name: project_transaction_tags project_transaction_tags_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_transaction_tags
    ADD CONSTRAINT project_transaction_tags_pkey PRIMARY KEY (project_transaction_id, event_tag_id);


--
-- Name: project_transactions project_transactions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_transactions
    ADD CONSTRAINT project_transactions_pkey PRIMARY KEY (id);


--
-- Name: projects projects_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.projects
    ADD CONSTRAINT projects_pkey PRIMARY KEY (id);


--
-- Name: recurring_due_checks recurring_due_checks_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.recurring_due_checks
    ADD CONSTRAINT recurring_due_checks_pkey PRIMARY KEY (user_id);


--
-- Name: recurring_items recurring_items_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.recurring_items
    ADD CONSTRAINT recurring_items_pkey PRIMARY KEY (id);


--
-- Name: recurring_rules recurring_rules_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.recurring_rules
    ADD CONSTRAINT recurring_rules_pkey PRIMARY KEY (id);


--
-- Name: saving_entries saving_entries_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.saving_entries
    ADD CONSTRAINT saving_entries_pkey PRIMARY KEY (id);


--
-- Name: saving_goals saving_goals_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.saving_goals
    ADD CONSTRAINT saving_goals_pkey PRIMARY KEY (id);



--
-- Name: split_person_contacts split_person_contacts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.split_person_contacts
    ADD CONSTRAINT split_person_contacts_pkey PRIMARY KEY (split_person_id, user_id);


--
-- Name: split_transaction_people split_transaction_people_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.split_transaction_people
    ADD CONSTRAINT split_transaction_people_pkey PRIMARY KEY (id);


--
-- Name: tags tags_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tags
    ADD CONSTRAINT tags_pkey PRIMARY KEY (id);


--
-- Name: telegram_messages telegram_messages_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.telegram_messages
    ADD CONSTRAINT telegram_messages_pkey PRIMARY KEY (id);


--
-- Name: telegram_messages telegram_messages_update_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.telegram_messages
    ADD CONSTRAINT telegram_messages_update_id_key UNIQUE (update_id);


--
-- Name: transaction_tags transaction_tags_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.transaction_tags
    ADD CONSTRAINT transaction_tags_pkey PRIMARY KEY (transaction_id, tag_id);


--
-- Name: transactions transactions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.transactions
    ADD CONSTRAINT transactions_pkey PRIMARY KEY (id);


--
-- Name: user_avatars user_avatars_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_avatars
    ADD CONSTRAINT user_avatars_pkey PRIMARY KEY (id);


--
-- Name: user_avatars user_avatars_user_id_source_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_avatars
    ADD CONSTRAINT user_avatars_user_id_source_key UNIQUE (user_id, source);


--
-- Name: user_connections user_connections_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_connections
    ADD CONSTRAINT user_connections_pkey PRIMARY KEY (id);


--
-- Name: user_connections user_connections_provider_external_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_connections
    ADD CONSTRAINT user_connections_provider_external_id_key UNIQUE (provider, external_id);


--
-- Name: user_connections user_connections_user_id_provider_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_connections
    ADD CONSTRAINT user_connections_user_id_provider_key UNIQUE (user_id, provider);


--
-- Name: user_notification_settings user_notification_settings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_notification_settings
    ADD CONSTRAINT user_notification_settings_pkey PRIMARY KEY (user_id);


--
-- Name: user_pack_permissions user_pack_permissions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_pack_permissions
    ADD CONSTRAINT user_pack_permissions_pkey PRIMARY KEY (id);


--
-- Name: user_preferences user_preferences_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_preferences
    ADD CONSTRAINT user_preferences_pkey PRIMARY KEY (user_id);


--
-- Name: users users_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_pkey PRIMARY KEY (id);


--
-- Name: idempotency_keys_created_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idempotency_keys_created_at_idx ON public.idempotency_keys USING btree (created_at);


--
-- Name: idx_account_members_account; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_account_members_account ON public.account_members USING btree (account_id) WHERE (left_at IS NULL);


--
-- Name: idx_account_members_live_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_account_members_live_unique ON public.account_members USING btree (account_id, user_id) WHERE (left_at IS NULL);


--
-- Name: idx_account_members_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_account_members_user ON public.account_members USING btree (user_id) WHERE (left_at IS NULL);


--
-- Name: idx_accounts_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_accounts_user ON public.accounts USING btree (user_id, status);


--
-- Name: idx_accounts_user_sort; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_accounts_user_sort ON public.accounts USING btree (user_id, sort_order);


--
-- Name: idx_budgets_v2_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_budgets_v2_user ON public.budgets USING btree (user_id, status);


--
-- Name: idx_categories_name; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_categories_name ON public.categories USING btree (user_id, type, parent_id, lower((name)::text)) NULLS NOT DISTINCT;


--
-- Name: idx_categories_parent; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_categories_parent ON public.categories USING btree (parent_id);


--
-- Name: idx_categories_system_kind; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_categories_system_kind ON public.categories USING btree (user_id, system_kind) WHERE (system_kind IS NOT NULL);


--
-- Name: idx_categories_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_categories_user ON public.categories USING btree (user_id);


--
-- Name: idx_categories_user_updated; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_categories_user_updated ON public.categories USING btree (user_id, updated_at);


--
-- Name: idx_connection_link_codes_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_connection_link_codes_user ON public.connection_link_codes USING btree (user_id, provider);


--
-- Name: idx_contacts_last_used; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_contacts_last_used ON public.contacts USING btree (user_id, last_used_at DESC NULLS LAST);


--
-- Name: idx_contacts_linked_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_contacts_linked_user ON public.contacts USING btree (linked_user_id) WHERE (linked_user_id IS NOT NULL);


--
-- Name: idx_contacts_name; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_contacts_name ON public.contacts USING btree (user_id, lower((display_name)::text));


--
-- Name: idx_contacts_owner_linked_user; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_contacts_owner_linked_user ON public.contacts USING btree (user_id, linked_user_id) WHERE (linked_user_id IS NOT NULL);


--
-- Name: idx_contacts_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_contacts_user ON public.contacts USING btree (user_id, status);


--
-- Name: idx_event_tags_name; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_event_tags_name ON public.event_tags USING btree (project_id, lower((name)::text));


--
-- Name: idx_import_logs_kind_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_import_logs_kind_created ON public.import_logs USING btree (kind, created_at DESC);


--
-- Name: idx_notifications_actor; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_notifications_actor ON public.notifications USING btree (actor_user_id) WHERE (actor_user_id IS NOT NULL);


--
-- Name: idx_notifications_recipient_all; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_notifications_recipient_all ON public.notifications USING btree (recipient_user_id, created_at DESC);


--
-- Name: idx_notifications_recipient_unread; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_notifications_recipient_unread ON public.notifications USING btree (recipient_user_id, created_at DESC) WHERE (read_at IS NULL);


--
-- Name: idx_pending_transactions_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pending_transactions_user ON public.pending_transactions USING btree (user_id, created_at DESC);


--
-- Name: idx_personal_debt_bills_debt; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_personal_debt_bills_debt ON public.personal_debt_bills USING btree (debt_id);


--
-- Name: idx_personal_debt_bills_pair; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_personal_debt_bills_pair ON public.personal_debt_bills USING btree (user_id, split_transaction_id, split_person_id);


--
-- Name: idx_personal_debt_bills_paper; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_personal_debt_bills_paper ON public.personal_debt_bills USING btree (split_transaction_id);


--
-- Name: idx_personal_debt_bills_tx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_personal_debt_bills_tx ON public.personal_debt_bills USING btree (transaction_id) WHERE (transaction_id IS NOT NULL);


--
-- Name: idx_personal_debts_counterpart; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_personal_debts_counterpart ON public.personal_debts USING btree (counterpart_debt_id) WHERE (counterpart_debt_id IS NOT NULL);


--
-- Name: idx_personal_debts_counterparty_contact; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_personal_debts_counterparty_contact ON public.personal_debts USING btree (user_id, counterparty_contact_id) WHERE (counterparty_contact_id IS NOT NULL);


--
-- Name: idx_personal_debts_project; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_personal_debts_project ON public.personal_debts USING btree (project_id) WHERE (project_id IS NOT NULL);


--
-- Name: idx_personal_debts_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_personal_debts_user ON public.personal_debts USING btree (user_id, date DESC, created_at DESC);


--
-- Name: idx_personal_debts_user_updated; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_personal_debts_user_updated ON public.personal_debts USING btree (user_id, updated_at);


--
-- Name: idx_project_members_owner; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_project_members_owner ON public.project_members USING btree (project_id) WHERE ((role)::text = 'owner'::text);


--
-- Name: idx_project_members_project; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_project_members_project ON public.project_members USING btree (project_id, status);


--
-- Name: idx_project_members_user; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_project_members_user ON public.project_members USING btree (project_id, user_id) WHERE (user_id IS NOT NULL);


--
-- Name: idx_project_members_user_reverse; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_project_members_user_reverse ON public.project_members USING btree (user_id, status) WHERE (user_id IS NOT NULL);


--
-- Name: idx_project_my_tags_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_project_my_tags_user ON public.project_my_tags USING btree (user_id, tag_id);


--
-- Name: idx_project_transaction_tags_tag; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_project_transaction_tags_tag ON public.project_transaction_tags USING btree (event_tag_id);


--
-- Name: idx_project_transactions_owner; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_project_transactions_owner ON public.project_transactions USING btree (owner_user_id);


--
-- Name: idx_project_transactions_project_updated; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_project_transactions_project_updated ON public.project_transactions USING btree (project_id, updated_at);


--
-- Name: idx_project_tx_project; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_project_tx_project ON public.project_transactions USING btree (project_id, date DESC);


--
-- Name: idx_projects_owner; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_projects_owner ON public.projects USING btree (owner_user_id, status);


--
-- Name: idx_projects_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_projects_status ON public.projects USING btree (status, updated_at DESC);


--
-- Name: idx_recurring_items_debt_entry; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_recurring_items_debt_entry ON public.recurring_items USING btree (debt_entry_id) WHERE (debt_entry_id IS NOT NULL);


--
-- Name: idx_recurring_items_pending; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_recurring_items_pending ON public.recurring_items USING btree (pending_id) WHERE (pending_id IS NOT NULL);


--
-- Name: idx_recurring_items_rule; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_recurring_items_rule ON public.recurring_items USING btree (rule_id, date);


--
-- Name: idx_recurring_items_rule_slot; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_recurring_items_rule_slot ON public.recurring_items USING btree (rule_id, slot) WHERE (slot IS NOT NULL);


--
-- Name: idx_recurring_items_saving; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_recurring_items_saving ON public.recurring_items USING btree (saving_entry_id) WHERE (saving_entry_id IS NOT NULL);


--
-- Name: idx_recurring_items_tx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_recurring_items_tx ON public.recurring_items USING btree (transaction_id) WHERE (transaction_id IS NOT NULL);


--
-- Name: idx_recurring_items_user_date; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_recurring_items_user_date ON public.recurring_items USING btree (user_id, date);


--
-- Name: idx_recurring_items_user_updated; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_recurring_items_user_updated ON public.recurring_items USING btree (user_id, updated_at);


--
-- Name: idx_recurring_rules_contact; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_recurring_rules_contact ON public.recurring_rules USING btree (contact_id) WHERE (contact_id IS NOT NULL);


--
-- Name: idx_recurring_rules_goal; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_recurring_rules_goal ON public.recurring_rules USING btree (goal_id) WHERE (goal_id IS NOT NULL);


--
-- Name: idx_recurring_rules_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_recurring_rules_user ON public.recurring_rules USING btree (user_id, status);


--
-- Name: idx_saving_entries_account; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_saving_entries_account ON public.saving_entries USING btree (account_id);


--
-- Name: idx_saving_entries_from_goal; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_saving_entries_from_goal ON public.saving_entries USING btree (from_goal_id) WHERE (from_goal_id IS NOT NULL);


--
-- Name: idx_saving_entries_to_goal; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_saving_entries_to_goal ON public.saving_entries USING btree (to_goal_id) WHERE (to_goal_id IS NOT NULL);


--
-- Name: idx_saving_entries_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_saving_entries_user ON public.saving_entries USING btree (user_id, date DESC, created_at DESC);


--
-- Name: idx_saving_entries_user_updated; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_saving_entries_user_updated ON public.saving_entries USING btree (user_id, updated_at);


--
-- Name: idx_saving_goals_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_saving_goals_user ON public.saving_goals USING btree (user_id, status);


--
-- Name: idx_split_people_bill_member; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_split_people_bill_member ON public.split_transaction_people USING btree (split_transaction_id, project_member_id) WHERE (project_member_id IS NOT NULL);


--
-- Name: idx_split_people_bill_user; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_split_people_bill_user ON public.split_transaction_people USING btree (split_transaction_id, user_id) WHERE (user_id IS NOT NULL);


--
-- Name: idx_split_people_member; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_split_people_member ON public.split_transaction_people USING btree (project_member_id) WHERE (project_member_id IS NOT NULL);


--
-- Name: idx_split_people_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_split_people_user ON public.split_transaction_people USING btree (user_id) WHERE (user_id IS NOT NULL);


--
-- Name: idx_tags_name; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_tags_name ON public.tags USING btree (user_id, lower((name)::text));


--
-- Name: idx_tags_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tags_user ON public.tags USING btree (user_id);


--
-- Name: idx_tags_user_updated; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tags_user_updated ON public.tags USING btree (user_id, updated_at);


--
-- Name: idx_telegram_messages_chat; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_telegram_messages_chat ON public.telegram_messages USING btree (chat_id, received_at DESC);


--
-- Name: idx_transaction_tags_tag; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_transaction_tags_tag ON public.transaction_tags USING btree (tag_id);


--
-- Name: idx_transaction_tags_tag_updated; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_transaction_tags_tag_updated ON public.transaction_tags USING btree (tag_id, updated_at);


--
-- Name: idx_transactions_account_date; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_transactions_account_date ON public.transactions USING btree (account_id, date DESC, created_at DESC);


--
-- Name: idx_transactions_account_updated; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_transactions_account_updated ON public.transactions USING btree (account_id, updated_at);


--
-- Name: idx_transactions_category; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_transactions_category ON public.transactions USING btree (category_id, date DESC) WHERE (category_id IS NOT NULL);


--
-- Name: idx_transactions_paper_person; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_transactions_paper_person ON public.transactions USING btree (split_transaction_id, user_id) WHERE (split_transaction_id IS NOT NULL);


--
-- Name: idx_transactions_source_personal_debt; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_transactions_source_personal_debt ON public.transactions USING btree (source_personal_debt_id) WHERE (source_personal_debt_id IS NOT NULL);


--
-- Name: idx_transactions_split_tx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_transactions_split_tx ON public.transactions USING btree (split_transaction_id) WHERE (split_transaction_id IS NOT NULL);


--
-- Name: idx_transactions_transfer_group; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_transactions_transfer_group ON public.transactions USING btree (transfer_group_id) WHERE (transfer_group_id IS NOT NULL);


--
-- Name: idx_transactions_user_date; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_transactions_user_date ON public.transactions USING btree (user_id, date DESC, created_at DESC);


--
-- Name: idx_transactions_user_updated; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_transactions_user_updated ON public.transactions USING btree (user_id, updated_at);


--
-- Name: idx_user_pack_permissions_expires; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_pack_permissions_expires ON public.user_pack_permissions USING btree (expires_at) WHERE (expires_at IS NOT NULL);


--
-- Name: idx_user_pack_permissions_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_pack_permissions_user ON public.user_pack_permissions USING btree (user_id);


--
-- Name: idx_user_pack_permissions_user_pack; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_user_pack_permissions_user_pack ON public.user_pack_permissions USING btree (user_id, pack_id);


--
-- Name: idx_users_email; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_users_email ON public.users USING btree (email);


--
-- Name: idx_users_username; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_users_username ON public.users USING btree (username);


--
-- Name: project_member_contacts_contact_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX project_member_contacts_contact_idx ON public.project_member_contacts USING btree (contact_id);


--
-- Name: project_members_invited_user_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX project_members_invited_user_idx ON public.project_members USING btree (project_id, invited_user_id) WHERE (invited_user_id IS NOT NULL);


--
-- Name: split_person_contacts_contact_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX split_person_contacts_contact_idx ON public.split_person_contacts USING btree (contact_id);


--
-- Name: personal_debts personal_debts_recurring_skip; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER personal_debts_recurring_skip BEFORE DELETE ON public.personal_debts FOR EACH ROW EXECUTE FUNCTION public.recurring_skip_on_debt_delete();


--
-- Name: project_transactions project_transactions_locked; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER project_transactions_locked BEFORE INSERT OR DELETE OR UPDATE OF project_id, type, payback_kind, amount, date, description, note, split_mode ON public.project_transactions FOR EACH ROW EXECUTE FUNCTION public.project_transactions_locked_check();


--
-- Name: account_members set_timestamp_account_members; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER set_timestamp_account_members BEFORE UPDATE ON public.account_members FOR EACH ROW EXECUTE FUNCTION public.set_timestamp();


--
-- Name: accounts set_timestamp_accounts; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER set_timestamp_accounts BEFORE UPDATE ON public.accounts FOR EACH ROW EXECUTE FUNCTION public.set_timestamp();


--
-- Name: budgets set_timestamp_budgets_v2; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER set_timestamp_budgets_v2 BEFORE UPDATE ON public.budgets FOR EACH ROW EXECUTE FUNCTION public.set_timestamp();


--
-- Name: categories set_timestamp_categories; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER set_timestamp_categories BEFORE UPDATE ON public.categories FOR EACH ROW EXECUTE FUNCTION public.set_timestamp();


--
-- Name: contacts set_timestamp_contacts; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER set_timestamp_contacts BEFORE UPDATE ON public.contacts FOR EACH ROW EXECUTE FUNCTION public.set_timestamp();


--
-- Name: event_tags set_timestamp_event_tags; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER set_timestamp_event_tags BEFORE UPDATE ON public.event_tags FOR EACH ROW EXECUTE FUNCTION public.set_timestamp();


--
-- Name: notifications set_timestamp_notifications; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER set_timestamp_notifications BEFORE UPDATE ON public.notifications FOR EACH ROW EXECUTE FUNCTION public.set_timestamp();


--
-- Name: payment_providers set_timestamp_payment_providers; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER set_timestamp_payment_providers BEFORE UPDATE ON public.payment_providers FOR EACH ROW EXECUTE FUNCTION public.set_timestamp();


--
-- Name: pending_transactions set_timestamp_pending_transactions; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER set_timestamp_pending_transactions BEFORE UPDATE ON public.pending_transactions FOR EACH ROW EXECUTE FUNCTION public.set_timestamp();


--
-- Name: personal_debt_bills set_timestamp_personal_debt_bills; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER set_timestamp_personal_debt_bills BEFORE UPDATE ON public.personal_debt_bills FOR EACH ROW EXECUTE FUNCTION public.set_timestamp();


--
-- Name: personal_debts set_timestamp_personal_debts; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER set_timestamp_personal_debts BEFORE UPDATE ON public.personal_debts FOR EACH ROW EXECUTE FUNCTION public.set_timestamp();


--
-- Name: project_members set_timestamp_project_members; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER set_timestamp_project_members BEFORE UPDATE ON public.project_members FOR EACH ROW EXECUTE FUNCTION public.set_timestamp();


--
-- Name: project_my_defaults set_timestamp_project_my_defaults; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER set_timestamp_project_my_defaults BEFORE UPDATE ON public.project_my_defaults FOR EACH ROW EXECUTE FUNCTION public.set_timestamp();


--
-- Name: project_transactions set_timestamp_project_transactions; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER set_timestamp_project_transactions BEFORE UPDATE ON public.project_transactions FOR EACH ROW EXECUTE FUNCTION public.set_timestamp();


--
-- Name: projects set_timestamp_projects; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER set_timestamp_projects BEFORE UPDATE ON public.projects FOR EACH ROW EXECUTE FUNCTION public.set_timestamp();


--
-- Name: recurring_items set_timestamp_recurring_items; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER set_timestamp_recurring_items BEFORE UPDATE ON public.recurring_items FOR EACH ROW EXECUTE FUNCTION public.set_timestamp();


--
-- Name: recurring_rules set_timestamp_recurring_rules; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER set_timestamp_recurring_rules BEFORE UPDATE ON public.recurring_rules FOR EACH ROW EXECUTE FUNCTION public.set_timestamp();


--
-- Name: saving_entries set_timestamp_saving_entries; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER set_timestamp_saving_entries BEFORE UPDATE ON public.saving_entries FOR EACH ROW EXECUTE FUNCTION public.set_timestamp();


--
-- Name: saving_goals set_timestamp_saving_goals; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER set_timestamp_saving_goals BEFORE UPDATE ON public.saving_goals FOR EACH ROW EXECUTE FUNCTION public.set_timestamp();


--
-- Name: split_transaction_people set_timestamp_split_transaction_people; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER set_timestamp_split_transaction_people BEFORE UPDATE ON public.split_transaction_people FOR EACH ROW EXECUTE FUNCTION public.set_timestamp();


--
-- Name: tags set_timestamp_tags; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER set_timestamp_tags BEFORE UPDATE ON public.tags FOR EACH ROW EXECUTE FUNCTION public.set_timestamp();


--
-- Name: transaction_tags set_timestamp_transaction_tags; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER set_timestamp_transaction_tags BEFORE UPDATE ON public.transaction_tags FOR EACH ROW EXECUTE FUNCTION public.set_timestamp();


--
-- Name: transactions set_timestamp_transactions; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER set_timestamp_transactions BEFORE UPDATE ON public.transactions FOR EACH ROW EXECUTE FUNCTION public.set_timestamp();


--
-- Name: user_connections set_timestamp_user_connections; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER set_timestamp_user_connections BEFORE UPDATE ON public.user_connections FOR EACH ROW EXECUTE FUNCTION public.set_timestamp();


--
-- Name: user_notification_settings set_timestamp_user_notification_settings; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER set_timestamp_user_notification_settings BEFORE UPDATE ON public.user_notification_settings FOR EACH ROW EXECUTE FUNCTION public.set_timestamp();


--
-- Name: user_pack_permissions set_timestamp_user_pack_permissions; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER set_timestamp_user_pack_permissions BEFORE UPDATE ON public.user_pack_permissions FOR EACH ROW EXECUTE FUNCTION public.set_timestamp();


--
-- Name: user_preferences set_timestamp_user_preferences; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER set_timestamp_user_preferences BEFORE UPDATE ON public.user_preferences FOR EACH ROW EXECUTE FUNCTION public.set_timestamp();


--
-- Name: users set_timestamp_users; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER set_timestamp_users BEFORE UPDATE ON public.users FOR EACH ROW EXECUTE FUNCTION public.set_timestamp();


--
-- Name: split_transaction_people split_people_locked; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER split_people_locked BEFORE INSERT OR DELETE OR UPDATE OF share, paid, paid_back, project_member_id ON public.split_transaction_people FOR EACH ROW EXECUTE FUNCTION public.split_people_locked();


--
-- Name: project_members sync_split_people_member; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER sync_split_people_member AFTER UPDATE OF user_id, display_name ON public.project_members FOR EACH ROW EXECUTE FUNCTION public.trg_sync_split_people_member();


--
-- Name: account_members account_members_account_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.account_members
    ADD CONSTRAINT account_members_account_id_fkey FOREIGN KEY (account_id) REFERENCES public.accounts(id) ON DELETE CASCADE;


--
-- Name: account_members account_members_created_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.account_members
    ADD CONSTRAINT account_members_created_by_user_id_fkey FOREIGN KEY (created_by_user_id) REFERENCES public.users(id);


--
-- Name: account_members account_members_updated_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.account_members
    ADD CONSTRAINT account_members_updated_by_user_id_fkey FOREIGN KEY (updated_by_user_id) REFERENCES public.users(id);


--
-- Name: account_members account_members_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.account_members
    ADD CONSTRAINT account_members_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id);


--
-- Name: accounts accounts_created_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.accounts
    ADD CONSTRAINT accounts_created_by_user_id_fkey FOREIGN KEY (created_by_user_id) REFERENCES public.users(id);


--
-- Name: accounts accounts_updated_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.accounts
    ADD CONSTRAINT accounts_updated_by_user_id_fkey FOREIGN KEY (updated_by_user_id) REFERENCES public.users(id);


--
-- Name: accounts accounts_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.accounts
    ADD CONSTRAINT accounts_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: budget_categories budget_categories_budget_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.budget_categories
    ADD CONSTRAINT budget_categories_budget_id_fkey FOREIGN KEY (budget_id) REFERENCES public.budgets(id) ON DELETE CASCADE;


--
-- Name: budget_categories budget_categories_category_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.budget_categories
    ADD CONSTRAINT budget_categories_category_id_fkey FOREIGN KEY (category_id) REFERENCES public.categories(id) ON DELETE CASCADE;


--
-- Name: budget_periods budget_periods_budget_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.budget_periods
    ADD CONSTRAINT budget_periods_budget_id_fkey FOREIGN KEY (budget_id) REFERENCES public.budgets(id) ON DELETE CASCADE;


--
-- Name: budget_wallets budget_wallets_account_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.budget_wallets
    ADD CONSTRAINT budget_wallets_account_id_fkey FOREIGN KEY (account_id) REFERENCES public.accounts(id) ON DELETE CASCADE;


--
-- Name: budget_wallets budget_wallets_budget_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.budget_wallets
    ADD CONSTRAINT budget_wallets_budget_id_fkey FOREIGN KEY (budget_id) REFERENCES public.budgets(id) ON DELETE CASCADE;


--
-- Name: budgets budgets_user_id_fkey1; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.budgets
    ADD CONSTRAINT budgets_user_id_fkey1 FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: categories categories_created_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.categories
    ADD CONSTRAINT categories_created_by_user_id_fkey FOREIGN KEY (created_by_user_id) REFERENCES public.users(id);


--
-- Name: categories categories_parent_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.categories
    ADD CONSTRAINT categories_parent_id_fkey FOREIGN KEY (parent_id) REFERENCES public.categories(id);


--
-- Name: categories categories_updated_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.categories
    ADD CONSTRAINT categories_updated_by_user_id_fkey FOREIGN KEY (updated_by_user_id) REFERENCES public.users(id);


--
-- Name: categories categories_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.categories
    ADD CONSTRAINT categories_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: connection_link_codes connection_link_codes_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.connection_link_codes
    ADD CONSTRAINT connection_link_codes_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: contacts contacts_created_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.contacts
    ADD CONSTRAINT contacts_created_by_user_id_fkey FOREIGN KEY (created_by_user_id) REFERENCES public.users(id);


--
-- Name: contacts contacts_linked_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.contacts
    ADD CONSTRAINT contacts_linked_user_id_fkey FOREIGN KEY (linked_user_id) REFERENCES public.users(id);


--
-- Name: contacts contacts_updated_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.contacts
    ADD CONSTRAINT contacts_updated_by_user_id_fkey FOREIGN KEY (updated_by_user_id) REFERENCES public.users(id);


--
-- Name: contacts contacts_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.contacts
    ADD CONSTRAINT contacts_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: event_settle_bookings event_settle_bookings_project_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.event_settle_bookings
    ADD CONSTRAINT event_settle_bookings_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.projects(id) ON DELETE CASCADE;


--
-- Name: event_settle_bookings event_settle_bookings_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.event_settle_bookings
    ADD CONSTRAINT event_settle_bookings_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: event_tags event_tags_created_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.event_tags
    ADD CONSTRAINT event_tags_created_by_user_id_fkey FOREIGN KEY (created_by_user_id) REFERENCES public.users(id) ON DELETE SET NULL;


--
-- Name: event_tags event_tags_project_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.event_tags
    ADD CONSTRAINT event_tags_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.projects(id) ON DELETE CASCADE;


--
-- Name: event_tags event_tags_updated_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.event_tags
    ADD CONSTRAINT event_tags_updated_by_user_id_fkey FOREIGN KEY (updated_by_user_id) REFERENCES public.users(id) ON DELETE SET NULL;


--
-- Name: idempotency_keys idempotency_keys_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.idempotency_keys
    ADD CONSTRAINT idempotency_keys_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: import_logs import_logs_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.import_logs
    ADD CONSTRAINT import_logs_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: notifications notifications_actor_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notifications
    ADD CONSTRAINT notifications_actor_user_id_fkey FOREIGN KEY (actor_user_id) REFERENCES public.users(id);


--
-- Name: notifications notifications_created_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notifications
    ADD CONSTRAINT notifications_created_by_user_id_fkey FOREIGN KEY (created_by_user_id) REFERENCES public.users(id);


--
-- Name: notifications notifications_recipient_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notifications
    ADD CONSTRAINT notifications_recipient_user_id_fkey FOREIGN KEY (recipient_user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: notifications notifications_updated_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notifications
    ADD CONSTRAINT notifications_updated_by_user_id_fkey FOREIGN KEY (updated_by_user_id) REFERENCES public.users(id);


--
-- Name: pending_transactions pending_transactions_target_debt_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pending_transactions
    ADD CONSTRAINT pending_transactions_target_debt_id_fkey FOREIGN KEY (target_debt_id) REFERENCES public.personal_debts(id) ON DELETE CASCADE;


--
-- Name: pending_transactions pending_transactions_target_transaction_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pending_transactions
    ADD CONSTRAINT pending_transactions_target_transaction_id_fkey FOREIGN KEY (target_transaction_id) REFERENCES public.transactions(id) ON DELETE CASCADE;


--
-- Name: pending_transactions pending_transactions_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pending_transactions
    ADD CONSTRAINT pending_transactions_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: personal_debt_bills personal_debt_bills_debt_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.personal_debt_bills
    ADD CONSTRAINT personal_debt_bills_debt_id_fkey FOREIGN KEY (debt_id) REFERENCES public.personal_debts(id) ON DELETE CASCADE;


--
-- Name: personal_debt_bills personal_debt_bills_split_person_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.personal_debt_bills
    ADD CONSTRAINT personal_debt_bills_split_person_id_fkey FOREIGN KEY (split_person_id) REFERENCES public.split_transaction_people(id) ON DELETE SET NULL;


--
-- Name: personal_debt_bills personal_debt_bills_split_transaction_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.personal_debt_bills
    ADD CONSTRAINT personal_debt_bills_split_transaction_id_fkey FOREIGN KEY (split_transaction_id) REFERENCES public.project_transactions(id) ON DELETE CASCADE;


--
-- Name: personal_debt_bills personal_debt_bills_transaction_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.personal_debt_bills
    ADD CONSTRAINT personal_debt_bills_transaction_id_fkey FOREIGN KEY (transaction_id) REFERENCES public.transactions(id) ON DELETE SET NULL;


--
-- Name: personal_debt_bills personal_debt_bills_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.personal_debt_bills
    ADD CONSTRAINT personal_debt_bills_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: personal_debts personal_debts_counterpart_debt_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.personal_debts
    ADD CONSTRAINT personal_debts_counterpart_debt_id_fkey FOREIGN KEY (counterpart_debt_id) REFERENCES public.personal_debts(id) ON DELETE SET NULL;


--
-- Name: personal_debts personal_debts_counterparty_contact_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.personal_debts
    ADD CONSTRAINT personal_debts_counterparty_contact_id_fkey FOREIGN KEY (counterparty_contact_id) REFERENCES public.contacts(id) ON DELETE SET NULL;


--
-- Name: personal_debts personal_debts_created_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.personal_debts
    ADD CONSTRAINT personal_debts_created_by_user_id_fkey FOREIGN KEY (created_by_user_id) REFERENCES public.users(id);


--
-- Name: personal_debts personal_debts_project_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.personal_debts
    ADD CONSTRAINT personal_debts_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.projects(id) ON DELETE SET NULL;


--
-- Name: personal_debts personal_debts_updated_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.personal_debts
    ADD CONSTRAINT personal_debts_updated_by_user_id_fkey FOREIGN KEY (updated_by_user_id) REFERENCES public.users(id);


--
-- Name: personal_debts personal_debts_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.personal_debts
    ADD CONSTRAINT personal_debts_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: project_member_contacts project_member_contacts_contact_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_member_contacts
    ADD CONSTRAINT project_member_contacts_contact_id_fkey FOREIGN KEY (contact_id) REFERENCES public.contacts(id) ON DELETE CASCADE;


--
-- Name: project_member_contacts project_member_contacts_project_member_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_member_contacts
    ADD CONSTRAINT project_member_contacts_project_member_id_fkey FOREIGN KEY (project_member_id) REFERENCES public.project_members(id) ON DELETE CASCADE;


--
-- Name: project_member_contacts project_member_contacts_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_member_contacts
    ADD CONSTRAINT project_member_contacts_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: project_members project_members_created_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_members
    ADD CONSTRAINT project_members_created_by_user_id_fkey FOREIGN KEY (created_by_user_id) REFERENCES public.users(id);


--
-- Name: project_members project_members_invited_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_members
    ADD CONSTRAINT project_members_invited_user_id_fkey FOREIGN KEY (invited_user_id) REFERENCES public.users(id) ON DELETE SET NULL;


--
-- Name: project_members project_members_project_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_members
    ADD CONSTRAINT project_members_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.projects(id) ON DELETE CASCADE;


--
-- Name: project_members project_members_updated_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_members
    ADD CONSTRAINT project_members_updated_by_user_id_fkey FOREIGN KEY (updated_by_user_id) REFERENCES public.users(id);


--
-- Name: project_members project_members_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_members
    ADD CONSTRAINT project_members_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE SET NULL;


--
-- Name: project_my_defaults project_my_defaults_account_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_my_defaults
    ADD CONSTRAINT project_my_defaults_account_id_fkey FOREIGN KEY (account_id) REFERENCES public.accounts(id) ON DELETE SET NULL;


--
-- Name: project_my_defaults project_my_defaults_category_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_my_defaults
    ADD CONSTRAINT project_my_defaults_category_id_fkey FOREIGN KEY (category_id) REFERENCES public.categories(id) ON DELETE SET NULL;


--
-- Name: project_my_defaults project_my_defaults_project_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_my_defaults
    ADD CONSTRAINT project_my_defaults_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.projects(id) ON DELETE CASCADE;


--
-- Name: project_my_defaults project_my_defaults_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_my_defaults
    ADD CONSTRAINT project_my_defaults_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: project_my_tags project_my_tags_project_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_my_tags
    ADD CONSTRAINT project_my_tags_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.projects(id) ON DELETE CASCADE;


--
-- Name: project_my_tags project_my_tags_tag_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_my_tags
    ADD CONSTRAINT project_my_tags_tag_id_fkey FOREIGN KEY (tag_id) REFERENCES public.tags(id) ON DELETE CASCADE;


--
-- Name: project_my_tags project_my_tags_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_my_tags
    ADD CONSTRAINT project_my_tags_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: project_transaction_tags project_transaction_tags_event_tag_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_transaction_tags
    ADD CONSTRAINT project_transaction_tags_event_tag_id_fkey FOREIGN KEY (event_tag_id) REFERENCES public.event_tags(id) ON DELETE CASCADE;


--
-- Name: project_transaction_tags project_transaction_tags_project_transaction_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_transaction_tags
    ADD CONSTRAINT project_transaction_tags_project_transaction_id_fkey FOREIGN KEY (project_transaction_id) REFERENCES public.project_transactions(id) ON DELETE CASCADE;


--
-- Name: project_transactions project_transactions_created_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_transactions
    ADD CONSTRAINT project_transactions_created_by_user_id_fkey FOREIGN KEY (created_by_user_id) REFERENCES public.users(id);


--
-- Name: project_transactions project_transactions_owner_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_transactions
    ADD CONSTRAINT project_transactions_owner_user_id_fkey FOREIGN KEY (owner_user_id) REFERENCES public.users(id);


--
-- Name: project_transactions project_transactions_project_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_transactions
    ADD CONSTRAINT project_transactions_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.projects(id) ON DELETE CASCADE;


--
-- Name: project_transactions project_transactions_updated_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.project_transactions
    ADD CONSTRAINT project_transactions_updated_by_user_id_fkey FOREIGN KEY (updated_by_user_id) REFERENCES public.users(id);


--
-- Name: projects projects_created_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.projects
    ADD CONSTRAINT projects_created_by_user_id_fkey FOREIGN KEY (created_by_user_id) REFERENCES public.users(id);


--
-- Name: projects projects_owner_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.projects
    ADD CONSTRAINT projects_owner_user_id_fkey FOREIGN KEY (owner_user_id) REFERENCES public.users(id);


--
-- Name: projects projects_updated_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.projects
    ADD CONSTRAINT projects_updated_by_user_id_fkey FOREIGN KEY (updated_by_user_id) REFERENCES public.users(id);


--
-- Name: recurring_due_checks recurring_due_checks_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.recurring_due_checks
    ADD CONSTRAINT recurring_due_checks_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: recurring_items recurring_items_debt_entry_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.recurring_items
    ADD CONSTRAINT recurring_items_debt_entry_id_fkey FOREIGN KEY (debt_entry_id) REFERENCES public.personal_debts(id) ON DELETE SET NULL;


--
-- Name: recurring_items recurring_items_pending_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.recurring_items
    ADD CONSTRAINT recurring_items_pending_id_fkey FOREIGN KEY (pending_id) REFERENCES public.pending_transactions(id) ON DELETE SET NULL;


--
-- Name: recurring_items recurring_items_rule_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.recurring_items
    ADD CONSTRAINT recurring_items_rule_id_fkey FOREIGN KEY (rule_id) REFERENCES public.recurring_rules(id) ON DELETE CASCADE;


--
-- Name: recurring_items recurring_items_saving_entry_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.recurring_items
    ADD CONSTRAINT recurring_items_saving_entry_id_fkey FOREIGN KEY (saving_entry_id) REFERENCES public.saving_entries(id) ON DELETE SET NULL;


--
-- Name: recurring_items recurring_items_transaction_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.recurring_items
    ADD CONSTRAINT recurring_items_transaction_id_fkey FOREIGN KEY (transaction_id) REFERENCES public.transactions(id) ON DELETE SET NULL;


--
-- Name: recurring_items recurring_items_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.recurring_items
    ADD CONSTRAINT recurring_items_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: recurring_rules recurring_rules_account_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.recurring_rules
    ADD CONSTRAINT recurring_rules_account_id_fkey FOREIGN KEY (account_id) REFERENCES public.accounts(id) ON DELETE SET NULL;


--
-- Name: recurring_rules recurring_rules_category_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.recurring_rules
    ADD CONSTRAINT recurring_rules_category_id_fkey FOREIGN KEY (category_id) REFERENCES public.categories(id) ON DELETE SET NULL;


--
-- Name: recurring_rules recurring_rules_contact_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.recurring_rules
    ADD CONSTRAINT recurring_rules_contact_id_fkey FOREIGN KEY (contact_id) REFERENCES public.contacts(id) ON DELETE SET NULL;


--
-- Name: recurring_rules recurring_rules_goal_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.recurring_rules
    ADD CONSTRAINT recurring_rules_goal_id_fkey FOREIGN KEY (goal_id) REFERENCES public.saving_goals(id) ON DELETE CASCADE;


--
-- Name: recurring_rules recurring_rules_to_account_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.recurring_rules
    ADD CONSTRAINT recurring_rules_to_account_id_fkey FOREIGN KEY (to_account_id) REFERENCES public.accounts(id) ON DELETE SET NULL;


--
-- Name: recurring_rules recurring_rules_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.recurring_rules
    ADD CONSTRAINT recurring_rules_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: saving_entries saving_entries_account_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.saving_entries
    ADD CONSTRAINT saving_entries_account_id_fkey FOREIGN KEY (account_id) REFERENCES public.accounts(id) ON DELETE SET NULL;


--
-- Name: saving_entries saving_entries_from_goal_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.saving_entries
    ADD CONSTRAINT saving_entries_from_goal_id_fkey FOREIGN KEY (from_goal_id) REFERENCES public.saving_goals(id) ON DELETE SET NULL;


--
-- Name: saving_entries saving_entries_to_goal_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.saving_entries
    ADD CONSTRAINT saving_entries_to_goal_id_fkey FOREIGN KEY (to_goal_id) REFERENCES public.saving_goals(id) ON DELETE SET NULL;


--
-- Name: saving_entries saving_entries_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.saving_entries
    ADD CONSTRAINT saving_entries_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: saving_goals saving_goals_created_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.saving_goals
    ADD CONSTRAINT saving_goals_created_by_user_id_fkey FOREIGN KEY (created_by_user_id) REFERENCES public.users(id);


--
-- Name: saving_goals saving_goals_updated_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.saving_goals
    ADD CONSTRAINT saving_goals_updated_by_user_id_fkey FOREIGN KEY (updated_by_user_id) REFERENCES public.users(id);


--
-- Name: saving_goals saving_goals_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.saving_goals
    ADD CONSTRAINT saving_goals_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: split_person_contacts split_person_contacts_contact_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.split_person_contacts
    ADD CONSTRAINT split_person_contacts_contact_id_fkey FOREIGN KEY (contact_id) REFERENCES public.contacts(id) ON DELETE CASCADE;


--
-- Name: split_person_contacts split_person_contacts_split_person_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.split_person_contacts
    ADD CONSTRAINT split_person_contacts_split_person_id_fkey FOREIGN KEY (split_person_id) REFERENCES public.split_transaction_people(id) ON DELETE CASCADE;


--
-- Name: split_person_contacts split_person_contacts_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.split_person_contacts
    ADD CONSTRAINT split_person_contacts_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: split_transaction_people split_transaction_people_paid_back_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.split_transaction_people
    ADD CONSTRAINT split_transaction_people_paid_back_by_user_id_fkey FOREIGN KEY (paid_back_by_user_id) REFERENCES public.users(id) ON DELETE SET NULL;


--
-- Name: split_transaction_people split_transaction_people_project_member_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.split_transaction_people
    ADD CONSTRAINT split_transaction_people_project_member_id_fkey FOREIGN KEY (project_member_id) REFERENCES public.project_members(id) ON DELETE SET NULL;


--
-- Name: split_transaction_people split_transaction_people_split_transaction_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.split_transaction_people
    ADD CONSTRAINT split_transaction_people_split_transaction_id_fkey FOREIGN KEY (split_transaction_id) REFERENCES public.project_transactions(id) ON DELETE CASCADE;


--
-- Name: split_transaction_people split_transaction_people_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.split_transaction_people
    ADD CONSTRAINT split_transaction_people_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE SET NULL;


--
-- Name: tags tags_created_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tags
    ADD CONSTRAINT tags_created_by_user_id_fkey FOREIGN KEY (created_by_user_id) REFERENCES public.users(id);


--
-- Name: tags tags_updated_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tags
    ADD CONSTRAINT tags_updated_by_user_id_fkey FOREIGN KEY (updated_by_user_id) REFERENCES public.users(id);


--
-- Name: tags tags_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tags
    ADD CONSTRAINT tags_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: telegram_messages telegram_messages_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.telegram_messages
    ADD CONSTRAINT telegram_messages_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE SET NULL;


--
-- Name: transaction_tags transaction_tags_created_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.transaction_tags
    ADD CONSTRAINT transaction_tags_created_by_user_id_fkey FOREIGN KEY (created_by_user_id) REFERENCES public.users(id);


--
-- Name: transaction_tags transaction_tags_tag_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.transaction_tags
    ADD CONSTRAINT transaction_tags_tag_id_fkey FOREIGN KEY (tag_id) REFERENCES public.tags(id) ON DELETE CASCADE;


--
-- Name: transaction_tags transaction_tags_transaction_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.transaction_tags
    ADD CONSTRAINT transaction_tags_transaction_id_fkey FOREIGN KEY (transaction_id) REFERENCES public.transactions(id) ON DELETE CASCADE;


--
-- Name: transaction_tags transaction_tags_updated_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.transaction_tags
    ADD CONSTRAINT transaction_tags_updated_by_user_id_fkey FOREIGN KEY (updated_by_user_id) REFERENCES public.users(id);


--
-- Name: transactions transactions_account_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.transactions
    ADD CONSTRAINT transactions_account_id_fkey FOREIGN KEY (account_id) REFERENCES public.accounts(id);


--
-- Name: transactions transactions_category_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.transactions
    ADD CONSTRAINT transactions_category_id_fkey FOREIGN KEY (category_id) REFERENCES public.categories(id) ON DELETE SET NULL;


--
-- Name: transactions transactions_created_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.transactions
    ADD CONSTRAINT transactions_created_by_user_id_fkey FOREIGN KEY (created_by_user_id) REFERENCES public.users(id);


--
-- Name: transactions transactions_debt_prev_category_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.transactions
    ADD CONSTRAINT transactions_debt_prev_category_id_fkey FOREIGN KEY (debt_prev_category_id) REFERENCES public.categories(id) ON DELETE SET NULL;


--
-- Name: transactions transactions_source_personal_debt_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.transactions
    ADD CONSTRAINT transactions_source_personal_debt_id_fkey FOREIGN KEY (source_personal_debt_id) REFERENCES public.personal_debts(id) ON DELETE SET NULL;


--
-- Name: transactions transactions_split_person_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.transactions
    ADD CONSTRAINT transactions_split_person_id_fkey FOREIGN KEY (split_person_id) REFERENCES public.split_transaction_people(id) ON DELETE SET NULL;


--
-- Name: transactions transactions_split_transaction_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.transactions
    ADD CONSTRAINT transactions_split_transaction_id_fkey FOREIGN KEY (split_transaction_id) REFERENCES public.project_transactions(id) ON DELETE SET NULL;


--
-- Name: transactions transactions_updated_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.transactions
    ADD CONSTRAINT transactions_updated_by_user_id_fkey FOREIGN KEY (updated_by_user_id) REFERENCES public.users(id);


--
-- Name: transactions transactions_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.transactions
    ADD CONSTRAINT transactions_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id);


--
-- Name: user_avatars user_avatars_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_avatars
    ADD CONSTRAINT user_avatars_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: user_connections user_connections_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_connections
    ADD CONSTRAINT user_connections_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: user_notification_settings user_notification_settings_created_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_notification_settings
    ADD CONSTRAINT user_notification_settings_created_by_user_id_fkey FOREIGN KEY (created_by_user_id) REFERENCES public.users(id);


--
-- Name: user_notification_settings user_notification_settings_default_account_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_notification_settings
    ADD CONSTRAINT user_notification_settings_default_account_id_fkey FOREIGN KEY (default_account_id) REFERENCES public.accounts(id) ON DELETE SET NULL;


--
-- Name: user_notification_settings user_notification_settings_updated_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_notification_settings
    ADD CONSTRAINT user_notification_settings_updated_by_user_id_fkey FOREIGN KEY (updated_by_user_id) REFERENCES public.users(id);


--
-- Name: user_notification_settings user_notification_settings_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_notification_settings
    ADD CONSTRAINT user_notification_settings_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: user_pack_permissions user_pack_permissions_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_pack_permissions
    ADD CONSTRAINT user_pack_permissions_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: user_preferences user_preferences_created_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_preferences
    ADD CONSTRAINT user_preferences_created_by_user_id_fkey FOREIGN KEY (created_by_user_id) REFERENCES public.users(id);


--
-- Name: user_preferences user_preferences_updated_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_preferences
    ADD CONSTRAINT user_preferences_updated_by_user_id_fkey FOREIGN KEY (updated_by_user_id) REFERENCES public.users(id);


--
-- Name: user_preferences user_preferences_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_preferences
    ADD CONSTRAINT user_preferences_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: users users_created_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_created_by_user_id_fkey FOREIGN KEY (created_by_user_id) REFERENCES public.users(id);


--
-- Name: users users_updated_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_updated_by_user_id_fkey FOREIGN KEY (updated_by_user_id) REFERENCES public.users(id);


--
-- PostgreSQL database dump complete
--



-- static data
INSERT INTO public.payment_providers (id, kind, scheme, code, name_th, name_en, short_name, color, created_at, updated_at) VALUES ('d2b411e2-870a-4c63-8eaf-63c4c705f2d9', 'bank', 'bot', '002', 'ธนาคารกรุงเทพ', 'Bangkok Bank', 'BBL', NULL, '2026-10-16 16:45:54.439976+00', '2026-10-24 18:12:50.570024+00');
INSERT INTO public.payment_providers (id, kind, scheme, code, name_th, name_en, short_name, color, created_at, updated_at) VALUES ('eff9eae8-7a1d-4323-a7f6-0749ae968a8e', 'bank', 'bot', '004', 'ธนาคารกสิกรไทย', 'Kasikornbank', 'KBANK', NULL, '2026-10-16 16:45:54.439976+00', '2026-10-26 08:16:51.108514+00');
INSERT INTO public.payment_providers (id, kind, scheme, code, name_th, name_en, short_name, color, created_at, updated_at) VALUES ('79efbcc0-f938-4115-b904-21ad31472d37', 'bank', 'bot', '006', 'ธนาคารกรุงไทย', 'Krungthai Bank', 'KTB', NULL, '2026-10-16 16:45:54.439976+00', '2026-10-31 07:43:01.811545+00');
INSERT INTO public.payment_providers (id, kind, scheme, code, name_th, name_en, short_name, color, created_at, updated_at) VALUES ('171126d8-3133-4b4f-b2b0-3eac35f5dd74', 'bank', 'bot', '014', 'ธนาคารไทยพาณิชย์', 'Siam Commercial Bank', 'SCB', NULL, '2026-10-16 16:45:54.439976+00', '2026-10-31 08:08:18.482452+00');
