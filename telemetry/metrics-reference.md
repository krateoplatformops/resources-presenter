# Resources Presenter Metrics Reference

This document describes the OpenTelemetry metrics emitted by `resources-presenter`.

## Naming note

In code, metric names use dots (for example `resources_presenter.startup.success`).
In Prometheus, names are typically normalized with underscores (for example `resources_presenter_startup_success`), and counters may be exposed with `_total`.

## Metrics

| Metric | Type | Unit | Description | Emitted from | PromQL example |
|---|---|---|---|---|---|
| `resources_presenter.startup.success` | Counter | count | Service startup completed successfully. | `main.go` | `sum(increase(resources_presenter_startup_success_total[1h]))` |
| `resources_presenter.startup.failure` | Counter | count | Service startup failed. | `main.go` | `sum(increase(resources_presenter_startup_failure_total[1h]))` |
| `resources_presenter.db.connect.duration_seconds` | Histogram | seconds | Time spent waiting for PostgreSQL readiness. | `main.go` | `histogram_quantile(0.95, sum by (le) (rate(resources_presenter_db_connect_duration_seconds_bucket[5m])))` |
| `resources_presenter.http.requests` | Counter | requests | Number of HTTP handler requests. Labels: `handler`, `method`, `status_code`. | `internal/handlers/resources.go` | `sum by (handler, method) (rate(resources_presenter_http_requests_total[5m]))` |
| `resources_presenter.http.duration_seconds` | Histogram | seconds | HTTP handler latency. Labels: `handler`, `method`, `status_code`. | `internal/handlers/resources.go` | `histogram_quantile(0.95, sum by (le, handler) (rate(resources_presenter_http_duration_seconds_bucket[5m])))` |
| `resources_presenter.http.resources_returned` | Counter | resources | Number of resources returned by handlers. Labels: `handler`, `method`. | `internal/handlers/resources.go` | `sum by (handler) (rate(resources_presenter_http_resources_returned_total[5m]))` |
| `resources_presenter.http.errors` | Counter | errors | Errors in handler flows. Labels: `handler`, `method`, `stage`, `status_code`. | `internal/handlers/resources.go` | `sum by (handler, stage) (rate(resources_presenter_http_errors_total[5m]))` |
| `resources_presenter.http.phase.duration_seconds` | Histogram | seconds | Per-phase handler latency. Labels: `handler`, `phase`. | `internal/handlers/helpers.go` | `histogram_quantile(0.95, sum by (le, handler, phase) (rate(resources_presenter_http_phase_duration_seconds_bucket[5m])))` |
| `resources_presenter.http.discovery.targets` | Counter | targets | Number of targets discovered before RBAC filtering. Label: `handler`. | `internal/handlers/resources.go` | `sum(rate(resources_presenter_http_discovery_targets_total[5m]))` |
| `resources_presenter.http.rbac.targets_allowed` | Counter | targets | Number of targets allowed by RBAC filtering. Label: `handler`. | `internal/handlers/resources.go` | `sum(rate(resources_presenter_http_rbac_targets_allowed_total[5m]))` |

## Handler labels

The `handler` label is intentionally low-cardinality:

- `resources`: `GET`/`POST /resources`
- `resource_detail`: `GET /resources/{global_uid}`

The `stage` and `phase` labels are fixed strings from the code path. Dynamic identifiers such as `global_uid`, resource names, trace IDs, user names, and namespaces are intentionally not used as metric labels.

## Cardinality guidance

- Avoid high-cardinality labels (`global_uid`, `resource_name`, dynamic IDs).
- Keep metrics at service-level and low-cardinality.
- Current labels are bounded: `handler`, `method`, `status_code`, `stage`, `phase`.
