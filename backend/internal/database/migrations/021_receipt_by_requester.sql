-- 021: goods are received by the person who raised the request (Flow Settings, on by default); anyone given the
-- Receiving / Store role at the property can still receive.
INSERT INTO setting (key, value) VALUES ('receipt_by_requester', 'true') ON CONFLICT (key) DO NOTHING;
