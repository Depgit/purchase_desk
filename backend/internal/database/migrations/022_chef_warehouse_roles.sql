-- 022: kitchen and warehouse roles.
--  Property Chef: raises (and receives) requests at their property; the Admin limits the grant to the F&B categories,
--    or leaves it open for all items.
--  Head Chef: sees everything at their properties (several, like a Cluster Manager); no approving.
--  Warehouse Staff: raises (and receives) at the warehouse; the Warehouse Manager approves those requests.
INSERT INTO role (code, name, step, rights, group_id) VALUES
 ('chef',  'Property Chef',   'chef',  '{raise,receive}', (SELECT id FROM role_group WHERE name='Property')),
 ('hchef', 'Head Chef',       'hchef', '{}',              (SELECT id FROM role_group WHERE name='Property')),
 ('whs',   'Warehouse Staff', 'whs',   '{raise,receive}', (SELECT id FROM role_group WHERE name='Warehouse'))
ON CONFLICT DO NOTHING;
-- The Warehouse Manager now approves warehouse requests (raised by Warehouse Staff); it may still raise for a
-- pre-opening property.
UPDATE role SET rights = array_append(rights, 'approve') WHERE step = 'wh' AND NOT 'approve' = ANY(rights);
