-- 0007 up: currency and collection target on the business.
--
-- Until now the API had no notion of currency at all — the frontend hard-coded
-- the Naira symbol. CredFlow serves African SMEs across several currencies, so
-- the business carries one and every amount it owns is denominated in it.
-- Holding it at the business (rather than per debt) keeps every aggregate valid
-- without needing FX rates.

ALTER TABLE businesses
    ADD COLUMN currency CHAR(3) NOT NULL DEFAULT 'NGN'
        CHECK (currency IN ('NGN', 'GHS', 'KES', 'ZAR', 'USD')),

    -- Nullable on purpose: a collection target is a business decision, not
    -- something the API should invent. NULL means "not set", and the analytics
    -- endpoint returns null so the frontend hides its target line.
    ADD COLUMN monthly_collection_target NUMERIC(14, 2)
        CHECK (monthly_collection_target IS NULL OR monthly_collection_target >= 0);
