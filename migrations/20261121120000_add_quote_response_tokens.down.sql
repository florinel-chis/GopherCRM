-- Migration: Remove quote response tokens
-- #382: drops the quote_response_tokens table and nothing else.

DROP TABLE IF EXISTS quote_response_tokens;
