-- Migration: Remove deals and their stage history
-- Date: 2026-09-30
-- Description: Drops the two tables added by the up migration. Nothing else
-- was touched by it, so nothing else is restored. Deals hold no personal
-- data; the rows are business records lost by this rollback.

DROP TABLE deal_stage_changes;
DROP TABLE deals;
