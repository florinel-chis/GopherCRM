-- Ticket comments table
CREATE TABLE IF NOT EXISTS ticket_comments (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    created_at datetime(3) NULL,
    updated_at datetime(3) NULL,
    deleted_at datetime(3) NULL,
    ticket_id bigint unsigned NOT NULL,
    author_user_id bigint unsigned NOT NULL,
    body text NOT NULL,
    is_internal tinyint(1) NOT NULL,
    PRIMARY KEY (id),
    KEY idx_ticket_comments_deleted_at (deleted_at),
    KEY idx_ticket_comments_ticket_id (ticket_id),
    KEY idx_ticket_comments_author_user_id (author_user_id),
    CONSTRAINT fk_ticket_comments_ticket FOREIGN KEY (ticket_id) REFERENCES tickets(id),
    CONSTRAINT fk_ticket_comments_author FOREIGN KEY (author_user_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
