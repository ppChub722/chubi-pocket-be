-- One-way: the split rows, payers and opt-outs are gone. Restore the backup taken before the deploy.
DO $$ BEGIN RAISE EXCEPTION 'migration 129 (split contract) cannot be undone: restore the pre-deploy backup'; END $$;
