//go:build !race

package cable

// raceBuild is false in ordinary (non -race) builds; see racebuild_on_test.go.
const raceBuild = false
