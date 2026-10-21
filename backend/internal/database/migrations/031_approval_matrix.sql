-- 031 (19 Oct 2026): every purchase gets an L1 and an L2, and amount approvals C1 / C2.
--  Approval matrix: for each live or pre-opening property and item category (purchases), or property and bill type
--    (utility and AMC bills), the L1 and L2 approver. They are fixed on a request when it is raised
--    (document.corp_l1_user_id / corp_l2_user_id, steps cl1 / cl2, as corporate departments already work). The
--    Cost Controller and the single pre-opening approver no longer approve new requests.
--  C1 / C2: after L2, a request above its type's C1 limit also goes to C1, above the C2 limit to C1 then C2
--    (setting amount_approvals, per type: property, preopening, warehouse, corporate, bills; 0 = off).
--  The CFO step of 029 is retired: pending requests waiting on it move to C2.
CREATE TABLE IF NOT EXISTS approval_matrix (
  id           BIGSERIAL PRIMARY KEY,
  property_id  BIGINT NOT NULL REFERENCES property(id),
  category_id  BIGINT REFERENCES category(id),
  bill_type_id BIGINT REFERENCES bill_type(id),
  l1_user_id   BIGINT REFERENCES app_user(id),
  l2_user_id   BIGINT REFERENCES app_user(id),
  updated_by   BIGINT REFERENCES app_user(id),
  updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK ((category_id IS NULL) <> (bill_type_id IS NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS approval_matrix_cat_uq ON approval_matrix (property_id, category_id) WHERE category_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS approval_matrix_bill_uq ON approval_matrix (property_id, bill_type_id) WHERE bill_type_id IS NOT NULL;

INSERT INTO role (code, name, step, rights, group_id) VALUES
 ('al1', 'Approver L1', 'al1', '{approve}', (SELECT id FROM role_group WHERE name='Property')),
 ('al2', 'Approver L2', 'al2', '{approve}', (SELECT id FROM role_group WHERE name='Property')),
 ('c1',  'Amount Approver C1', 'c1', '{approve}', (SELECT id FROM role_group WHERE name='Head office')),
 ('c2',  'Amount Approver C2', 'c2', '{approve}', (SELECT id FROM role_group WHERE name='Head office'))
ON CONFLICT DO NOTHING;

INSERT INTO setting (key, value) VALUES ('amount_approvals',
 '{"property":{"c1":0,"c2":0},"preopening":{"c1":0,"c2":0},"warehouse":{"c1":0,"c2":0},"corporate":{"c1":0,"c2":0},"bills":{"c1":0,"c2":0}}')
ON CONFLICT (key) DO NOTHING;

UPDATE document SET route_steps = array_replace(route_steps, 'cfo', 'c2') WHERE status='PENDING' AND 'cfo' = ANY(route_steps);
DELETE FROM user_role WHERE role_id IN (SELECT id FROM role WHERE step='cfo');
UPDATE role SET active=FALSE, inactive_at=now(), inactive_reason='Retired: amount approvals are C1 and C2' WHERE step='cfo';
DELETE FROM setting WHERE key='cfo_above';
