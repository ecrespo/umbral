// Package integration holds the workspace tree's tests against a real SQLite database.
//
// They are integration tests on purpose. The identifier allocator, the alias table and the
// layout rewrites are all statements about what SQLite does under a transaction — a fake
// store would prove that the service calls it, not that the tree survives being written
// down.
package integration
