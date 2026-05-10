package openusage

import (
	"context"

	"limitdock/internal/readmodel"
)

type Reader struct {
	Client readmodel.Client
}

func (r Reader) Name() string {
	return "openusage"
}

func (r Reader) Read(ctx context.Context) (*readmodel.ReadModel, error) {
	model, _, err := r.Client.Read(ctx, map[string]any{})
	return model, err
}
