package provider

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"limitdock/internal/readmodel"
)

type Logger interface {
	Printf(format string, args ...any)
}

type Reader interface {
	Name() string
	Read(ctx context.Context) (*readmodel.ReadModel, error)
}

type Aggregator struct {
	Readers []Reader
	Log     Logger
}

func (a Aggregator) Read(ctx context.Context) (*readmodel.ReadModel, error) {
	out := &readmodel.ReadModel{Snapshots: map[string]*readmodel.Snapshot{}}
	var firstErr error
	for _, reader := range a.Readers {
		if reader == nil {
			continue
		}
		model, err := reader.Read(ctx)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", reader.Name(), err)
			}
			if a.Log != nil {
				a.Log.Printf("Provider reader %s failed: %v", reader.Name(), err)
			}
			continue
		}
		mergeReadModel(out, model, reader.Name(), a.Log)
	}
	if len(out.Snapshots) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return orderReadModel(out), nil
}

func mergeReadModel(dst, src *readmodel.ReadModel, source string, log Logger) {
	if dst == nil || src == nil || len(src.Snapshots) == 0 {
		return
	}
	if dst.Snapshots == nil {
		dst.Snapshots = map[string]*readmodel.Snapshot{}
	}
	keys := make([]string, 0, len(src.Snapshots))
	for key := range src.Snapshots {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		snap := src.Snapshots[key]
		if snap == nil || key == "__invalid" {
			continue
		}
		if _, exists := dst.Snapshots[key]; exists {
			if log != nil {
				log.Printf("Provider reader %s skipped duplicate snapshot %s", source, key)
			}
			continue
		}
		dst.Snapshots[key] = snap
	}
}

func orderReadModel(model *readmodel.ReadModel) *readmodel.ReadModel {
	if model == nil || len(model.Snapshots) == 0 {
		return model
	}
	keys := make([]string, 0, len(model.Snapshots))
	for key := range model.Snapshots {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	ordered := map[string]*readmodel.Snapshot{}
	for _, key := range keys {
		ordered[key] = model.Snapshots[key]
	}
	model.Snapshots = ordered
	return model
}

func snapshotKey(parts ...string) string {
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	if len(out) == 0 {
		return "local"
	}
	return strings.Join(out, "-")
}
