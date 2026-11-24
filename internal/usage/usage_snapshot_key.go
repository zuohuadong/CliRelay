package usage

import "github.com/router-for-me/CLIProxyAPI/v6/internal/cluster"

// usageSnapshotKey is the key the in-memory statistics snapshot is saved under.
//
// The store is shared with the other nodes of a cluster, so with a single fixed
// key each node's periodic save would overwrite the others' snapshots. In
// cluster mode the key therefore carries the node ID. It is resolved on every
// use: StartService installs the cluster coordinator (cluster.Prepare) before
// the snapshot is first loaded. A single node keeps the historical key, so the
// snapshot it saved before this change still loads.
func usageSnapshotKey() string {
	if c := cluster.Default(); c.Enabled() {
		return usageSnapshotBaseKey + ":" + c.NodeID()
	}
	return usageSnapshotBaseKey
}
