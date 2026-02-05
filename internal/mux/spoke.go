package mux

import (
	"net/http"

	"connectrpc.com/connect"

	resourcev1 "github.com/otterscale/otterscale-agent/api/resource/v1/pbconnect"
)

// SpokeResourceHandler is the ResourceService implementation used by the agent spoke.
type SpokeResourceHandler interface {
	resourcev1.ResourceServiceHandler
}

type Spoke struct {
	*http.ServeMux

	resource SpokeResourceHandler
}

func NewSpoke(resource SpokeResourceHandler) *Spoke {
	return &Spoke{
		ServeMux: &http.ServeMux{},
		resource: resource,
	}
}

func (s *Spoke) RegisterHandlers(opts []connect.HandlerOption) error {
	// Health endpoint for tunnel keepalive/readiness checks.
	s.HandleFunc("/ping", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	// Core service handlers.
	s.Handle(resourcev1.NewResourceServiceHandler(s.resource, opts...))
	return nil
}
