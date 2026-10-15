-- One vendor portal sign-in (an email and its password) can open several vendors: someone who looks after a few small
-- vendors signs in once and picks the vendor. vendor_account is that sign-in; vendor_login now only links each vendor
-- to its account (one account per vendor), and can be turned off per vendor. Existing sign-ins move over unchanged.
CREATE TABLE IF NOT EXISTS vendor_account (
  id            BIGSERIAL PRIMARY KEY,
  email         CITEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  must_change   BOOLEAN NOT NULL DEFAULT TRUE, -- the one-time password given by Saltstayz is changed at the first sign-in
  last_login_at TIMESTAMPTZ,
  created_by    BIGINT REFERENCES app_user(id),
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE vendor_login ADD COLUMN IF NOT EXISTS account_id BIGINT REFERENCES vendor_account(id);

INSERT INTO vendor_account (email, password_hash, must_change, last_login_at, created_by, created_at)
SELECT email, password_hash, must_change, last_login_at, created_by, created_at FROM vendor_login
ON CONFLICT (email) DO NOTHING;

UPDATE vendor_login l SET account_id = a.id FROM vendor_account a WHERE a.email = l.email AND l.account_id IS NULL;

ALTER TABLE vendor_login ALTER COLUMN account_id SET NOT NULL;
ALTER TABLE vendor_login DROP COLUMN IF EXISTS email, DROP COLUMN IF EXISTS password_hash, DROP COLUMN IF EXISTS must_change, DROP COLUMN IF EXISTS last_login_at;
CREATE INDEX IF NOT EXISTS vendor_login_account_idx ON vendor_login (account_id);
