-- 019: no Purchase Team step. The final approval of a request raises its PO (Flow Settings, on by default).
INSERT INTO setting (key, value) VALUES ('po_on_approval', 'true') ON CONFLICT (key) DO NOTHING;
