-- Employee ID on each user: asked when a user is added, unique (case-blind) when given. Existing users start blank.
ALTER TABLE app_user ADD COLUMN IF NOT EXISTS employee_id TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX IF NOT EXISTS app_user_employee_id_key ON app_user (upper(employee_id)) WHERE employee_id <> '';
