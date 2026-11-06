-- Migration: Stop recording form mail deliveries
-- Date: 2026-11-06
-- Description: Drops form_mail_deliveries and nothing else. The delivery
-- history it held is lost; submissions, tokens and leads are untouched. The
-- application's AutoMigrate creates the table again on its next start.

DROP TABLE IF EXISTS form_mail_deliveries;
