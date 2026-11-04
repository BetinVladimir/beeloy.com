-- Corrective migration (ТЗ 02 «0013_review_fixes»): social providers and a strict comment body rule.
DO $$
DECLARE c record;
BEGIN
    FOR c IN SELECT conname FROM pg_constraint
             WHERE conrelid = 'auth_identities'::regclass AND contype = 'c' AND pg_get_constraintdef(oid) LIKE '%provider%' LOOP
        EXECUTE format('ALTER TABLE auth_identities DROP CONSTRAINT %I', c.conname);
    END LOOP;
    FOR c IN SELECT conname FROM pg_constraint
             WHERE conrelid = 'comments'::regclass AND contype = 'c' AND pg_get_constraintdef(oid) LIKE '%body%' LOOP
        EXECUTE format('ALTER TABLE comments DROP CONSTRAINT %I', c.conname);
    END LOOP;
END $$;

ALTER TABLE auth_identities ADD CONSTRAINT auth_identities_provider_check
    CHECK (provider IN ('email_otp','google','facebook'));

UPDATE comments SET body = '' WHERE status = 'deleted';
ALTER TABLE comments ADD CONSTRAINT comments_body_check CHECK (
    (status = 'deleted' AND body = '') OR
    (status IN ('visible','hidden') AND char_length(body) BETWEEN 1 AND 2000)
);
