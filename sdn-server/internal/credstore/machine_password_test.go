package credstore

import "testing"

// useTestMachinePassword supplies stable machine state without host permissions.
func useTestMachinePassword(t *testing.T) {
	t.Helper()
	saved := deriveMachinePassword
	deriveMachinePassword = func() (string, error) { return "test-machine-password-v4", nil }
	t.Cleanup(func() { deriveMachinePassword = saved })
}
