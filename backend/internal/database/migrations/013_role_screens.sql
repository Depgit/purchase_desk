-- Roles grow into the full access record:
-- screens   each screen's level (none, view, edit) set on the role; a screen not set takes its starting level from
--           the role's rights and step (screens.Defaults), which matches how the app behaved before.
-- reports   the reports the role sees; NULL means every report (the starting point for existing roles).
-- can_users the role may add and edit users, giving only user_role_ids, inside its own properties (user_scope own)
--           or at any property (any). This replaces the per-user app_user.creatable_role_ids of migration 011,
--           which no user had set.
-- can_roles the role may create and edit roles, never above its own levels. Only the System Admin grants it.
-- Roles are listed in groups (role_group).
CREATE TABLE IF NOT EXISTS role_group (
  id         BIGSERIAL PRIMARY KEY,
  name       TEXT NOT NULL,
  sort       INT NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS role_group_name_key ON role_group ((norm_key(name)));
INSERT INTO role_group (name, sort) VALUES ('Head office', 1), ('Property', 2), ('Warehouse', 3), ('Corporate office', 4)
ON CONFLICT DO NOTHING;

ALTER TABLE role ADD COLUMN IF NOT EXISTS group_id BIGINT REFERENCES role_group(id);
ALTER TABLE role ADD COLUMN IF NOT EXISTS screens JSONB NOT NULL DEFAULT '{}';
ALTER TABLE role ADD COLUMN IF NOT EXISTS reports TEXT[];
ALTER TABLE role ADD COLUMN IF NOT EXISTS can_users BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE role ADD COLUMN IF NOT EXISTS user_role_ids BIGINT[] NOT NULL DEFAULT '{}';
ALTER TABLE role ADD COLUMN IF NOT EXISTS user_scope TEXT NOT NULL DEFAULT 'own' CHECK (user_scope IN ('own', 'any'));
ALTER TABLE role ADD COLUMN IF NOT EXISTS can_roles BOOLEAN NOT NULL DEFAULT FALSE;

UPDATE role SET group_id = (SELECT id FROM role_group WHERE name = CASE
    WHEN step IN ('staff', 'l1', 'l2', 'store') THEN 'Property'
    WHEN step = 'wh' THEN 'Warehouse'
    WHEN step IN ('creq', 'dh', 'capp') THEN 'Corporate office'
    ELSE 'Head office' END)
WHERE group_id IS NULL;

ALTER TABLE app_user DROP COLUMN IF EXISTS creatable_role_ids;
