-- Retire two seeded settings nothing ever read (#224).
--
--   security.session_timeout_hours  session length comes from
--                                   JWT_EXPIRY_HOURS and
--                                   REFRESH_TOKEN_EXPIRY_DAYS
--   tickets.auto_assign_support     tickets are never distributed among
--                                   support users
--
-- The application deletes the same rows on every start
-- (models.RetiredConfigurationKeys), on every driver, so this migration is
-- safe on a database the new release has already booted against.
DELETE FROM configurations
WHERE config_key IN ('security.session_timeout_hours', 'tickets.auto_assign_support');
