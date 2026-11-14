package node

import "time"

// Reconciliation is independent of peer connectivity and publishing, so an
// idle replica retires history too. Wait for hydration before any store write.
func (n *Node) runDatasetPublicationAvailability() {
	defer n.wg.Done()
	timer := time.NewTimer(datasetPublicationCatchupInitialDelay)
	defer timer.Stop()
	for {
		select {
		case <-n.ctx.Done():
			return
		case now := <-timer.C:
			if n.store.EngineHotWindowHydrated() && !n.store.EngineHotWindowHydrating() {
				retired, err := n.store.RetireUnavailableDatasetPublications(now)
				if err != nil {
					log.Errorf("Reconcile publication availability: %v", err)
				} else if retired > 0 {
					log.Infof("Retired %d publication listing rows unavailable for 24 hours", retired)
				}
			}
			timer.Reset(datasetPublicationCatchupInterval)
		}
	}
}
