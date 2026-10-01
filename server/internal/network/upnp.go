package network

import "fmt"

// detectUPnP attempts to discover a UPnP IGD on the local network.
// For the MVP phase it returns false; a full implementation will use
// github.com/huin/goupnp to discover the router and attempt port mapping.
func detectUPnP() (bool, error) {
	// TODO(P0-8 Phase 2): integrate goupnp for UPnP/NAT-PMP/PCP discovery.
	return false, fmt.Errorf("UPnP discovery not yet implemented")
}
