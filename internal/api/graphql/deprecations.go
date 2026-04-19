package graphql

import (
	"log/slog"
	"sync"
	"sync/atomic"
)

var deprecatedFieldReads sync.Map

func recordDeprecatedFieldRead(field string) {
	if field == "" {
		return
	}
	counterAny, _ := deprecatedFieldReads.LoadOrStore(field, &atomic.Int64{})
	counter := counterAny.(*atomic.Int64)
	counter.Add(1)
	slog.Warn("deprecated_field_read", "event", "schema", "field", field)
}

func DeprecatedFieldReadsTotal() map[string]int64 {
	out := map[string]int64{}
	deprecatedFieldReads.Range(func(key, value any) bool {
		name, ok := key.(string)
		if !ok {
			return true
		}
		counter, ok := value.(*atomic.Int64)
		if !ok {
			return true
		}
		out[name] = counter.Load()
		return true
	})
	return out
}
