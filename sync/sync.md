# Sync

Syncer implements efficient synchronization for headers. 

There are two main processes running in Syncer:
* Main syncing loop(`syncLoop`)
    * Performs syncing from the latest stored header up to the latest known Subjective Head
    * Syncs by requesting missing headers from Exchange or
    * By accessing cache of pending headers
* Receives every new Network Head from PubSub gossip subnetwork (`incomingNetworkHead`)
    * Validates against the latest known Subjective Head, is so
    * Sets as the new Subjective Head, which
    * If there is a gap between the previous and the new Subjective Head
    * Triggers s.syncLoop and saves the Subjective Head in the pending so s.syncLoop can access it