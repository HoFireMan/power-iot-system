package deployment

import "power-iot-backend/internal/runtimegate"

// WriteGate remains available to the private deployment workflow while the
// reusable ingress gate lives in the public runtime boundary.
type WriteGate = runtimegate.WriteGate

func NewWriteGate(blocked bool) *WriteGate {
	return runtimegate.NewWriteGate(blocked)
}
