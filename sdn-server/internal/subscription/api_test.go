package subscription

import (
	"os"
	"regexp"
	"testing"
)

func TestSubscriptionFormUsesStandardCodesForDataTypeValues(t *testing.T) {
	source, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatalf("read api.go: %v", err)
	}
	forbidden := regexp.MustCompile(`name="dataTypes"\s+value="[A-Z]{3}\.fbs"`)
	if match := forbidden.Find(source); match != nil {
		t.Fatalf("subscription form exposes schema suffix in public dataTypes value: %s", string(match))
	}
}
