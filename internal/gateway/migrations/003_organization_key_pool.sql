-- Preserve OpenAI project metadata and old quota/cooldown IDs separately.
ALTER TABLE projects RENAME TO upstream_projects;
ALTER TABLE organizations ADD COLUMN probe_model text NOT NULL DEFAULT '';
ALTER TABLE organizations ADD COLUMN admin_status text NOT NULL DEFAULT 'unchecked';
ALTER TABLE organizations ADD COLUMN admin_checked_at timestamptz;
ALTER TABLE organizations ADD COLUMN admin_error text NOT NULL DEFAULT '';
ALTER TABLE organizations ADD COLUMN projects_access text NOT NULL DEFAULT 'unchecked';
ALTER TABLE organizations ADD COLUMN costs_access text NOT NULL DEFAULT 'unchecked';
UPDATE organizations o SET probe_model=coalesce((SELECT p.probe_model FROM upstream_projects p WHERE p.org_id=o.id ORDER BY p.created_at LIMIT 1),'');
ALTER TABLE upstream_keys ADD COLUMN org_id text REFERENCES organizations(id);
ALTER TABLE upstream_keys ADD COLUMN upstream_project_id text NOT NULL DEFAULT '';
ALTER TABLE upstream_keys ADD COLUMN probe_model text NOT NULL DEFAULT '';
UPDATE upstream_keys k SET org_id=p.org_id,upstream_project_id=p.upstream_id FROM upstream_projects p WHERE k.project_id=p.id;
ALTER TABLE upstream_keys ALTER COLUMN org_id SET NOT NULL;
DROP INDEX upstream_official_id;
ALTER TABLE upstream_keys DROP COLUMN project_id;
CREATE UNIQUE INDEX upstream_official_id ON upstream_keys(org_id,upstream_api_key_id) WHERE upstream_api_key_id<>'';
CREATE INDEX upstream_key_org ON upstream_keys(org_id);
-- Gateway business projects have no upstream organization or key binding.
CREATE TABLE projects (
 id text PRIMARY KEY, name text NOT NULL, description text NOT NULL DEFAULT '',
 enabled boolean NOT NULL DEFAULT true, created_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO projects(id,name,enabled,created_at) SELECT id,name,enabled,created_at FROM upstream_projects;
