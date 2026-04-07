package telemetry

import (
	"context"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
)

type Config struct {
	Enabled        bool
	ServiceName    string
	ExportInterval time.Duration
}

type Metrics struct {
	startupSuccess   metric.Int64Counter
	startupFailure   metric.Int64Counter
	dbConnectDurSecs metric.Float64Histogram

	httpRequests         metric.Int64Counter
	httpDurSecs          metric.Float64Histogram
	httpResources        metric.Int64Counter
	httpErrors           metric.Int64Counter
	httpPhaseDurSecs     metric.Float64Histogram
	httpDiscoveredTarget metric.Int64Counter
	httpAllowedTarget    metric.Int64Counter
}

func Setup(ctx context.Context, log *slog.Logger, cfg Config) (*Metrics, func(context.Context) error, error) {
	if !cfg.Enabled {
		return nil, func(context.Context) error { return nil }, nil
	}

	exporter, err := otlpmetrichttp.New(ctx)
	if err != nil {
		return nil, nil, err
	}

	resource, err := resource.Merge(resource.Default(),
		resource.NewSchemaless(attribute.String("service.name", cfg.ServiceName)))
	if err != nil {
		return nil, nil, err
	}

	options := []sdkmetric.PeriodicReaderOption{}
	if cfg.ExportInterval > 0 {
		options = append(options, sdkmetric.WithInterval(cfg.ExportInterval))
	}

	reader := sdkmetric.NewPeriodicReader(exporter, options...)
	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(reader),
		sdkmetric.WithResource(resource),
	)
	otel.SetMeterProvider(provider)

	meter := provider.Meter("github.com/krateoplatformops/resources-presenter")
	metrics, err := newMetrics(meter)
	if err != nil {
		_ = provider.Shutdown(ctx)
		return nil, nil, err
	}

	log.Info("OpenTelemetry metrics initialized")
	return metrics, provider.Shutdown, nil
}

func newMetrics(meter metric.Meter) (*Metrics, error) {
	var err error
	m := &Metrics{}

	if m.startupSuccess, err = meter.Int64Counter("resources_presenter.startup.success"); err != nil {
		return nil, err
	}
	if m.startupFailure, err = meter.Int64Counter("resources_presenter.startup.failure"); err != nil {
		return nil, err
	}
	if m.dbConnectDurSecs, err = meter.Float64Histogram("resources_presenter.db.connect.duration_seconds"); err != nil {
		return nil, err
	}
	if m.httpRequests, err = meter.Int64Counter("resources_presenter.http.requests"); err != nil {
		return nil, err
	}
	if m.httpDurSecs, err = meter.Float64Histogram("resources_presenter.http.duration_seconds"); err != nil {
		return nil, err
	}
	if m.httpResources, err = meter.Int64Counter("resources_presenter.http.resources_returned"); err != nil {
		return nil, err
	}
	if m.httpErrors, err = meter.Int64Counter("resources_presenter.http.errors"); err != nil {
		return nil, err
	}
	if m.httpPhaseDurSecs, err = meter.Float64Histogram("resources_presenter.http.phase.duration_seconds"); err != nil {
		return nil, err
	}
	if m.httpDiscoveredTarget, err = meter.Int64Counter("resources_presenter.http.discovery.targets"); err != nil {
		return nil, err
	}
	if m.httpAllowedTarget, err = meter.Int64Counter("resources_presenter.http.rbac.targets_allowed"); err != nil {
		return nil, err
	}

	return m, nil
}

func (m *Metrics) IncStartupSuccess(ctx context.Context) {
	if m == nil {
		return
	}
	m.startupSuccess.Add(ctx, 1)
}

func (m *Metrics) IncStartupFailure(ctx context.Context) {
	if m == nil {
		return
	}
	m.startupFailure.Add(ctx, 1)
}

func (m *Metrics) RecordDBConnectDuration(ctx context.Context, d time.Duration) {
	if m == nil {
		return
	}
	m.dbConnectDurSecs.Record(ctx, d.Seconds())
}

func (m *Metrics) RecordHTTPRequest(ctx context.Context, handler, method string, status int, d time.Duration) {
	if m == nil {
		return
	}
	attrs := metric.WithAttributes(
		attribute.String("handler", handler),
		attribute.String("method", method),
		attribute.Int("status_code", status),
	)
	m.httpRequests.Add(ctx, 1, attrs)
	m.httpDurSecs.Record(ctx, d.Seconds(), attrs)
}

func (m *Metrics) AddResourcesReturned(ctx context.Context, handler, method string, n int64) {
	if m == nil || n <= 0 {
		return
	}
	m.httpResources.Add(ctx, n,
		metric.WithAttributes(
			attribute.String("handler", handler),
			attribute.String("method", method),
		))
}

func (m *Metrics) IncHTTPError(ctx context.Context, handler, method, stage string, status int) {
	if m == nil {
		return
	}
	m.httpErrors.Add(ctx, 1,
		metric.WithAttributes(
			attribute.String("handler", handler),
			attribute.String("method", method),
			attribute.String("stage", stage),
			attribute.Int("status_code", status),
		))
}

func (m *Metrics) RecordHTTPPhaseDuration(ctx context.Context, handler, phase string, d time.Duration) {
	if m == nil {
		return
	}
	m.httpPhaseDurSecs.Record(ctx, d.Seconds(),
		metric.WithAttributes(
			attribute.String("handler", handler),
			attribute.String("phase", phase),
		))
}

func (m *Metrics) AddDiscoveredTargets(ctx context.Context, handler string, n int64) {
	if m == nil || n <= 0 {
		return
	}
	m.httpDiscoveredTarget.Add(ctx, n,
		metric.WithAttributes(attribute.String("handler", handler)))
}

func (m *Metrics) AddAllowedTargets(ctx context.Context, handler string, n int64) {
	if m == nil || n <= 0 {
		return
	}
	m.httpAllowedTarget.Add(ctx, n,
		metric.WithAttributes(attribute.String("handler", handler)))
}
