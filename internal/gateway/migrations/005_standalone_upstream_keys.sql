-- Carry inherited settings onto each key before removing organization ownership.
UPDATE upstream_keys k SET
 enabled=legacy.enabled,
 quota_blocked=legacy.quota_blocked,
 last_error=legacy.last_error,
 probe_model=legacy.probe_model,
 probe_failures=legacy.probe_failures,
 next_probe_at=legacy.next_probe_at
FROM (
 SELECT k.id,k.enabled AND o.enabled AND coalesce(p.enabled,true) AS enabled,
  k.quota_blocked OR o.quota_blocked OR coalesce(p.quota_blocked,false) AS quota_blocked,
  CASE WHEN o.quota_blocked THEN o.last_error WHEN coalesce(p.quota_blocked,false) THEN p.last_error ELSE k.last_error END AS last_error,
  coalesce(nullif(k.probe_model,''),o.probe_model) AS probe_model,
  greatest(k.probe_failures,CASE WHEN o.quota_blocked THEN o.probe_failures ELSE 0 END,CASE WHEN coalesce(p.quota_blocked,false) THEN p.probe_failures ELSE 0 END) AS probe_failures,
  CASE WHEN o.quota_blocked THEN coalesce(o.next_probe_at,k.next_probe_at,now()+interval '5 minutes') WHEN coalesce(p.quota_blocked,false) THEN coalesce(p.next_probe_at,k.next_probe_at,now()+interval '5 minutes') ELSE k.next_probe_at END AS next_probe_at
 FROM upstream_keys k JOIN organizations o ON o.id=k.org_id
 LEFT JOIN upstream_projects p ON p.org_id=k.org_id AND p.upstream_id=k.upstream_project_id
) legacy WHERE legacy.id=k.id;

INSERT INTO cooldowns(scope,scope_id,model,until_at)
 SELECT 'key',k.id,c.model,max(c.until_at)
 FROM upstream_keys k
 LEFT JOIN upstream_projects p ON p.org_id=k.org_id AND p.upstream_id=k.upstream_project_id
 JOIN cooldowns c ON (c.scope='organization' AND c.scope_id=k.org_id) OR (c.scope='project' AND c.scope_id=p.id)
 GROUP BY k.id,c.model
 ON CONFLICT(scope,scope_id,model) DO UPDATE SET until_at=greatest(cooldowns.until_at,EXCLUDED.until_at);
DELETE FROM cooldowns WHERE scope<>'key';

ALTER TABLE upstream_keys DROP COLUMN org_id;
ALTER TABLE upstream_keys DROP COLUMN upstream_api_key_id;

-- Preserve historical metadata/costs for upgrades, but remove saved Admin credentials.
ALTER TABLE organizations DROP COLUMN admin_secret;
ALTER TABLE organizations RENAME TO archived_organizations;
ALTER TABLE upstream_projects RENAME TO archived_upstream_projects;
ALTER TABLE official_buckets RENAME TO archived_official_buckets;
