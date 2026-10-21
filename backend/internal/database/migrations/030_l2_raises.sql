-- 030: Warehouse Approver L2 raises the warehouse's shortfall purchases (a job of the retired Warehouse Manager); the
-- L1 alone approves those.
UPDATE role SET rights = array_append(rights, 'raise') WHERE step = 'whl2' AND NOT 'raise' = ANY(rights);
