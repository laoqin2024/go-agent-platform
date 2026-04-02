package collector

import (
	"context"
	"time"
)

// NetworkConnectionsSnapshot is a lightweight snapshot for periodic monitoring.
// It intentionally excludes startup/hotfix inventory to keep T2 collection cheap.
type NetworkConnectionsSnapshot struct {
	Connections []NetConnInfo `json:"connections"`
	CollectedAt time.Time     `json:"collected_at"`
}

type NetworkConnectionsCollector struct {
	timeout time.Duration
}

func NewNetworkConnectionsCollector() *NetworkConnectionsCollector {
	return &NetworkConnectionsCollector{timeout: 6 * time.Second}
}

func (c *NetworkConnectionsCollector) CollectWithContext(ctx context.Context) (NetworkConnectionsSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	conns, err := collectNetworkConnections(ctx)
	if err != nil {
		return NetworkConnectionsSnapshot{}, err
	}
	return NetworkConnectionsSnapshot{
		Connections: conns,
		CollectedAt: time.Now(),
	}, nil
}

