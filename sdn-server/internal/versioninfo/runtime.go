package versioninfo

// AgentName is the name half of the libp2p identify agent string, and it is
// the ONLY half anything matches on. The accounts-board membership gate
// (internal/epm/sdnpeers.go isSDNAgentVersion) and the browser's
// hasSdnIdentityEvidence both ask whether the agent string CONTAINS this name;
// neither has ever looked at the version half. That is what lets the version
// half move without stranding a node on anyone's board.
const AgentName = "spacedatanetwork"

// AgentVersion is the libp2p identify agent string this build presents, and
// the version it carries is the version the binary reports everywhere else:
// the release tag when the binary was cut as a release, else the suite
// version.
//
// This is a var and not a const on purpose. The release stamp arrives as
//
//	-ldflags "-X ...versioninfo.ReleaseTag=v1.0.5-beta.67"
//
// which is a link-time write into a string VARIABLE's data. It cannot reach a
// const, which the compiler has already folded into every use. AgentVersion
// was born a const over SuiteVersion before the stamp mechanism existed, so
// every 1.0.5 build — release, beta and dev alike — advertised the
// byte-identical "spacedatanetwork/1.0.5" and no peer could tell them apart.
//
// It derives from Version() rather than re-deriving the tag itself so that
// "what version is this binary" has exactly one answer in exactly one place.
//
// It is also deliberately PINNED for the life of the process: libp2p takes the
// user agent once, at host construction, and pushes that value to peers over
// identify. A value that could change afterwards would leave two peers holding
// different answers for the same node with no way to tell which is current.
// ReleaseTag is written by the linker before any initializer runs, so this
// initializer already sees the stamped value.
var AgentVersion = AgentName + "/" + Version()
