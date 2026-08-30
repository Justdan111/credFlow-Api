-- 0009 up: persist onboarding state on the business.
--
-- The onboarding flow lived entirely in frontend React state, so a page
-- refresh lost it and the API had no way to tell whether a user had finished.

ALTER TABLE businesses
    -- Nullable: NULL means not finished. A timestamp rather than a boolean
    -- records WHEN, which costs nothing extra and is worth having.
    ADD COLUMN onboarding_completed_at TIMESTAMPTZ,

    -- Mirrors the frontend's three step ids so a user who refreshes resumes
    -- where they left off instead of starting over.
    ADD COLUMN onboarding_step TEXT
        CHECK (onboarding_step IS NULL OR onboarding_step IN ('business', 'customer', 'debt'));

-- Any business that already has customers predates onboarding entirely and must
-- not be pushed back through it on next login.
UPDATE businesses b
SET onboarding_completed_at = b.created_at
WHERE EXISTS (SELECT 1 FROM customers c WHERE c.business_id = b.id);
