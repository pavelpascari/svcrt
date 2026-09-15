// Package kit composes svcrt's runtime modules into the stacks a service
// actually wants, with the orderings that are silently wrong when inverted.
//
// It is opt-in and deliberately thin. Nothing else in svcrt imports it, the
// modules it composes stay independently usable, and its options embed their
// types rather than mirroring them -- so a service that outgrows kit deletes
// one import and writes the four lines itself.
//
// The cost of adopting it is stated plainly: a service on kit upgrades kit
// rather than upgrading resilience or telemetry independently. That is the
// trade, and staying thin enough to leave is what makes it safe.
//
// kit ships no server-side helper. A service builds one handler chain in one
// visible place but a client per upstream, and it is the repeated case that
// earns a constructor. See telemetry.Server's documentation for the server
// ordering, which remains the caller's to get right.
package kit
