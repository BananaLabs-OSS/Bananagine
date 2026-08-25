package main

// retireServer destroys the runtime before changing allocation ownership.
// A non-not-found Docker failure means the runtime may still be active, so
// capacity, ports, and IPs must remain assigned to it.
func retireServer(
	id string,
	keepAllocations bool,
	newID string,
	destroy dockerServerDestroy,
	capacity *capacityTracker,
	portPools *portPoolSet,
	ipp *ipPool,
) error {
	if err := destroy(id); err != nil && !isDockerNotFound(err) {
		return err
	}

	capacity.release(id)
	if !keepAllocations {
		portPools.releaseByServer(id)
		ipp.releaseByServer(id)
	} else if newID != "" {
		portPools.reKey(id, newID)
		ipp.reKey(id, newID)
	}
	return nil
}
