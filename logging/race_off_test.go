//go:build !race

package logging_test

// raceDetectorEnabled is false in builds compiled without -race.
const raceDetectorEnabled = false
