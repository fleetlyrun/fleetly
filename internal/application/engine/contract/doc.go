// Package contract defines the interfaces shared by the application engine
// kernel and application modules.
//
// The engine owns the reconciliation runtime (loops, watch fan-out, state
// transitions, ensure cache, maintenance lock, event recording, unit of work).
// Application modules own business state machines and expose their own API
// types. Modules depend on this package; the engine depends on this package;
// neither imports the other.
//
// Keep this package free of business types. Domain-specific helpers belong to
// their domain package; module APIs belong to the module package. Ports declared
// here may name domain value types and repo interfaces, but never application
// module types: the engine and its modules meet only at this leaf.
package contract
