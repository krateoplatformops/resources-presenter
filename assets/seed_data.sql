DO $$
DECLARE
  clusters   TEXT[] := ARRAY['prod-eu', 'prod-us', 'staging', 'dev'];
  namespaces TEXT[] := ARRAY['krateo-system', 'demo-system', 'ns-1', 'ns-2'];
  titles     TEXT[] := ARRAY['Blueprints', 'Deployments', 'Costs', 'Pipelines', 'Alerts', 'Metrics', 'Logs', 'Clusters', 'Users', 'Quotas'];
  sections   TEXT[] := ARRAY['dashboard', 'overview', 'admin'];
  c TEXT; ns TEXT; title TEXT; sec TEXT;
  i INT := 1;
  uid_val TEXT;
  name_val TEXT;
  raw_val JSONB;
BEGIN
  FOR rep IN 1..7 LOOP                                  -- repeat to reach 500
    FOREACH c IN ARRAY clusters LOOP
      FOREACH ns IN ARRAY namespaces[1:2] LOOP          -- 2 namespaces per cluster
        FOR ti IN 1..array_length(titles, 1) LOOP
          IF i > 500 THEN RETURN; END IF;
        title  := titles[ti];
        sec    := sections[1 + (i % array_length(sections, 1))];
        name_val := sec || '-' || lower(replace(title, ' ', '-')) || '-panel-' || i;
        uid_val  := 'uid-' || lpad(i::TEXT, 4, '0');

        raw_val := jsonb_build_object(
          'apiVersion', 'widgets.templates.krateo.io/v1beta1',
          'kind',       'Panel',
          'metadata',   jsonb_build_object(
            'name',      name_val,
            'namespace', ns,
            'labels',    jsonb_build_object('app.kubernetes.io/part-of', sec),
            'annotations', jsonb_build_object('krateo.io/verbose', (i % 2 = 0)::TEXT)
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

        INSERT INTO krateo_resources
          (updated_at, cluster_name, uid, global_uid, namespace, resource_kind, resource_name, raw)
        VALUES
          (now() - (i || ' minutes')::INTERVAL,
           c, uid_val, c || ':' || uid_val, ns,
           'widgets.templates.krateo.io/v1beta1.Panel', name_val, raw_val);

          i := i + 1;
        END LOOP;
      END LOOP;
    END LOOP;
  END LOOP;
END $$;
