package connector

// APIVersion is the connector ABI version string. Third-party connectors must
// return this exact value from Ping; the Registry rejects mismatches.
//
// Bumping rules:
//   PATCH for documentation-only changes
//   MINOR for additive proto changes (new fields with new field numbers)
//   MAJOR for any breaking change — and a new package data_governance.connector.v2
const APIVersion = "v1.0.0"
