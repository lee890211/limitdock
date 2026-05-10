package provider

import (
	"context"

	"limitdock/internal/readmodel"
)

type OpenUsageReader struct {
	Client       readmodel.Client
	SettingsPath string
	Log          Logger
}

func (r OpenUsageReader) Name() string {
	return "openusage"
}

func (r OpenUsageReader) Read(ctx context.Context) (*readmodel.ReadModel, error) {
	return fetchOpenUsageMerged(ctx, r.Client, r.SettingsPath, r.Log)
}
