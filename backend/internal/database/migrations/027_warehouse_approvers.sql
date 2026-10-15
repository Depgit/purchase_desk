-- 027: warehouse purchases get two approvers. Warehouse Approver L1 approves the sub-categories ticked on their role at
-- the warehouse (user_role.sub_category_ids; none ticked = every sub-category), then Warehouse Approver L2 (the
-- Warehouse Manager may also act at L2). A request with items for different L1 approvers splits, one per L1.
ALTER TABLE user_role ADD COLUMN IF NOT EXISTS sub_category_ids BIGINT[] NOT NULL DEFAULT '{}';

INSERT INTO role (code, name, step, rights, group_id) VALUES
 ('whl1', 'Warehouse Approver L1', 'whl1', '{approve}', (SELECT id FROM role_group WHERE name='Warehouse')),
 ('whl2', 'Warehouse Approver L2', 'whl2', '{approve}', (SELECT id FROM role_group WHERE name='Warehouse'))
ON CONFLICT DO NOTHING;
