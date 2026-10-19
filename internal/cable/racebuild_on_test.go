//go:build race

package cable

// raceBuild is true when the test binary is built with -race. Used only by
// TestPublishAllocsBounded to select between exact and tolerant allocation
// pins (see that test for why), never to weaken functional assertions.
const raceBuild = true
