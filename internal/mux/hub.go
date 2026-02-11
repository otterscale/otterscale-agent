package mux

import (
	"net/http"

	"connectrpc.com/connect"
	"connectrpc.com/grpchealth"
	"connectrpc.com/grpcreflect"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/sdk/metric"

	resourcev1 "github.com/otterscale/otterscale-agent/api/resource/v1/pbconnect"
	"github.com/otterscale/otterscale-agent/internal/app"
)

type Hub struct {
	*http.ServeMux

	resource *app.ResourceService
}

func NewHub(resource *app.ResourceService) *Hub {
	return &Hub{
		ServeMux: &http.ServeMux{},
		resource: resource,
	}
}

func (h *Hub) RegisterHandlers(opts []connect.HandlerOption) error {
	// Prepare service names for reflection and health check
	services := []string{
		resourcev1.ResourceServiceName,
	}

	// Register gRPC reflection
	reflector := grpcreflect.NewStaticReflector(services...)
	h.Handle(grpcreflect.NewHandlerV1(reflector))
	h.Handle(grpcreflect.NewHandlerV1Alpha(reflector))

	// Register gRPC health check
	checker := grpchealth.NewStaticChecker(services...)
	h.Handle(grpchealth.NewHandler(checker))

	// Register metrics endpoint
	if err := h.registerMetrics(); err != nil {
		return err
	}

	// Register Prometheus proxy
	h.registerProxy()

	// Register WebSocket handler
	h.registerWebSocket()

	// Register service handlers
	h.Handle(resourcev1.NewResourceServiceHandler(h.resource, opts...))

	return nil
}

func (h *Hub) registerMetrics() error {
	exporter, err := prometheus.New()
	if err != nil {
		return err
	}
	otel.SetMeterProvider(metric.NewMeterProvider(metric.WithReader(exporter)))
	h.Handle("/metrics", promhttp.Handler())
	return nil
}

// TODO: Implement Prometheus proxy
func (h *Hub) registerProxy() {
	// proxy := httputil.NewSingleHostReverseProxy(h.environment.GetPrometheusURL())
	// proxy.ModifyResponse = func(resp *http.Response) error {
	// 	resp.Header.Del("Access-Control-Allow-Origin")
	// 	return nil
	// }
	// h.Handle("/prometheus/", http.StripPrefix("/prometheus", proxy))
}

// TODO: Implement WebSocket handler
func (h *Hub) registerWebSocket() {
	// h.HandleFunc(h.instance.VNCPathPrefix(), h.instance.VNCHandler())
}
