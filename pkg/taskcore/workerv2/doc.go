// Package workerv2 implements a step-driven worker architecture.
//
// Core idea:
//   - Engine: pure state transitions (Event -> Command).
//   - Runtime: trigger loops (poll/heartbeat/config) and command execution via Port.
//
// This split enables deterministic distributed-system testing by serializing
// interleavings at event/command boundaries without exposing internal private
// methods from the legacy worker package.
package workerv2
