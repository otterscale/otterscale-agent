package integration

import (
	"context"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/otterscale/otterscale-agent/internal/core"
	"github.com/otterscale/otterscale-agent/internal/pki"
	"github.com/otterscale/otterscale-agent/internal/providers/chisel"
	tunneltransport "github.com/otterscale/otterscale-agent/internal/transport/tunnel"
)

func TestFleetRegisterClusterUsesSingleSharedTunnelPort(t *testing.T) {
	tunnel := chisel.NewService()
	initTunnelServer(t, tunnel)
	fleet := core.NewFleetUseCase(tunnel, "test")

	csrA := generateCSR(t, "agent-a")
	csrB := generateCSR(t, "agent-b")

	regA, err := fleet.RegisterCluster("cluster-a", "agent-a", "test", csrA)
	if err != nil {
		t.Fatalf("register cluster-a: %v", err)
	}
	regB, err := fleet.RegisterCluster("cluster-b", "agent-b", "test", csrB)
	if err != nil {
		t.Fatalf("register cluster-b: %v", err)
	}

	if len(regA.Certificate) == 0 || len(regB.Certificate) == 0 {
		t.Fatal("expected non-empty certificates")
	}
	if len(regA.CACertificate) == 0 || len(regB.CACertificate) == 0 {
		t.Fatal("expected non-empty CA certificates")
	}

	if regA.Endpoint == "" || regB.Endpoint == "" {
		t.Fatalf("expected non-empty tunnel endpoints, got endpointA=%q endpointB=%q", regA.Endpoint, regB.Endpoint)
	}
	if regA.Endpoint == regB.Endpoint {
		t.Fatalf("expected distinct endpoints for different clusters, got %q", regA.Endpoint)
	}

	addrA, err := tunnel.ResolveAddress("cluster-a")
	if err != nil {
		t.Fatalf("resolve cluster-a: %v", err)
	}
	addrB, err := tunnel.ResolveAddress("cluster-b")
	if err != nil {
		t.Fatalf("resolve cluster-b: %v", err)
	}

	if !strings.HasSuffix(addrA, ":16598") || !strings.HasSuffix(addrB, ":16598") {
		t.Fatalf("expected resolved addresses to use shared port 16598, got addrA=%q addrB=%q", addrA, addrB)
	}
}

func TestFleetRegisterClusterLatestAgentWinsForSameCluster(t *testing.T) {
	tunnel := chisel.NewService()
	initTunnelServer(t, tunnel)
	fleet := core.NewFleetUseCase(tunnel, "test")

	csr1 := generateCSR(t, "agent-r-1")
	csr2 := generateCSR(t, "agent-r-2")

	_, err := fleet.RegisterCluster("cluster-r", "agent-r-1", "test", csr1)
	if err != nil {
		t.Fatalf("register agent-r-1: %v", err)
	}
	reg2, err := fleet.RegisterCluster("cluster-r", "agent-r-2", "test", csr2)
	if err != nil {
		t.Fatalf("register agent-r-2: %v", err)
	}

	// After re-registration the route must resolve to the latest
	// agent's endpoint regardless of whether the host was reused.
	addr, err := tunnel.ResolveAddress("cluster-r")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if addr != "http://"+reg2.Endpoint {
		t.Fatalf("expected resolve to use latest agent endpoint %q, got %q", reg2.Endpoint, addr)
	}

	// Only one cluster should be registered.
	clusters := tunnel.ListClusters()
	if len(clusters) != 1 || slices.Collect(maps.Keys(clusters))[0] != "cluster-r" {
		t.Fatalf("expected exactly one cluster 'cluster-r', got %v", clusters)
	}
}

func TestFleetRegisterClusterReregisterAndReplaceAcrossAgents(t *testing.T) {
	tunnel := chisel.NewService()
	initTunnelServer(t, tunnel)
	fleet := core.NewFleetUseCase(tunnel, "test")

	csrA := generateCSR(t, "agent-a")
	csrB := generateCSR(t, "agent-b")

	regA1, err := fleet.RegisterCluster("cluster-z", "agent-a", "test", csrA)
	if err != nil {
		t.Fatalf("register agent-a #1: %v", err)
	}

	regB, err := fleet.RegisterCluster("cluster-z", "agent-b", "test", csrB)
	if err != nil {
		t.Fatalf("register agent-b: %v", err)
	}

	// After re-registration for the same cluster, the route must
	// resolve to the latest agent's endpoint.
	addrB, err := tunnel.ResolveAddress("cluster-z")
	if err != nil {
		t.Fatalf("resolve after agent-b register: %v", err)
	}
	if addrB != "http://"+regB.Endpoint {
		t.Fatalf("expected resolve to point to agent-b endpoint %q, got %q", regB.Endpoint, addrB)
	}

	regA2, err := fleet.RegisterCluster("cluster-z", "agent-a", "test", csrA)
	if err != nil {
		t.Fatalf("register agent-a #2: %v", err)
	}

	// Each registration produces a distinct certificate (different
	// serial numbers) so the derived auth must differ.
	authA1, err := pki.DeriveAuth("agent-a", regA1.Certificate)
	if err != nil {
		t.Fatalf("derive auth A1: %v", err)
	}
	authA2, err := pki.DeriveAuth("agent-a", regA2.Certificate)
	if err != nil {
		t.Fatalf("derive auth A2: %v", err)
	}
	if authA1 == authA2 {
		t.Fatal("expected auth rotation for same agent re-register")
	}

	for i := 0; i < 3; i++ {
		addr, err := tunnel.ResolveAddress("cluster-z")
		if err != nil {
			t.Fatalf("resolve #%d: %v", i+1, err)
		}
		if addr != "http://"+regA2.Endpoint {
			t.Fatalf("expected only re-registered route to be selected, got %q", addr)
		}
	}
}

func initTunnelServer(t *testing.T, tunnel *chisel.Service) {
	t.Helper()

	// Initialize a CA for the tunnel provider so that CSR signing
	// works during tests.
	ca, err := pki.NewCAFromSeed("test-ca-seed")
	if err != nil {
		t.Fatalf("create CA: %v", err)
	}
	tunnel.SetCA(ca)

	srv, err := tunneltransport.NewServer(
		tunneltransport.WithServer(tunnel.ServerRef()),
	)
	if err != nil {
		t.Fatalf("init tunnel server: %v", err)
	}
	t.Cleanup(func() {
		_ = srv.Stop(context.Background())
	})
}

// generateCSR creates a fresh ECDSA key pair and PEM-encoded CSR for
// the given common name.
func generateCSR(t *testing.T, cn string) []byte {
	t.Helper()
	key, _, err := pki.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	csr, err := pki.GenerateCSR(key, cn)
	if err != nil {
		t.Fatalf("generate CSR: %v", err)
	}
	return csr
}
