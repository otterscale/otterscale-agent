package chisel

import "fmt"

// addressAllocator manages a pool of unique loopback addresses in the
// 127.x.x.x range. Each cluster is assigned a distinct address so
// that chisel can route reverse-tunnel traffic without port conflicts.
//
// All methods must be called with the parent Service's mu held.
type addressAllocator struct {
	usedHosts map[string]struct{}
}

func newAddressAllocator() *addressAllocator {
	return &addressAllocator{
		usedHosts: make(map[string]struct{}),
	}
}

// allocate picks a unique loopback address for the given cluster by
// hashing the name and probing linearly until an unused address is
// found.
func (a *addressAllocator) allocate(cluster string) (string, error) {
	base := hashKey(cluster)
	for i := range uint32(maxHosts) {
		candidate := hostFromIndex((base + i) % uint32(maxHosts))
		if _, exists := a.usedHosts[candidate]; exists {
			continue
		}
		a.usedHosts[candidate] = struct{}{}
		return candidate, nil
	}
	return "", fmt.Errorf("exhausted loopback address space (%d hosts)", maxHosts)
}

// release returns a previously allocated host to the pool.
func (a *addressAllocator) release(host string) {
	delete(a.usedHosts, host)
}
