package templatevalidate

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

// validationGauge reports each slot's BYO base-image validation state. For a
// slot, exactly one of result="valid"/result="invalid" is 1 and the other 0,
// so alerts can match `vibed_template_validation{result="invalid"} == 1`.
var validationGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Name: "vibed_template_validation",
	Help: "Bring-your-own base-image validation state per template slot (1 = current state; result=valid|invalid).",
}, []string{"template", "result"})

var registerOnce sync.Once

// RegisterMetrics registers the validation metric with controller-runtime's
// Prometheus registry (the controller's :8081 /metrics). Idempotent.
func RegisterMetrics() {
	registerOnce.Do(func() { ctrlmetrics.Registry.MustRegister(validationGauge) })
}

// RecordResults updates the metric from a validation pass.
func RecordResults(results []Result) {
	for _, r := range results {
		valid, invalid := 0.0, 1.0
		if r.Valid {
			valid, invalid = 1.0, 0.0
		}
		validationGauge.WithLabelValues(r.Template, "valid").Set(valid)
		validationGauge.WithLabelValues(r.Template, "invalid").Set(invalid)
	}
}
