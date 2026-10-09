package postgres_test

import (
	"os"
	"testing"
	"time"
	_ "time/tzdata" // Europe/London loads on a host without a zone database.
)

// TestMain runs the package's tests with time.Local set to Europe/London,
// so a File or Directory the store returned in time.Local, not time.UTC,
// fails the conformance suite's checks whatever zone the host runs in.
func TestMain(m *testing.M) {
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		panic(err)
	}
	time.Local = london
	os.Exit(m.Run())
}
