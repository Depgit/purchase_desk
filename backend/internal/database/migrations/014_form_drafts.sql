-- Drafts of forms (purchase request, goods receipt invoice, utility/AMC bill, corporate purchase or bill), saved half-filled and
-- finished later. Each draft belongs to the person who saved it and nobody else sees it. form says which form;
-- ref_id is the document for a goods receipt draft. fields holds the form's fields as the browser had them; files
-- lists the attached files kept with it ({field, id, name}), stored like any other upload in file_object.
CREATE TABLE IF NOT EXISTS form_draft (
  id         BIGSERIAL PRIMARY KEY,
  user_id    BIGINT NOT NULL REFERENCES app_user(id),
  form       TEXT NOT NULL CHECK (form IN ('grn','bill','corporate','request')),
  ref_id     BIGINT,
  title      TEXT NOT NULL DEFAULT '',
  fields     JSONB NOT NULL DEFAULT '{}',
  files      JSONB NOT NULL DEFAULT '[]',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS form_draft_user ON form_draft(user_id, form, ref_id);
