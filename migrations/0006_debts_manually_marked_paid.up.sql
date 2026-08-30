-- 0006 up: record WHY a debt is paid.
--
-- Before this, MarkPaid set status='paid' exactly as a covering payment does,
-- so RecomputeDebtStatus could not tell an administrative close from a
-- payment-derived one. It protected both, which meant voiding the payment that
-- settled a debt left the debt reporting as fully paid.

ALTER TABLE debts
    ADD COLUMN manually_marked_paid BOOLEAN NOT NULL DEFAULT false;

-- Backfill. A debt already 'paid' whose live payments do not cover its amount
-- can only have reached that state administratively, so preserve the intent.
-- Debts genuinely covered by payments stay false, so a later void correctly
-- re-opens them.
UPDATE debts d
SET manually_marked_paid = true
WHERE d.status = 'paid'
  AND COALESCE((
        SELECT SUM(p.amount) FROM payments p
        WHERE p.debt_id = d.id AND p.deleted_at IS NULL
      ), 0) < d.amount;
