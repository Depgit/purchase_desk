-- 029 (19 Oct 2026):
--  CFO: any request or bill whose total incl. GST is above the Flow Settings limit (cfo_above, 0 = off) goes to the
--    CFO first, then to its usual approvers.
--  Warehouse Manager retired: Warehouse Approver L2 takes over stock, opening stock, delivery scheduling, dispatch,
--    cannot-supply and receiving at the warehouse; each Warehouse Manager grant becomes Warehouse Staff (same scope).
--    At a pre-opening property the requester or the Pre-opening Team receives a vendor's goods.
--  Expense heads list the sub-categories they cover, so a corporate line's head follows its item.
--  A request approved over its budget carries the approver's reason.
INSERT INTO role (code, name, step, rights, group_id) VALUES
 ('cfo', 'CFO', 'cfo', '{approve}', (SELECT id FROM role_group WHERE name='Head office'))
ON CONFLICT DO NOTHING;
INSERT INTO setting (key, value) VALUES ('cfo_above', '100000') ON CONFLICT (key) DO NOTHING;

UPDATE role SET rights = array_append(rights, 'receive') WHERE step IN ('whl2', 'preop') AND NOT 'receive' = ANY(rights);

INSERT INTO user_role (user_id, role_id, scope, cluster_ids, property_ids, category_ids, created_by)
SELECT ur.user_id, (SELECT id FROM role WHERE code='whs'), ur.scope, ur.cluster_ids, ur.property_ids, ur.category_ids, ur.created_by
FROM user_role ur JOIN role r ON r.id=ur.role_id
WHERE r.step='wh' AND EXISTS (SELECT 1 FROM role WHERE code='whs')
ON CONFLICT (user_id, role_id) DO NOTHING;
DELETE FROM user_role WHERE role_id IN (SELECT id FROM role WHERE step='wh');
UPDATE role SET active=FALSE, inactive_at=now(), inactive_reason='Retired: Warehouse Approver L2 and Warehouse Staff do this work' WHERE step='wh';

ALTER TABLE expense_head ADD COLUMN IF NOT EXISTS sub_category_ids BIGINT[] NOT NULL DEFAULT '{}';

ALTER TABLE document ADD COLUMN IF NOT EXISTS over_budget_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE document ADD COLUMN IF NOT EXISTS over_budget_note TEXT NOT NULL DEFAULT '';
ALTER TABLE document ADD COLUMN IF NOT EXISTS over_budget_by TEXT NOT NULL DEFAULT '';
INSERT INTO pick_list (name, entries) VALUES
 ('over_budget_reasons', '{"Occupancy higher than planned","Event or banquet","Urgent repair or replacement","Price increase","Stock-up ahead of season","Other (see note)"}')
ON CONFLICT (name) DO NOTHING;
