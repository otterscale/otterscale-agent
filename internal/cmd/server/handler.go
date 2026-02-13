package server

import (
	"net/http"

	"connectrpc.com/connect"
	"connectrpc.com/grpchealth"
	"connectrpc.com/grpcreflect"
	"connectrpc.com/otelconnect"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/sdk/metric"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	fleetv1 "github.com/otterscale/otterscale-agent/api/fleet/v1/pbconnect"
	resourcev1 "github.com/otterscale/otterscale-agent/api/resource/v1/pbconnect"
	runtimev1 "github.com/otterscale/otterscale-agent/api/runtime/v1/pbconnect"
	"github.com/otterscale/otterscale-agent/internal/app"
)

// Handler is responsible for mounting all gRPC service handlers,
// interceptors, and operational endpoints (health, reflection,
// metrics) onto an HTTP mux.
type Handler struct {
	fleet    *app.FleetService
	resource *app.ResourceService
	runtime  *app.RuntimeService
}

// NewHandler returns a Handler for the given gRPC services.
func NewHandler(fleet *app.FleetService, resource *app.ResourceService, runtime *app.RuntimeService) *Handler {
	return &Handler{
		fleet:    fleet,
		resource: resource,
		runtime:  runtime,
	}
}

// Mount registers all gRPC service handlers, OTel interceptors, and
// operational endpoints onto the provided mux.
func (h *Handler) Mount(mux *http.ServeMux) error {
	// OpenTelemetry interceptor for automatic tracing and metrics.
	otelInterceptor, err := otelconnect.NewInterceptor()
	if err != nil {
		return err
	}

	interceptors := connect.WithInterceptors(
		otelInterceptor,
	)

	// Operational endpoints: gRPC reflection, health checks, Prometheus.
	services := []string{
		fleetv1.FleetServiceName,
		resourcev1.ResourceServiceName,
		runtimev1.RuntimeServiceName,
	}

	if err := h.registerOpsHandlers(mux, services); err != nil {
		return err
	}

	// Application service handlers.
	mux.Handle(fleetv1.NewFleetServiceHandler(h.fleet, interceptors))
	mux.Handle(resourcev1.NewResourceServiceHandler(h.resource, interceptors))
	mux.Handle(runtimev1.NewRuntimeServiceHandler(h.runtime, interceptors))

	return nil
}

// registerOpsHandlers sets up gRPC reflection, health checks, and
// Prometheus metrics scraping.
func (h *Handler) registerOpsHandlers(mux *http.ServeMux, serviceNames []string) error {
	reflector := grpcreflect.NewStaticReflector(serviceNames...)
	mux.Handle(grpcreflect.NewHandlerV1(reflector))
	mux.Handle(grpcreflect.NewHandlerV1Alpha(reflector))

	checker := grpchealth.NewStaticChecker(serviceNames...)
	mux.Handle(grpchealth.NewHandler(checker))

	exporter, err := prometheus.New()
	if err != nil {
		return err
	}
	// NOTE: This intentionally sets the global OTel MeterProvider so
	// that otelconnect interceptors and other libraries can discover
	// it without explicit injection. Ideally this would be injected
	// via Wire, but otelconnect relies on the global provider.
	otel.SetMeterProvider(metric.NewMeterProvider(metric.WithReader(exporter)))
	mux.Handle("/metrics", promhttp.Handler())

	return nil
}
