-- +goose Up

-- Allow 'mock' as a valid llm_providers.type so test/e2e environments can
-- register a built-in mock provider that requires no external API key.
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

    EXECUTE $_$ALTER TABLE llm_providers ADD CONSTRAINT llm_providers_type_check
        CHECK (type IN ('openai', 'anthropic', 'openai-compat', 'mock'))$_$;
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

    EXECUTE $_$ALTER TABLE llm_providers ADD CONSTRAINT llm_providers_type_check
        CHECK (type IN ('openai', 'anthropic', 'openai-compat'))$_$;
END;
$$;
-- +goose StatementEnd
