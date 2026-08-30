-- 0009 down.

ALTER TABLE businesses
    DROP COLUMN IF EXISTS onboarding_step,
    DROP COLUMN IF EXISTS onboarding_completed_at;
