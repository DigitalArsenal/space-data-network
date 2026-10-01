package format4

import "time"

// Test hooks for the external test package (format4_test), which may import
// format4test (an internal test may not: format4test imports format4).

// AttachForTest builds an Engine over a mailbox in mem, programmed from the
// raw layout block, with ctl as its control surface: the engine double in
// double_test.go stands in for the instance.
func AttachForTest(mem memory, h host, ctl control, rawLayout []byte) (*Engine, error) {
	e := &Engine{}
	if err := e.attach(mem, h, ctl, rawLayout); err != nil {
		return nil, err
	}
	return e, nil
}

// SetAbandonGrace shortens the cancel grace for a test; the func restores it.
func SetAbandonGrace(d time.Duration) func() {
	old := abandonGrace
	abandonGrace = d
	return func() { abandonGrace = old }
}

// EncodeConfigForTest exposes flatsql_p4_init's config encoding.
func EncodeConfigForTest(engineRoot string, opt Options) []byte { return encodeConfig(engineRoot, opt) }
