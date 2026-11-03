-- Paper model (feat/paper, owner 2026-11-02/03): split bills and event bills
-- become one shared paper; each user's books follow it. Schema from the old
-- baseline to the paper baseline; the old bills are dropped, not converted.

drop trigger if exists "links_debt_bills" on "public"."personal_debt_bills";

drop trigger if exists "links_debts" on "public"."personal_debts";

drop trigger if exists "links_origin" on "public"."project_transactions";

drop trigger if exists "links_transactions" on "public"."transactions";

drop trigger if exists "project_transactions_locked" on "public"."project_transactions";

drop trigger if exists "split_people_locked" on "public"."split_transaction_people";

alter table "public"."personal_debt_bills" drop constraint "personal_debt_bills_member_id_fkey";

alter table "public"."personal_debt_bills" drop constraint "personal_debt_bills_pt_id_fkey";

alter table "public"."personal_debts" drop constraint "personal_debts_paid_for_transaction_id_fkey";

alter table "public"."personal_debts" drop constraint "personal_debts_source_project_transaction_id_fkey";

alter table "public"."personal_debts" drop constraint "personal_debts_source_transaction_id_fkey";

alter table "public"."personal_debts" drop constraint "personal_debts_split_transaction_id_fkey";

alter table "public"."personal_debts" drop constraint "personal_debts_unlinked_project_transaction_id_fkey";

alter table "public"."personal_debts" drop constraint "personal_debts_unlinked_split_transaction_id_fkey";

alter table "public"."project_members" drop constraint "project_members_one_scope";

alter table "public"."project_members" drop constraint "project_members_split_transaction_id_fkey";

alter table "public"."project_transactions" drop constraint "project_transactions_linked_transaction_id_fkey";

alter table "public"."project_transactions" drop constraint "project_transactions_paid_back_for_id_fkey";

alter table "public"."project_transactions" drop constraint "project_transactions_parent_project_transaction_id_fkey";

alter table "public"."project_transactions" drop constraint "project_transactions_record_user_id_fkey";

alter table "public"."project_transactions" drop constraint "project_transactions_transaction_member_id_fkey";

alter table "public"."project_transactions" drop constraint "project_transactions_unlinked_transaction_id_fkey";

alter table "public"."projects" drop constraint "projects_kind_check";

alter table "public"."projects" drop constraint "projects_kind_event";

alter table "public"."split_transaction_people" drop constraint "split_transaction_people_event_member_id_fkey";

alter table "public"."transactions" drop constraint "transactions_paid_by_check";

alter table "public"."transactions" drop constraint "transactions_project_id_fkey";

alter table "public"."transactions" drop constraint "transactions_project_id_implies_source";

alter table "public"."transactions" drop constraint "transactions_source_project_transaction_id_fkey";

alter table "public"."transactions" drop constraint "transactions_unlinked_project_transaction_id_fkey";

alter table "public"."transactions" drop constraint "transactions_unlinked_split_transaction_id_fkey";

alter table "public"."account_members" drop constraint "account_members_report_scope_check";

alter table "public"."account_members" drop constraint "account_members_role_check";

alter table "public"."accounts" drop constraint "accounts_status_check";

alter table "public"."accounts" drop constraint "accounts_type_check";

alter table "public"."budgets" drop constraint "budgets_end_kind_check";

alter table "public"."budgets" drop constraint "budgets_freq_check";

alter table "public"."budgets" drop constraint "budgets_status_check1";

alter table "public"."categories" drop constraint "categories_type_check";

alter table "public"."connection_link_codes" drop constraint "connection_link_codes_provider_check";

alter table "public"."connection_link_codes" drop constraint "connection_link_codes_purpose_check";

alter table "public"."contacts" drop constraint "contacts_status_check";

alter table "public"."notifications" drop constraint "notifications_type_check";

alter table "public"."payment_providers" drop constraint "payment_providers_kind_check";

alter table "public"."pending_transactions" drop constraint "pending_kind_target";

alter table "public"."pending_transactions" drop constraint "pending_transactions_kind_check";

alter table "public"."personal_debts" drop constraint "personal_debts_direction_check";

alter table "public"."personal_debts" drop constraint "personal_debts_kind_check";

alter table "public"."personal_debts" drop constraint "personal_debts_source_kind_check";

alter table "public"."project_members" drop constraint "project_members_role_check";

alter table "public"."project_members" drop constraint "project_members_status_check";

alter table "public"."project_transactions" drop constraint "project_transactions_payback_kind_check";

alter table "public"."project_transactions" drop constraint "project_transactions_type_check";

alter table "public"."projects" drop constraint "projects_status_check";

alter table "public"."recurring_rules" drop constraint "recurring_rules_debt_direction_check";

alter table "public"."recurring_rules" drop constraint "recurring_rules_debt_kind_check";

alter table "public"."recurring_rules" drop constraint "recurring_rules_end_kind_check";

alter table "public"."recurring_rules" drop constraint "recurring_rules_freq_check";

alter table "public"."recurring_rules" drop constraint "recurring_rules_kind_check";

alter table "public"."recurring_rules" drop constraint "recurring_rules_monthly_mode_check";

alter table "public"."recurring_rules" drop constraint "recurring_rules_status_check";

alter table "public"."recurring_rules" drop constraint "recurring_rules_type_check";

alter table "public"."saving_entries" drop constraint "saving_entries_from_bucket_check";

alter table "public"."saving_entries" drop constraint "saving_entries_kind_check";

alter table "public"."saving_entries" drop constraint "saving_entries_source_check";

alter table "public"."saving_entries" drop constraint "saving_entries_to_bucket_check";

alter table "public"."saving_goals" drop constraint "saving_goals_status_check";

alter table "public"."transactions" drop constraint "transactions_source_at_most_one";

alter table "public"."transactions" drop constraint "transactions_type_check";

alter table "public"."user_avatars" drop constraint "user_avatars_source_check";

alter table "public"."user_connections" drop constraint "user_connections_provider_check";

alter table "public"."users" drop constraint "users_created_via_check";

alter table "public"."users" drop constraint "users_status_check";

-- ---------------------------------------------------------------------------
-- The old split / event bills go (owner 2026-11-03: no data conversion).
-- Everything else stays: users, wallets, transactions, categories, tags,
-- budgets, recurring, savings, contacts, hand-made debts, events and members.
-- A transaction that was a bill's copy becomes a plain transaction (its
-- wallet balance unchanged); a debt made from a bill becomes a plain debt.
-- ---------------------------------------------------------------------------
UPDATE transactions
   SET split_transaction_id = NULL, split_person_id = NULL, project_id = NULL,
       source_project_transaction_id = NULL, unlinked_project_transaction_id = NULL,
       unlinked_split_transaction_id = NULL, paid_by_amount = NULL
 WHERE split_transaction_id IS NOT NULL OR split_person_id IS NOT NULL OR project_id IS NOT NULL
    OR source_project_transaction_id IS NOT NULL OR unlinked_project_transaction_id IS NOT NULL
    OR unlinked_split_transaction_id IS NOT NULL OR paid_by_amount IS NOT NULL;

UPDATE personal_debts
   SET split_transaction_id = NULL, unlinked_split_transaction_id = NULL,
       source_project_transaction_id = NULL, unlinked_project_transaction_id = NULL,
       source_transaction_id = NULL, paid_for_transaction_id = NULL, source_kind = NULL
 WHERE split_transaction_id IS NOT NULL OR unlinked_split_transaction_id IS NOT NULL
    OR source_project_transaction_id IS NOT NULL OR unlinked_project_transaction_id IS NOT NULL
    OR source_transaction_id IS NOT NULL OR paid_for_transaction_id IS NOT NULL OR source_kind IS NOT NULL;

DELETE FROM personal_debt_bills;
DELETE FROM event_settle_bookings;
DELETE FROM project_transaction_tags;
DELETE FROM split_transaction_people;
DELETE FROM project_transactions;
-- Split bills' hidden scopes: members with no event, and non-event "projects".
DELETE FROM project_members WHERE project_id IS NULL;
DELETE FROM projects WHERE kind <> 'event';
-- Notices of the old model (their types are gone).
DELETE FROM notifications WHERE type NOT IN ('contact_link_request','account_invite','project_invite','recurring_due','recurring_ending','recurring_auto_created','debt_disconnected','debt_repaid','event_completed','event_member_removed','event_deleted','bill_invite','bill_new','bill_changed','bill_removed','bill_owner_changed','bill_moved');

drop function if exists "public"."event_or_null"(p_scope uuid);

drop function if exists "public"."project_transaction_payers_locked_check"();

drop view if exists "public"."scopes";

drop function if exists "public"."split_bill_of"(p_pt uuid);

drop function if exists "public"."trg_links_debt_bills"();

drop function if exists "public"."trg_links_debts"();

drop function if exists "public"."trg_links_origin"();

drop function if exists "public"."trg_links_transactions"();

drop view if exists "public"."bill_pairs";

drop index if exists "public"."idx_personal_debt_bills_pt";

drop index if exists "public"."idx_personal_debts_paid_for_tx";

drop index if exists "public"."idx_personal_debts_source_pt";

drop index if exists "public"."idx_personal_debts_source_tx";

drop index if exists "public"."idx_personal_debts_split_tx";

drop index if exists "public"."idx_personal_debts_unlinked_pt";

drop index if exists "public"."idx_project_members_scope";

drop index if exists "public"."idx_project_members_split_tx";

drop index if exists "public"."idx_project_members_split_tx_user";

drop index if exists "public"."idx_project_transactions_linked_tx";

drop index if exists "public"."idx_project_transactions_paid_back_for";

drop index if exists "public"."idx_project_transactions_scope";

drop index if exists "public"."idx_project_tx_member";

drop index if exists "public"."idx_project_tx_recorder";

drop index if exists "public"."idx_transactions_project";

drop index if exists "public"."idx_transactions_source_pt";

drop index if exists "public"."idx_transactions_unlinked_pt";

drop index if exists "public"."project_transactions_parent_idx";

drop index if exists "public"."idx_personal_debt_bills_pair";

drop index if exists "public"."idx_projects_owner";

drop index if exists "public"."idx_split_people_bill_member";

drop index if exists "public"."idx_split_people_member";

create table "public"."project_my_defaults" (
    "project_id" uuid not null,
    "user_id" uuid not null,
    "category_id" uuid,
    "account_id" uuid,
    "created_at" timestamp with time zone not null default now(),
    "updated_at" timestamp with time zone not null default now()
);


create table "public"."split_person_contacts" (
    "split_person_id" uuid not null,
    "user_id" uuid not null,
    "contact_id" uuid not null,
    "created_at" timestamp with time zone not null default now()
);


alter table "public"."personal_debt_bills" drop column "member_id";

alter table "public"."personal_debt_bills" drop column "pt_id";

alter table "public"."personal_debt_bills" alter column "split_transaction_id" set not null;

alter table "public"."personal_debts" drop column "paid_for_transaction_id";

alter table "public"."personal_debts" drop column "source_project_transaction_id";

alter table "public"."personal_debts" drop column "source_transaction_id";

alter table "public"."personal_debts" drop column "split_transaction_id";

alter table "public"."personal_debts" drop column "unlinked_project_transaction_id";

alter table "public"."personal_debts" drop column "unlinked_split_transaction_id";

alter table "public"."project_members" drop column "name_match";

alter table "public"."project_members" drop column "scope_id";

alter table "public"."project_members" drop column "split_transaction_id";

alter table "public"."project_members" alter column "project_id" set not null;

-- scope_id is generated from paid_back_for_id: it goes first.
alter table "public"."project_transactions" drop column "scope_id";

alter table "public"."project_transactions" drop column "currency";

alter table "public"."project_transactions" drop column "linked_transaction_id";

alter table "public"."project_transactions" drop column "paid_back_for_id";

alter table "public"."project_transactions" drop column "parent_project_transaction_id";

alter table "public"."project_transactions" drop column "record_user_id";

alter table "public"."project_transactions" drop column "transaction_member_id";

alter table "public"."project_transactions" drop column "unlinked_transaction_id";

alter table "public"."project_transactions" add column "owner_user_id" uuid not null;

alter table "public"."project_transactions" add column "split_mode" character varying(10) not null default 'custom'::character varying;

alter table "public"."project_transactions" add column "version" integer not null default 1;

alter table "public"."projects" drop column "kind";

alter table "public"."split_transaction_people" drop column "disconnected_at";

alter table "public"."split_transaction_people" drop column "event_member_id";

alter table "public"."split_transaction_people" drop column "marked_at";

alter table "public"."split_transaction_people" add column "paid_back_at" timestamp with time zone;

alter table "public"."split_transaction_people" add column "paid_back_by_user_id" uuid;

alter table "public"."split_transaction_people" add column "project_member_id" uuid;

alter table "public"."split_transaction_people" add column "status" character varying(10) not null default 'joined'::character varying;

alter table "public"."transactions" drop column "paid_by_amount";

alter table "public"."transactions" drop column "project_id";

alter table "public"."transactions" drop column "source_project_transaction_id";

alter table "public"."transactions" drop column "unlinked_project_transaction_id";

alter table "public"."transactions" drop column "unlinked_split_transaction_id";

alter table "public"."transactions" add column "my_share" numeric(15,2);

alter table "public"."user_notification_settings" add column "auto_accept_repayments" boolean not null default false;

alter table "public"."user_notification_settings" add column "on_split_removed" character varying(10) not null default 'ask'::character varying;

CREATE INDEX idx_personal_debt_bills_paper ON public.personal_debt_bills USING btree (split_transaction_id);

CREATE INDEX idx_personal_debts_counterpart ON public.personal_debts USING btree (counterpart_debt_id) WHERE (counterpart_debt_id IS NOT NULL);

CREATE INDEX idx_project_transactions_owner ON public.project_transactions USING btree (owner_user_id);

CREATE UNIQUE INDEX idx_transactions_paper_person ON public.transactions USING btree (split_transaction_id, user_id) WHERE (split_transaction_id IS NOT NULL);

CREATE UNIQUE INDEX project_my_defaults_pkey ON public.project_my_defaults USING btree (project_id, user_id);

CREATE INDEX split_person_contacts_contact_idx ON public.split_person_contacts USING btree (contact_id);

CREATE UNIQUE INDEX split_person_contacts_pkey ON public.split_person_contacts USING btree (split_person_id, user_id);

CREATE UNIQUE INDEX idx_personal_debt_bills_pair ON public.personal_debt_bills USING btree (user_id, split_transaction_id, split_person_id);

CREATE INDEX idx_projects_owner ON public.projects USING btree (owner_user_id, status);

CREATE UNIQUE INDEX idx_split_people_bill_member ON public.split_transaction_people USING btree (split_transaction_id, project_member_id) WHERE (project_member_id IS NOT NULL);

CREATE INDEX idx_split_people_member ON public.split_transaction_people USING btree (project_member_id) WHERE (project_member_id IS NOT NULL);

alter table "public"."project_my_defaults" add constraint "project_my_defaults_pkey" PRIMARY KEY using index "project_my_defaults_pkey";

alter table "public"."split_person_contacts" add constraint "split_person_contacts_pkey" PRIMARY KEY using index "split_person_contacts_pkey";

alter table "public"."project_my_defaults" add constraint "project_my_defaults_account_id_fkey" FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE SET NULL not valid;

alter table "public"."project_my_defaults" validate constraint "project_my_defaults_account_id_fkey";

alter table "public"."project_my_defaults" add constraint "project_my_defaults_category_id_fkey" FOREIGN KEY (category_id) REFERENCES categories(id) ON DELETE SET NULL not valid;

alter table "public"."project_my_defaults" validate constraint "project_my_defaults_category_id_fkey";

alter table "public"."project_my_defaults" add constraint "project_my_defaults_project_id_fkey" FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE not valid;

alter table "public"."project_my_defaults" validate constraint "project_my_defaults_project_id_fkey";

alter table "public"."project_my_defaults" add constraint "project_my_defaults_user_id_fkey" FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE not valid;

alter table "public"."project_my_defaults" validate constraint "project_my_defaults_user_id_fkey";

alter table "public"."project_transactions" add constraint "project_transactions_owner_user_id_fkey" FOREIGN KEY (owner_user_id) REFERENCES users(id) not valid;

alter table "public"."project_transactions" validate constraint "project_transactions_owner_user_id_fkey";

alter table "public"."project_transactions" add constraint "project_transactions_split_mode_check" CHECK (((split_mode)::text = ANY (ARRAY[('equal'::character varying)::text, ('custom'::character varying)::text]))) not valid;

alter table "public"."project_transactions" validate constraint "project_transactions_split_mode_check";

alter table "public"."split_person_contacts" add constraint "split_person_contacts_contact_id_fkey" FOREIGN KEY (contact_id) REFERENCES contacts(id) ON DELETE CASCADE not valid;

alter table "public"."split_person_contacts" validate constraint "split_person_contacts_contact_id_fkey";

alter table "public"."split_person_contacts" add constraint "split_person_contacts_split_person_id_fkey" FOREIGN KEY (split_person_id) REFERENCES split_transaction_people(id) ON DELETE CASCADE not valid;

alter table "public"."split_person_contacts" validate constraint "split_person_contacts_split_person_id_fkey";

alter table "public"."split_person_contacts" add constraint "split_person_contacts_user_id_fkey" FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE not valid;

alter table "public"."split_person_contacts" validate constraint "split_person_contacts_user_id_fkey";

alter table "public"."split_transaction_people" add constraint "split_transaction_people_paid_back_by_user_id_fkey" FOREIGN KEY (paid_back_by_user_id) REFERENCES users(id) ON DELETE SET NULL not valid;

alter table "public"."split_transaction_people" validate constraint "split_transaction_people_paid_back_by_user_id_fkey";

alter table "public"."split_transaction_people" add constraint "split_transaction_people_project_member_id_fkey" FOREIGN KEY (project_member_id) REFERENCES project_members(id) ON DELETE SET NULL not valid;

alter table "public"."split_transaction_people" validate constraint "split_transaction_people_project_member_id_fkey";

alter table "public"."split_transaction_people" add constraint "split_transaction_people_status_check" CHECK (((status)::text = ANY (ARRAY[('invited'::character varying)::text, ('joined'::character varying)::text, ('declined'::character varying)::text, ('left'::character varying)::text]))) not valid;

alter table "public"."split_transaction_people" validate constraint "split_transaction_people_status_check";

alter table "public"."transactions" add constraint "transactions_my_share_check" CHECK ((my_share >= (0)::numeric)) not valid;

alter table "public"."transactions" validate constraint "transactions_my_share_check";

alter table "public"."user_notification_settings" add constraint "user_notification_settings_on_split_removed_check" CHECK (((on_split_removed)::text = ANY (ARRAY[('ask'::character varying)::text, ('keep'::character varying)::text, ('delete'::character varying)::text]))) not valid;

alter table "public"."user_notification_settings" validate constraint "user_notification_settings_on_split_removed_check";

alter table "public"."account_members" add constraint "account_members_report_scope_check" CHECK (((report_scope)::text = ANY (ARRAY[('none'::character varying)::text, ('own'::character varying)::text, ('all'::character varying)::text]))) not valid;

alter table "public"."account_members" validate constraint "account_members_report_scope_check";

alter table "public"."account_members" add constraint "account_members_role_check" CHECK (((role)::text = ANY (ARRAY[('owner'::character varying)::text, ('member'::character varying)::text]))) not valid;

alter table "public"."account_members" validate constraint "account_members_role_check";

alter table "public"."accounts" add constraint "accounts_status_check" CHECK (((status)::text = ANY (ARRAY[('active'::character varying)::text, ('archived'::character varying)::text]))) not valid;

alter table "public"."accounts" validate constraint "accounts_status_check";

alter table "public"."accounts" add constraint "accounts_type_check" CHECK (((type)::text = ANY (ARRAY[('cash'::character varying)::text, ('bank'::character varying)::text, ('e_wallet'::character varying)::text, ('credit_card'::character varying)::text, ('pay_later'::character varying)::text]))) not valid;

alter table "public"."accounts" validate constraint "accounts_type_check";

alter table "public"."budgets" add constraint "budgets_end_kind_check" CHECK (((end_kind)::text = ANY (ARRAY[('none'::character varying)::text, ('date'::character varying)::text, ('count'::character varying)::text]))) not valid;

alter table "public"."budgets" validate constraint "budgets_end_kind_check";

alter table "public"."budgets" add constraint "budgets_freq_check" CHECK (((freq)::text = ANY (ARRAY[('daily'::character varying)::text, ('weekly'::character varying)::text, ('monthly'::character varying)::text, ('yearly'::character varying)::text]))) not valid;

alter table "public"."budgets" validate constraint "budgets_freq_check";

alter table "public"."budgets" add constraint "budgets_status_check1" CHECK (((status)::text = ANY (ARRAY[('active'::character varying)::text, ('archived'::character varying)::text]))) not valid;

alter table "public"."budgets" validate constraint "budgets_status_check1";

alter table "public"."categories" add constraint "categories_type_check" CHECK (((type)::text = ANY (ARRAY[('income'::character varying)::text, ('expense'::character varying)::text]))) not valid;

alter table "public"."categories" validate constraint "categories_type_check";

alter table "public"."connection_link_codes" add constraint "connection_link_codes_provider_check" CHECK (((provider)::text = ANY (ARRAY[('telegram'::character varying)::text, ('line'::character varying)::text, ('google'::character varying)::text]))) not valid;

alter table "public"."connection_link_codes" validate constraint "connection_link_codes_provider_check";

alter table "public"."connection_link_codes" add constraint "connection_link_codes_purpose_check" CHECK (((purpose)::text = ANY (ARRAY[('link'::character varying)::text, ('login'::character varying)::text]))) not valid;

alter table "public"."connection_link_codes" validate constraint "connection_link_codes_purpose_check";

alter table "public"."contacts" add constraint "contacts_status_check" CHECK (((status)::text = ANY (ARRAY[('active'::character varying)::text, ('archived'::character varying)::text]))) not valid;

alter table "public"."contacts" validate constraint "contacts_status_check";

alter table "public"."notifications" add constraint "notifications_type_check" CHECK (((type)::text = ANY (ARRAY[('contact_link_request'::character varying)::text, ('account_invite'::character varying)::text, ('project_invite'::character varying)::text, ('recurring_due'::character varying)::text, ('recurring_ending'::character varying)::text, ('recurring_auto_created'::character varying)::text, ('debt_disconnected'::character varying)::text, ('debt_repaid'::character varying)::text, ('event_completed'::character varying)::text, ('event_member_removed'::character varying)::text, ('event_deleted'::character varying)::text, ('bill_invite'::character varying)::text, ('bill_new'::character varying)::text, ('bill_changed'::character varying)::text, ('bill_removed'::character varying)::text, ('bill_owner_changed'::character varying)::text, ('bill_moved'::character varying)::text]))) not valid;

alter table "public"."notifications" validate constraint "notifications_type_check";

alter table "public"."payment_providers" add constraint "payment_providers_kind_check" CHECK (((kind)::text = ANY (ARRAY[('bank'::character varying)::text, ('e_wallet'::character varying)::text, ('card_issuer'::character varying)::text, ('other'::character varying)::text]))) not valid;

alter table "public"."payment_providers" validate constraint "payment_providers_kind_check";

alter table "public"."pending_transactions" add constraint "pending_kind_target" CHECK (((((kind)::text = ANY (ARRAY[('create'::character varying)::text, ('saving'::character varying)::text])) AND (target_debt_id IS NULL) AND (target_transaction_id IS NULL)) OR (((kind)::text = 'settle_debt'::text) AND (target_debt_id IS NOT NULL)) OR (((kind)::text = 'update_tx'::text) AND (target_transaction_id IS NOT NULL)))) not valid;

alter table "public"."pending_transactions" validate constraint "pending_kind_target";

alter table "public"."pending_transactions" add constraint "pending_transactions_kind_check" CHECK (((kind)::text = ANY (ARRAY[('create'::character varying)::text, ('settle_debt'::character varying)::text, ('update_tx'::character varying)::text, ('saving'::character varying)::text]))) not valid;

alter table "public"."pending_transactions" validate constraint "pending_transactions_kind_check";

alter table "public"."personal_debts" add constraint "personal_debts_direction_check" CHECK (((direction)::text = ANY (ARRAY[('paid'::character varying)::text, ('received'::character varying)::text]))) not valid;

alter table "public"."personal_debts" validate constraint "personal_debts_direction_check";

alter table "public"."personal_debts" add constraint "personal_debts_kind_check" CHECK (((kind)::text = ANY (ARRAY[('debt'::character varying)::text, ('repayment'::character varying)::text]))) not valid;

alter table "public"."personal_debts" validate constraint "personal_debts_kind_check";

alter table "public"."personal_debts" add constraint "personal_debts_source_kind_check" CHECK (((source_kind IS NULL) OR ((source_kind)::text = 'paper'::text))) not valid;

alter table "public"."personal_debts" validate constraint "personal_debts_source_kind_check";

alter table "public"."project_members" add constraint "project_members_role_check" CHECK (((role)::text = ANY (ARRAY[('owner'::character varying)::text, ('contributor'::character varying)::text, ('viewer'::character varying)::text]))) not valid;

alter table "public"."project_members" validate constraint "project_members_role_check";

alter table "public"."project_members" add constraint "project_members_status_check" CHECK (((status)::text = ANY (ARRAY[('pending'::character varying)::text, ('active'::character varying)::text, ('left'::character varying)::text]))) not valid;

alter table "public"."project_members" validate constraint "project_members_status_check";

alter table "public"."project_transactions" add constraint "project_transactions_payback_kind_check" CHECK (((payback_kind)::text = ANY (ARRAY[('payback'::character varying)::text, ('loan'::character varying)::text]))) not valid;

alter table "public"."project_transactions" validate constraint "project_transactions_payback_kind_check";

alter table "public"."project_transactions" add constraint "project_transactions_type_check" CHECK (((type)::text = ANY (ARRAY[('expense'::character varying)::text, ('income'::character varying)::text, ('payback'::character varying)::text]))) not valid;

alter table "public"."project_transactions" validate constraint "project_transactions_type_check";

alter table "public"."projects" add constraint "projects_status_check" CHECK (((status)::text = ANY (ARRAY[('active'::character varying)::text, ('completed'::character varying)::text, ('cancelled'::character varying)::text, ('archived'::character varying)::text]))) not valid;

alter table "public"."projects" validate constraint "projects_status_check";

alter table "public"."recurring_rules" add constraint "recurring_rules_debt_direction_check" CHECK (((debt_direction)::text = ANY (ARRAY[('paid'::character varying)::text, ('received'::character varying)::text]))) not valid;

alter table "public"."recurring_rules" validate constraint "recurring_rules_debt_direction_check";

alter table "public"."recurring_rules" add constraint "recurring_rules_debt_kind_check" CHECK (((debt_kind)::text = ANY (ARRAY[('debt'::character varying)::text, ('repayment'::character varying)::text]))) not valid;

alter table "public"."recurring_rules" validate constraint "recurring_rules_debt_kind_check";

alter table "public"."recurring_rules" add constraint "recurring_rules_end_kind_check" CHECK (((end_kind)::text = ANY (ARRAY[('none'::character varying)::text, ('date'::character varying)::text, ('count'::character varying)::text]))) not valid;

alter table "public"."recurring_rules" validate constraint "recurring_rules_end_kind_check";

alter table "public"."recurring_rules" add constraint "recurring_rules_freq_check" CHECK (((freq)::text = ANY (ARRAY[('daily'::character varying)::text, ('weekly'::character varying)::text, ('monthly'::character varying)::text, ('yearly'::character varying)::text, ('dates'::character varying)::text]))) not valid;

alter table "public"."recurring_rules" validate constraint "recurring_rules_freq_check";

alter table "public"."recurring_rules" add constraint "recurring_rules_kind_check" CHECK (((kind)::text = ANY (ARRAY[('subscription'::character varying)::text, ('installment'::character varying)::text, ('income'::character varying)::text, ('reminder'::character varying)::text, ('saving'::character varying)::text, ('debt'::character varying)::text]))) not valid;

alter table "public"."recurring_rules" validate constraint "recurring_rules_kind_check";

alter table "public"."recurring_rules" add constraint "recurring_rules_monthly_mode_check" CHECK (((monthly_mode IS NULL) OR ((monthly_mode)::text = ANY (ARRAY[('day'::character varying)::text, ('weekday'::character varying)::text])))) not valid;

alter table "public"."recurring_rules" validate constraint "recurring_rules_monthly_mode_check";

alter table "public"."recurring_rules" add constraint "recurring_rules_status_check" CHECK (((status)::text = ANY (ARRAY[('active'::character varying)::text, ('paused'::character varying)::text, ('archived'::character varying)::text]))) not valid;

alter table "public"."recurring_rules" validate constraint "recurring_rules_status_check";

alter table "public"."recurring_rules" add constraint "recurring_rules_type_check" CHECK (((type)::text = ANY (ARRAY[('expense'::character varying)::text, ('income'::character varying)::text, ('transfer'::character varying)::text, ('saving'::character varying)::text, ('debt'::character varying)::text]))) not valid;

alter table "public"."recurring_rules" validate constraint "recurring_rules_type_check";

alter table "public"."saving_entries" add constraint "saving_entries_from_bucket_check" CHECK (((from_bucket)::text = ANY (ARRAY[('available'::character varying)::text, ('spare'::character varying)::text, ('goal'::character varying)::text]))) not valid;

alter table "public"."saving_entries" validate constraint "saving_entries_from_bucket_check";

alter table "public"."saving_entries" add constraint "saving_entries_kind_check" CHECK (((kind)::text = ANY (ARRAY[('save'::character varying)::text, ('withdraw'::character varying)::text, ('move'::character varying)::text]))) not valid;

alter table "public"."saving_entries" validate constraint "saving_entries_kind_check";

alter table "public"."saving_entries" add constraint "saving_entries_source_check" CHECK (((source)::text = ANY (ARRAY[('manual'::character varying)::text, ('recurring'::character varying)::text, ('pending'::character varying)::text]))) not valid;

alter table "public"."saving_entries" validate constraint "saving_entries_source_check";

alter table "public"."saving_entries" add constraint "saving_entries_to_bucket_check" CHECK (((to_bucket)::text = ANY (ARRAY[('available'::character varying)::text, ('spare'::character varying)::text, ('goal'::character varying)::text]))) not valid;

alter table "public"."saving_entries" validate constraint "saving_entries_to_bucket_check";

alter table "public"."saving_goals" add constraint "saving_goals_status_check" CHECK (((status)::text = ANY (ARRAY[('active'::character varying)::text, ('completed'::character varying)::text, ('archived'::character varying)::text]))) not valid;

alter table "public"."saving_goals" validate constraint "saving_goals_status_check";

alter table "public"."transactions" add constraint "transactions_source_at_most_one" CHECK (((source_personal_debt_id IS NULL) OR (split_transaction_id IS NULL))) not valid;

alter table "public"."transactions" validate constraint "transactions_source_at_most_one";

alter table "public"."transactions" add constraint "transactions_type_check" CHECK (((type)::text = ANY (ARRAY[('expense'::character varying)::text, ('income'::character varying)::text, ('transfer'::character varying)::text]))) not valid;

alter table "public"."transactions" validate constraint "transactions_type_check";

alter table "public"."user_avatars" add constraint "user_avatars_source_check" CHECK (((source)::text = ANY (ARRAY[('upload'::character varying)::text, ('telegram'::character varying)::text, ('line'::character varying)::text, ('google'::character varying)::text]))) not valid;

alter table "public"."user_avatars" validate constraint "user_avatars_source_check";

alter table "public"."user_connections" add constraint "user_connections_provider_check" CHECK (((provider)::text = ANY (ARRAY[('telegram'::character varying)::text, ('line'::character varying)::text, ('google'::character varying)::text]))) not valid;

alter table "public"."user_connections" validate constraint "user_connections_provider_check";

alter table "public"."users" add constraint "users_created_via_check" CHECK (((created_via)::text = ANY (ARRAY[('password'::character varying)::text, ('telegram'::character varying)::text, ('line'::character varying)::text, ('google'::character varying)::text]))) not valid;

alter table "public"."users" validate constraint "users_created_via_check";

alter table "public"."users" add constraint "users_status_check" CHECK (((status)::text = ANY (ARRAY[('active'::character varying)::text, ('inactive'::character varying)::text]))) not valid;

alter table "public"."users" validate constraint "users_status_check";

set check_function_bodies = off;

CREATE OR REPLACE FUNCTION public.avatar_src(p_user_id uuid, p_icon jsonb)
 RETURNS jsonb
 LANGUAGE sql
 STABLE
AS $function$
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
$function$
;

create or replace view "public"."bill_pairs" as  SELECT b.id AS pt_id,
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
   FROM ((project_transactions b
     JOIN split_transaction_people pa ON (((pa.split_transaction_id = b.id) AND (pa.paid > (0)::numeric))))
     JOIN split_transaction_people s ON (((s.split_transaction_id = b.id) AND (s.share > (0)::numeric) AND (s.id <> pa.id))))
  WHERE (round(((s.share * pa.paid) / b.amount), 2) > (0)::numeric);


CREATE OR REPLACE FUNCTION public.project_transactions_locked_check()
 RETURNS trigger
 LANGUAGE plpgsql
AS $function$
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
END $function$
;

CREATE OR REPLACE FUNCTION public.recurring_skip_on_debt_delete()
 RETURNS trigger
 LANGUAGE plpgsql
AS $function$
BEGIN
    UPDATE recurring_items
    SET archived = TRUE, edited = TRUE, debt_entry_id = NULL, produced_at = NULL, auto_created_at = NULL,
        cleared_at = NULL,
        draft = jsonb_set(draft, '{amount}', to_jsonb(OLD.amount))
    WHERE debt_entry_id = OLD.id;
    RETURN OLD;
END;
$function$
;

CREATE OR REPLACE FUNCTION public.set_timestamp()
 RETURNS trigger
 LANGUAGE plpgsql
AS $function$
BEGIN
  NEW.updated_at = NOW();
  RETURN NEW;
END;
$function$
;

CREATE OR REPLACE FUNCTION public.split_people_locked()
 RETURNS trigger
 LANGUAGE plpgsql
AS $function$
BEGIN
    IF EXISTS (SELECT 1 FROM project_transactions b JOIN projects p ON p.id = b.project_id
                WHERE b.id = COALESCE(NEW.split_transaction_id, OLD.split_transaction_id)
                  AND p.completed_at IS NOT NULL FOR SHARE OF p) THEN
        RAISE EXCEPTION 'EVENT_COMPLETED' USING ERRCODE = 'P0001', HINT = 'event_completed';
    END IF;
    RETURN COALESCE(NEW, OLD);
END $function$
;

CREATE OR REPLACE FUNCTION public.trg_sync_split_people_member()
 RETURNS trigger
 LANGUAGE plpgsql
AS $function$
BEGIN
    UPDATE split_transaction_people
       SET name = NEW.display_name, user_id = COALESCE(NEW.user_id, user_id)
     WHERE project_member_id = NEW.id
       AND (name IS DISTINCT FROM NEW.display_name OR (NEW.user_id IS NOT NULL AND user_id IS DISTINCT FROM NEW.user_id));
    RETURN NEW;
END $function$
;

CREATE TRIGGER set_timestamp_project_my_defaults BEFORE UPDATE ON public.project_my_defaults FOR EACH ROW EXECUTE FUNCTION set_timestamp();

CREATE TRIGGER project_transactions_locked BEFORE INSERT OR DELETE OR UPDATE OF project_id, type, payback_kind, amount, date, description, note, split_mode ON public.project_transactions FOR EACH ROW EXECUTE FUNCTION project_transactions_locked_check();

CREATE TRIGGER split_people_locked BEFORE INSERT OR DELETE OR UPDATE OF share, paid, paid_back, project_member_id ON public.split_transaction_people FOR EACH ROW EXECUTE FUNCTION split_people_locked();


