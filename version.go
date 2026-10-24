package main

// version is the release this binary was built from. CI overwrites this
// file with the dispatch run's version input on release builds; pull
// request builds and local builds keep "dev", which never prompts for
// updates.
//
// This file must stay stamp-only: the stamp step in ci.yml replaces its
// entire contents. Anything else (parseSemver, updateAvailable, …) belongs
// in semver.go.
var version = "dev"
