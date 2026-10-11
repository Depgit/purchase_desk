-- The System Admin can let a user add users of chosen roles (Finance may add Property and Cluster Managers).
-- creatable_role_ids lists those roles; empty means the user cannot add users. Such a user sees, edits and
-- deactivates only the users they added (app_user.created_by), only with those roles and only for properties inside
-- their own access. Only the System Admin sets this list; users added by a delegate never get one.
ALTER TABLE app_user ADD COLUMN IF NOT EXISTS creatable_role_ids BIGINT[] NOT NULL DEFAULT '{}';
