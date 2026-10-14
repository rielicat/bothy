-- 0005_approval_comments.sql (v0.2 UI): the reason an approver gives with a
-- decision or a vote, and the start of a pending sender's first message for
-- the approvals inbox (cleared once the sender is approved or blocked).

ALTER TABLE approvals ADD COLUMN comment TEXT NOT NULL DEFAULT '';
ALTER TABLE approval_votes ADD COLUMN comment TEXT NOT NULL DEFAULT '';
ALTER TABLE channel_identities ADD COLUMN first_text TEXT NOT NULL DEFAULT '';
