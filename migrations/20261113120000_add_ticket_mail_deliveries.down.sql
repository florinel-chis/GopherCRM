-- Rollback: Ticket comment mail deliveries (#219). The delivery records are
-- lost; no other table references this one.

DROP TABLE IF EXISTS ticket_mail_deliveries;
