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

	resourcev1 "github.com/otterscale/otterscale-agent/api/resource/v1/pbconnect"
	"github.com/otterscale/otterscale-agent/internal/app"
)

type Handler struct {
	resource *app.ResourceService
}

func NewHandler(resource *app.ResourceService) *Handler {
	return &Handler{
		resource: resource,
	}
}

// Mount registers all handlers, middlewares, and observability tools to the mux.
func (h *Handler) Mount(mux *http.ServeMux) error {
	// Prepare Interceptors
	otelInterceptor, err := otelconnect.NewInterceptor()
	if err != nil {
		return err
	}

	interceptors := connect.WithInterceptors(
		otelInterceptor,
	)

	// Register Observability & Operations (Reflection, Health, Metrics)
	services := []string{
		resourcev1.ResourceServiceName,
	}

	if err := h.registerOpsHandlers(mux, services); err != nil {
		return err
	}

	// Register Service Handlers
	mux.Handle(resourcev1.NewResourceServiceHandler(h.resource, interceptors))

	// Register Pending Implementations (TODOs)
	h.registerProxy(mux)
	h.registerWebSocket(mux)

	return nil
}

// registerOpsHandlers sets up Reflection, Health Check, and Metrics.
func (h *Handler) registerOpsHandlers(mux *http.ServeMux, serviceNames []string) error {
	// gRPC Reflection
	reflector := grpcreflect.NewStaticReflector(serviceNames...)
	mux.Handle(grpcreflect.NewHandlerV1(reflector))
	mux.Handle(grpcreflect.NewHandlerV1Alpha(reflector))

	// gRPC Health Check
	checker := grpchealth.NewStaticChecker(serviceNames...)
	mux.Handle(grpchealth.NewHandler(checker))

	// Prometheus Metrics
	exporter, err := prometheus.New()
	if err != nil {
		return err
	}
	otel.SetMeterProvider(metric.NewMeterProvider(metric.WithReader(exporter)))
	mux.Handle("/metrics", promhttp.Handler())

	return nil
}

// TODO: Implement Prometheus proxy
func (h *Handler) registerProxy(mux *http.ServeMux) {
	// proxy := httputil.NewSingleHostReverseProxy(h.environment.GetPrometheusURL())
	// proxy.ModifyResponse = func(resp *http.Response) error {
	// 	resp.Header.Del("Access-Control-Allow-Origin")
	// 	return nil
	// }
	// mux.Handle("/prometheus/", http.StripPrefix("/prometheus", proxy))
}

// TODO: Implement WebSocket handler
func (h *Handler) registerWebSocket(mux *http.ServeMux) {
	// mux.HandleFunc(h.instance.VNCPathPrefix(), h.instance.VNCHandler())
}
