-- Migration: Undo API key lifetime (#532)
-- Date: 2026-11-29
-- Description: Changes nothing. The expiries set and the keys of customer accounts revoked by the up half stay as they are: the earlier values are not recorded, and a revoked key cannot be switched on again. MySQL 8 and MariaDB 10.11 accept this as written.

SELECT 1;
