DO $$
DECLARE
  clusters   TEXT[] := ARRAY['prod-eu', 'prod-us', 'staging', 'dev'];
  namespaces TEXT[] := ARRAY['krateo-system', 'demo-system', 'ns-1', 'ns-2'];
  titles     TEXT[] := ARRAY['Blueprints', 'Deployments', 'Costs', 'Pipelines', 'Alerts', 'Metrics', 'Logs', 'Clusters', 'Users', 'Quotas'];
  sections   TEXT[] := ARRAY['dashboard', 'overview', 'admin'];

  c TEXT;
  ns TEXT;
  title TEXT;
  sec TEXT;

  rep INT;
  ti INT;

  i INT := 0;            -- deterministic global counter
  ks_count INT := 0;     -- enforce exactly 5000 for krateo-system

  uid_val TEXT;
  name_val TEXT;
  global_uid_val TEXT;

  raw_val JSONB;
  status_raw_val JSONB;

  base_ts TIMESTAMPTZ := '2024-01-01 00:00:00+00';

BEGIN
  FOR rep IN 1..200 LOOP
    FOREACH c IN ARRAY clusters LOOP
      FOREACH ns IN ARRAY namespaces LOOP
        FOR ti IN 1..array_length(titles, 1) LOOP

          title := titles[ti];

          -- deterministic section
          sec := sections[1 + ((rep + ti) % array_length(sections, 1))];

          -- enforce 5000 only for krateo-system
          IF ns = 'krateo-system' AND ks_count >= 5000 THEN
            CONTINUE;
          END IF;

          -- deterministic counter
          i := i + 1;

          -- deterministic naming (stable across runs)
          name_val :=
            sec || '-' ||
            lower(replace(title, ' ', '-')) ||
            '-' || c ||
            '-' || ns ||
            '-panel-' || rep || '-' || ti;

          -- deterministic uid (NO randomness)
          uid_val := md5(c || '|' || ns || '|' || title || '|' || rep || '|' || ti);

          global_uid_val := c || ':' || uid_val;

          raw_val := jsonb_build_object(
            'apiVersion', 'widgets.templates.krateo.io/v1beta1',
            'kind',       'Panel',
            'metadata',   jsonb_build_object(
              'name',      name_val,
              'namespace', ns,
              'labels',    jsonb_build_object('app.kubernetes.io/part-of', sec),
              'annotations', jsonb_build_object(
                'krateo.io/verbose',
                ((rep + ti) % 2 = 0)::TEXT
              )
            ),
            'spec', jsonb_build_object(
              'widgetData', jsonb_build_object(
                'title',   title,
                'actions', '{}'::jsonb,
                'items',   jsonb_build_array(
                  jsonb_build_object('resourceRefId', name_val || '-row')
                )
              ),
              'resourcesRefs', jsonb_build_object(
                'items', jsonb_build_array(
                  jsonb_build_object(
                    'id',         name_val || '-row',
                    'apiVersion', 'widgets.templates.krateo.io/v1beta1',
                    'name',       name_val || '-row',
                    'namespace',  ns,
                    'resource',   'rows',
                    'verb',       'GET'
                  )
                )
              )
            )
          );

          -- deterministic status
          IF (rep + ti) % 2 = 0 THEN
            status_raw_val := jsonb_build_object(
              'conditions', jsonb_build_array(
                jsonb_build_object(
                  'type',   'Ready',
                  'status', 'True',
                  'lastTransitionTime',
                    to_char(
                      base_ts + (i || ' minutes')::INTERVAL,
                      'YYYY-MM-DD"T"HH24:MI:SS"Z"'
                    ),
                  'reason',  'Available',
                  'message', 'Resource is ready'
                )
              ),
              'observedGeneration', i
            );
          ELSE
            status_raw_val := NULL;
          END IF;

          INSERT INTO krateo_resources
            (created_at, updated_at, cluster_name, uid, global_uid, namespace,
            resource_group, resource_version, resource_kind, resource_plural,
            resource_name, raw, status_raw)
          VALUES
            (
              base_ts,
              base_ts + (i || ' minutes')::INTERVAL,
              c,
              uid_val,
              global_uid_val,
              ns,
              'widgets.templates.krateo.io',
              'v1beta1',
              'Panel',
              'panels',
              name_val,
              raw_val,
              status_raw_val
            )
          ON CONFLICT (global_uid) DO UPDATE SET
            updated_at = EXCLUDED.updated_at,
            raw        = EXCLUDED.raw,
            status_raw = EXCLUDED.status_raw;

          -- increment krateo-system counter
          IF ns = 'krateo-system' THEN
            ks_count := ks_count + 1;
          END IF;

        END LOOP;
      END LOOP;
    END LOOP;

    EXIT WHEN ks_count >= 5000;
  END LOOP;
END $$;
