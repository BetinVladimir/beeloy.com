DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM auth_identities WHERE provider IN ('google','facebook')) THEN
        RAISE EXCEPTION '0013 down: google/facebook identities exist; refusing to delete them';
    END IF;
    IF EXISTS (SELECT 1 FROM comments WHERE status = 'deleted' AND body = '') THEN
        RAISE EXCEPTION '0013 down: deleted comments with empty body exist; the previous constraint would be violated';
    END IF;
END $$;
ALTER TABLE auth_identities DROP CONSTRAINT auth_identities_provider_check;
ALTER TABLE auth_identities ADD CONSTRAINT auth_identities_provider_check CHECK (provider IN ('email_otp'));
ALTER TABLE comments DROP CONSTRAINT comments_body_check;
ALTER TABLE comments ADD CONSTRAINT comments_body_check CHECK (char_length(body) <= 2000);
