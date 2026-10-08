INSERT INTO role (code, name, step, rights) VALUES
 ('staff',    'Property Staff',           'staff',    '{raise}'),
 ('l1',       'Property Manager (L1)',    'l1',       '{raise}'),
 ('l2',       'Cluster Manager (L2)',     'l2',       '{approve}'),
 ('cc',       'Cost Controller',          'cc',       '{approve}'),
 ('purchase', 'Purchase Team',            'purchase', '{raise,issue_po,masters,upload}'),
 ('store',    'Receiving / Store',        'store',    '{receive}'),
 ('ap',       'Accounts Payable',         'ap',       '{match}'),
 ('finance',  'Finance',                  'finance',  '{pay}'),
 ('wh',       'Warehouse Manager',        'wh',       '{raise,receive,upload}'),
 ('preop',    'Pre-opening Team',         'preop',    '{approve}'),
 ('creq',     'Corporate Requester',      'creq',     '{raise,receive}'),
 ('dh',       'Department Head (L1)',     'dh',       '{raise,approve,receive}'),
 ('capp',     'Corporate Approver (L2)',  'capp',     '{approve}'),
 ('admin',    'System Admin',             'admin',    '{raise,approve,issue_po,receive,match,pay,masters,upload}');

INSERT INTO setting (key, value) VALUES
 ('evt_l2',          'true'),
 ('l2_above',        '0'),
 ('cc_all',          'true'),
 ('wh_approval',     'true'),
 ('qty_up',          'true'),
 ('week_flag',       'true'),
 ('bill_approver',   '"l2"'),
 ('bill_jump_pct',   '15'),
 ('amc_warn_days',   '90'),
 ('amc_durations',   '[3,6,12]'),
 ('match_tol_pct',   '2'),
 ('msme_days',       '45'),
 ('need_schedule',   'true'),
 ('sheet_approved',  'true'),
 ('corp_l1_only_below', '0');

INSERT INTO pick_list (name, entries) VALUES
 ('event_reasons',  '{"Banquet or party booking","Group check-in","Festival or peak occupancy","Other"}'),
 ('return_reasons', '{"Quantity too high","Rate higher than last purchase","Wrong vendor selected","Wrong item or category","Not needed this month","Duplicate request","Other (see remark)"}'),
 ('reject_reasons', '{"Not required","Budget exhausted","Duplicate request","Other (see remark)"}'),
 ('route_reasons',  '{"Large quantity, confirm the event or booking","Over budget, needs the cluster view","Unusual item for this property","Other (see remark)"}'),
 ('wh_back_reasons','{"Out of stock, no date for new stock","Item no longer kept in the warehouse","Quantity too large for warehouse stock","Other (see remark)"}'),
 ('delivery_slots', '{"8 am to 11 am","11 am to 2 pm","2 pm to 5 pm","5 pm to 8 pm"}');

INSERT INTO message_template (code, name, channel, body) VALUES
 ('po',  'PO to vendor', 'email', E'Dear {vendor} team,\n\nPlease find purchase order {po_number} for {property}, value {value} incl. GST. Deliver by {due}.\nPlease supply only against this PO number and quote it on your invoice.\n\nRegards,\nSaltstayz Purchase Team'),
 ('vse', 'Vendor order sheet', 'email', E'Dear {vendor} team,\n\nPlease find attached order sheet version {version}.\n\nConfirmed: {po_count} purchase orders for {properties} properties, value {value} incl. GST. Deliver by: {due}.\n{pending_line}\nPlease supply only against confirmed PO numbers and reply to confirm the delivery dates.\n\nRegards,\nSaltstayz Purchase Team'),
 ('vsw', 'Vendor order sheet', 'whatsapp', E'Saltstayz order sheet v{version} for {vendor}\nConfirmed: {po_count} POs, {properties} properties, {value}. Deliver by {due}.\n{pending_line}\nExcel attached. Supply only against confirmed PO numbers. Please reply OK to confirm.'),
 ('ok',  'Request approved', 'whatsapp', '{request} for {property} is approved by {by}. Value {value}.'),
 ('mod', 'Request approved with changes', 'whatsapp', '{request} for {property} is approved by {by} with changed quantities. Please open the request to see what changed.'),
 ('ret', 'Request returned', 'whatsapp', '{request} for {property} is returned by {by}. Reason: {reason}. Please correct and resubmit.'),
 ('rej', 'Request rejected', 'whatsapp', '{request} for {property} is rejected by {by}. Reason: {reason}.'),
 ('dlv', 'Warehouse delivery scheduled', 'whatsapp', '{request} for {property}: delivery from the warehouse is scheduled for {date}, {slot}.'),
 ('pay', 'Payment released to vendor', 'email', E'Dear {vendor} team,\n\nPayment of {value} against invoice {invoice} is released.\n\nRegards,\nSaltstayz Finance'),
 ('otp', 'Sign-in code', 'whatsapp', 'Your Purchase Desk sign-in code is {code}. It is valid for 10 minutes.');
