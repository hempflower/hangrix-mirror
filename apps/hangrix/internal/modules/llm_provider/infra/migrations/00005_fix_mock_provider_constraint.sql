-- +goose Up

-- Add 'mock' to the llm_providers.type CHECK constraint as a forward migration.
--
-- NOTE: The preceding 00004_add_mock_provider_type.sql was intended to do this
-- but contained nested $$ dollar-quoting that prevents execution. This migration
-- is a standalone, idempotent fix — it drops and re-adds the constraint with
-- 'mock' included, and is safe to run whether 00004 succeeded or not.
--
-- If 00004 never ran (the expected case), manually advance goose's version to 4
-- before applying this migration:
--   UPDATE goose_db_version SET version_id = 4 WHERE id = 1;
-- Or equivalently: goose -dir ... up-by-one 4 times, accepting the failure on 00004
-- and then running this migration normally.
-- +goose StatementBegin
DO $$
DECLARE
    constraint_name text;
BEGIN
    SELECT con.conname INTO constraint_name
    FROM pg_constraint con
    JOIN pg_class rel ON rel.oid = con.conrelid
    WHERE rel.relname = 'llm_providers'
      AND con.contype = 'c'
      AND pg_get_constraintdef(con.oid) LIKE 'CHECK%type%';

    IF constraint_name IS NOT NULL THEN
        EXECUTE format('ALTER TABLE llm_providers DROP CONSTRAINT %I', constraint_name);
    END IF;

    EXECUTE $alter$ALTER TABLE llm_providers ADD CONSTRAINT llm_providers_type_check
        CHECK (type IN ('openai', 'anthropic', 'openai-compat', 'mock'))$alter$;
END;
$$;
-- +goose StatementEnd

-- +goose Down

-- Revert to the original constraint set (without 'mock').
-- +goose StatementBegin
DO $$
DECLARE
    constraint_name text;
BEGIN
    SELECT con.conname INTO constraint_name
    FROM pg_constraint con
    JOIN pg_class rel ON rel.oid = con.conrelid
    WHERE rel.relname = 'llm_providers'
      AND con.contype = 'c'
      AND pg_get_constraintdef(con.oid) LIKE 'CHECK%type%';

    IF constraint_name IS NOT NULL THEN
        EXECUTE format('ALTER TABLE llm_providers DROP CONSTRAINT %I', constraint_name);
    END IF;

    EXECUTE $alter$ALTER TABLE llm_providers ADD CONSTRAINT llm_providers_type_check
        CHECK (type IN ('openai', 'anthropic', 'openai-compat'))$alter$;
END;
$$;
-- +goose StatementEnd
